package compute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	exeletfs "exe.dev/exelet/fs"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// ReceiveVM receives a VM from another exelet.
func (s *Service) ReceiveVM(stream api.ComputeService_ReceiveVMServer) error {
	ctx := stream.Context()

	// Receive start request
	req, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to receive start request: %v", err)
	}
	startReq := req.GetStart()
	if startReq == nil {
		return status.Error(codes.InvalidArgument, "first message must be ReceiveVMStartRequest")
	}

	instanceID := startReq.InstanceID
	s.log.InfoContext(ctx, "ReceiveVM started", "instance", instanceID)

	// Check if instance already exists
	if _, err := s.loadInstanceConfig(instanceID); err == nil {
		return status.Errorf(codes.AlreadyExists, "instance %s already exists", instanceID)
	} else if !errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.Internal, "failed to check instance existence: %v", err)
	}

	// Check if base image exists locally
	hasBaseImage := false
	if startReq.BaseImageID != "" {
		if _, err := s.context.StorageManager.Get(ctx, startReq.BaseImageID); err == nil {
			hasBaseImage = true
		}
	}

	// Send ready response
	if err := stream.Send(&api.ReceiveVMResponse{
		Type: &api.ReceiveVMResponse_Ready{
			Ready: &api.ReceiveVMReady{
				HasBaseImage: hasBaseImage,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send ready: %v", err)
	}

	s.log.DebugContext(ctx, "sent ready response", "instance", instanceID, "has_base_image", hasBaseImage)

	// Setup rollback
	rb := &receiveVMRollback{
		ctx:            ctx,
		log:            s.log,
		storageManager: s.context.StorageManager,
		instanceID:     instanceID,
		instanceDir:    s.getInstanceDir(instanceID),
		baseImageID:    startReq.BaseImageID,
	}
	var receiveErr error
	defer func() {
		if receiveErr != nil {
			rb.Rollback()
		}
	}()

	// Store encryption key if provided
	if startReq.Encrypted && len(startReq.EncryptionKey) > 0 {
		if err := s.context.StorageManager.SetEncryptionKey(instanceID, startReq.EncryptionKey); err != nil {
			receiveErr = status.Errorf(codes.Internal, "failed to store encryption key: %v", err)
			return receiveErr
		}
		rb.encryptionKeyCreated = true
	}

	// zfsReceiver manages a zfs recv process with a pipe
	type zfsReceiver struct {
		id     string
		pw     *io.PipeWriter
		errCh  chan error
		err    error
		closed bool
	}

	startZfsRecv := func(id string) *zfsReceiver {
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			err := s.context.StorageManager.ReceiveSnapshot(ctx, id, pr)
			errCh <- err
			pr.Close()
		}()
		return &zfsReceiver{id: id, pw: pw, errCh: errCh}
	}

	checkRecvError := func(recv *zfsReceiver) error {
		if recv.closed {
			return recv.err
		}
		select {
		case recv.err = <-recv.errCh:
			recv.closed = true
			return recv.err
		default:
			return nil
		}
	}

	waitRecvComplete := func(recv *zfsReceiver) error {
		if recv.closed {
			return recv.err
		}
		recv.pw.Close()
		recv.err = <-recv.errCh
		recv.closed = true
		return recv.err
	}

	// Receive and pipe data chunks
	hasher := sha256.New()
	var totalBytes uint64
	var expectedChecksum string

	// Track current receiver (may switch from base image to instance)
	var baseImageRecv *zfsReceiver
	var instanceRecv *zfsReceiver
	var currentRecv *zfsReceiver
	receivingBaseImage := false

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if currentRecv != nil {
				currentRecv.pw.CloseWithError(err)
			}
			receiveErr = status.Errorf(codes.Internal, "failed to receive: %v", err)
			return receiveErr
		}

		switch v := req.Type.(type) {
		case *api.ReceiveVMRequest_Data:
			// Handle transition from base image to instance data
			if v.Data.IsBaseImage && !receivingBaseImage {
				// Validate base image ID is set - without this we'd target the pool root
				if startReq.BaseImageID == "" {
					receiveErr = status.Error(codes.InvalidArgument,
						"received base image data but BaseImageID is empty")
					return receiveErr
				}
				// Receiving base image data
				if hasBaseImage {
					// Target already has base image but sender is sending it anyway.
					// This is a protocol mismatch - the sender should have been told
					// TargetHasBaseImage=true to send a full stream instead.
					// We cannot skip the base image and then receive incremental because
					// ZFS incremental streams require the exact origin snapshot GUID.
					receiveErr = status.Errorf(codes.FailedPrecondition,
						"received base image data but target already has base image %s; "+
							"sender should use TargetHasBaseImage=true for full stream transfer",
						startReq.BaseImageID)
					return receiveErr
				}
				// Start receiving base image
				s.log.DebugContext(ctx, "starting base image receive", "base_image", startReq.BaseImageID)
				baseImageRecv = startZfsRecv(startReq.BaseImageID)
				currentRecv = baseImageRecv
				receivingBaseImage = true
				rb.baseImageCreated = true
			} else if !v.Data.IsBaseImage && receivingBaseImage {
				// Transition from base image to instance - complete base image recv first
				s.log.DebugContext(ctx, "completing base image receive, starting instance receive")
				if err := waitRecvComplete(baseImageRecv); err != nil {
					receiveErr = status.Errorf(codes.Internal, "base image zfs recv failed: %v", err)
					return receiveErr
				}
				// Start instance receiver (incremental from base image)
				instanceRecv = startZfsRecv(instanceID)
				currentRecv = instanceRecv
				receivingBaseImage = false
			} else if !v.Data.IsBaseImage && currentRecv == nil {
				// No base image data sent - receiving full stream directly
				s.log.DebugContext(ctx, "starting full instance receive")
				instanceRecv = startZfsRecv(instanceID)
				currentRecv = instanceRecv
			}

			hasher.Write(v.Data.Data)
			totalBytes += uint64(len(v.Data.Data))
			if _, err := currentRecv.pw.Write(v.Data.Data); err != nil {
				// Check if zfs recv failed (which would cause the pipe to close)
				if zfsErr := checkRecvError(currentRecv); zfsErr != nil {
					receiveErr = status.Errorf(codes.Internal, "zfs recv failed: %v", zfsErr)
					return receiveErr
				}
				receiveErr = status.Errorf(codes.Internal, "failed to write to zfs recv: %v", err)
				return receiveErr
			}
			// NOTE: We intentionally don't send periodic Acks here. The proto defines
			// ReceiveVMAck for flow control, but clients (exelet-ctl, exed) don't read
			// from the response stream while uploading data. Sending Acks without readers
			// could fill the gRPC send buffer and cause deadlocks. If flow control is
			// needed in the future, clients must be updated to drain Acks concurrently.

		case *api.ReceiveVMRequest_Complete:
			expectedChecksum = v.Complete.Checksum
		}
	}

	// Validate that we received disk data - without this we'd create a broken instance
	if instanceRecv == nil {
		receiveErr = status.Error(codes.InvalidArgument,
			"no instance disk data received")
		return receiveErr
	}

	// Validate that we received a Complete message with checksum
	if expectedChecksum == "" {
		receiveErr = status.Error(codes.InvalidArgument,
			"no Complete message received with checksum")
		return receiveErr
	}

	// Wait for instance zfs recv to complete
	if err := waitRecvComplete(instanceRecv); err != nil {
		rb.zfsDatasetCreated = true // Attempt cleanup even on partial failure
		receiveErr = status.Errorf(codes.Internal, "zfs recv failed: %v", err)
		return receiveErr
	}
	rb.zfsDatasetCreated = true

	// Verify checksum
	actualChecksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if actualChecksum != expectedChecksum {
		receiveErr = status.Errorf(codes.DataLoss,
			"checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
		return receiveErr
	}

	// Create instance directory
	instanceDir := s.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to create instance dir: %v", err)
		return receiveErr
	}
	rb.instanceDirCreated = true

	// Copy kernel to new instance dir
	if err := s.setupKernelForMigration(ctx, startReq.SourceInstance.VMConfig, instanceDir); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to setup kernel: %v", err)
		return receiveErr
	}

	// Create new instance config
	// Note: SSHPort is NOT preserved - the target exelet will allocate a new port
	// when the VM is started, since the source port may conflict on the target.
	// exed must be updated with the new ctrhost and ssh_port after migration.
	sourceInstance := startReq.SourceInstance
	newInstance := &api.Instance{
		ID:        instanceID,
		Name:      sourceInstance.Name,
		Image:     sourceInstance.Image,
		VMConfig:  s.adaptVMConfigForTarget(sourceInstance.VMConfig, instanceID, instanceDir),
		CreatedAt: sourceInstance.CreatedAt,
		UpdatedAt: time.Now().UnixNano(),
		State:     api.VMState_STOPPED,
		GroupID:   startReq.GroupID,
	}

	// Save instance config
	if err := s.saveInstanceConfig(newInstance); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to save instance config: %v", err)
		return receiveErr
	}

	// Send result
	if err := stream.Send(&api.ReceiveVMResponse{
		Type: &api.ReceiveVMResponse_Result{
			Result: &api.ReceiveVMResult{Instance: newInstance},
		},
	}); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to send result: %v", err)
		return receiveErr
	}

	s.log.InfoContext(ctx, "ReceiveVM completed",
		"instance", instanceID,
		"total_bytes", totalBytes,
		"checksum", actualChecksum)

	return nil
}

// adaptVMConfigForTarget adapts a VMConfig for the target exelet.
// It updates paths and clears network config (will be assigned on start).
func (s *Service) adaptVMConfigForTarget(src *api.VMConfig, newInstanceID, instanceDir string) *api.VMConfig {
	return &api.VMConfig{
		ID:         newInstanceID,
		Name:       src.Name,
		CPUs:       src.CPUs,
		Memory:     src.Memory,
		Disk:       src.Disk,
		KernelPath: filepath.Join(instanceDir, kernelName),
		Args:       src.Args,
		Shares:     src.Shares,
		// Network interface will be assigned on StartInstance
		NetworkInterface: nil,
	}
}

// setupKernelForMigration ensures the kernel is available for the migrated instance.
// It uses the embedded kernel from the exelet binary.
func (s *Service) setupKernelForMigration(_ context.Context, _ *api.VMConfig, instanceDir string) error {
	kernelPath := filepath.Join(instanceDir, kernelName)

	// Use the embedded kernel from the exelet
	kernel, err := exeletfs.Kernel()
	if err != nil {
		return fmt.Errorf("failed to load embedded kernel: %w", err)
	}

	// Create kernel file
	kernelFile, err := os.Create(kernelPath)
	if err != nil {
		return fmt.Errorf("failed to create kernel file: %w", err)
	}

	if _, err := io.Copy(kernelFile, kernel); err != nil {
		kernelFile.Close()
		return fmt.Errorf("failed to write kernel: %w", err)
	}

	if err := kernelFile.Sync(); err != nil {
		kernelFile.Close()
		return fmt.Errorf("failed to sync kernel file: %w", err)
	}

	if err := kernelFile.Close(); err != nil {
		return fmt.Errorf("failed to close kernel file: %w", err)
	}

	return nil
}

// receiveVMRollback handles cleanup on ReceiveVM failure.
type receiveVMRollback struct {
	ctx context.Context
	log interface {
		WarnContext(ctx context.Context, msg string, args ...any)
	}
	storageManager interface {
		Delete(ctx context.Context, id string) error
	}
	instanceID           string
	instanceDir          string
	baseImageID          string
	encryptionKeyCreated bool
	baseImageCreated     bool
	zfsDatasetCreated    bool
	instanceDirCreated   bool
}

func (r *receiveVMRollback) Rollback() {
	r.log.WarnContext(r.ctx, "rolling back receive VM", "instance", r.instanceID)

	// Use a background context to ensure cleanup completes
	ctx := context.WithoutCancel(r.ctx)

	// Delete any partially created ZFS dataset for the instance
	if r.zfsDatasetCreated {
		if err := r.storageManager.Delete(ctx, r.instanceID); err != nil {
			r.log.WarnContext(ctx, "failed to delete ZFS dataset during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Note: We intentionally do NOT delete the base image during rollback.
	// Base images are shared resources that may be used by other VMs and are
	// expensive to re-transfer. Leaving them around doesn't cause problems.

	// Remove instance directory
	if r.instanceDirCreated {
		if err := os.RemoveAll(r.instanceDir); err != nil {
			r.log.WarnContext(ctx, "failed to remove instance dir during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Note: encryption key is stored in storage volumes dir, which is cleaned up with the dataset
}
