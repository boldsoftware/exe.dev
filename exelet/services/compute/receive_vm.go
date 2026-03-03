package compute

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	exeletfs "exe.dev/exelet/fs"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// ReceiveVM receives a VM from another exelet.
func (s *Service) ReceiveVM(stream api.ComputeService_ReceiveVMServer) error {
	err := s.receiveVM(stream)
	// Context cancellation during migration is expected:
	// the caller disconnects or the migration completes.
	// Return the context error directly.
	if err != nil && stream.Context().Err() != nil {
		return status.FromContextError(stream.Context().Err()).Err()
	}
	return err
}

func (s *Service) receiveVM(stream api.ComputeService_ReceiveVMServer) error {
	ctx := stream.Context()

	zstdDec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(2*4*1024*1024))
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create zstd decoder: %v", err)
	}
	defer zstdDec.Close()

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

	// Suspend replication for this volume to prevent "dataset is busy" errors
	// during zfs recv. The replication worker must not snapshot the dataset
	// while we are receiving data into it.
	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		defer rs.ResumeVolume(instanceID)
		s.log.InfoContext(ctx, "waiting on VM storage replication", "instance", instanceID)
		rs.WaitVolumeIdle(ctx, instanceID)
	}

	// Pre-flight: for live migration, verify the target host has enough memory
	// before we begin. Failing early avoids the messy rollback path (IP reconfig
	// has already happened, source VM is paused, needs cold restart).
	if startReq.Live && startReq.SourceInstance != nil && startReq.SourceInstance.VMConfig != nil {
		requiredBytes := startReq.SourceInstance.VMConfig.Memory
		if err := checkAvailableMemory(requiredBytes); err != nil {
			// Not enough free memory. Try to reclaim by pushing idle VMs to swap.
			mr := s.context.MemoryReclaimer
			if mr == nil {
				return status.Errorf(codes.ResourceExhausted,
					"target host does not have enough memory for live restore: %v", err)
			}

			s.log.InfoContext(ctx, "insufficient memory for live migration, attempting reclaim",
				"instance", instanceID,
				"error", err)

			// Retry up to 3 times. The kernel continues reclaiming in the
			// background after memory.reclaim writes, so subsequent attempts
			// often succeed when the first falls just short.
			const maxAttempts = 3
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				if reclaimErr := mr.ReclaimMemory(ctx, requiredBytes); reclaimErr != nil {
					s.log.WarnContext(ctx, "memory reclaim did not free enough",
						"instance", instanceID,
						"attempt", attempt,
						"error", reclaimErr)
				}
				if err := checkAvailableMemory(requiredBytes); err == nil {
					s.log.InfoContext(ctx, "memory reclaim succeeded",
						"instance", instanceID,
						"attempt", attempt)
					break
				} else if attempt == maxAttempts {
					return status.Errorf(codes.ResourceExhausted,
						"target host does not have enough memory for live restore (even after reclaim): %v", err)
				} else {
					s.log.InfoContext(ctx, "memory still insufficient after reclaim, retrying",
						"instance", instanceID,
						"attempt", attempt,
						"error", err)
				}
			}
		}
	}

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

	// Setup rollback early so all resource allocations below are covered.
	rb := &receiveVMRollback{
		ctx:            ctx,
		log:            s.log,
		storageManager: s.context.StorageManager,
		networkManager: s.context.NetworkManager,
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

	// For live migration, allocate network interface early so the orchestrator
	// can reconfigure the VM's IP before we pause the source.
	var targetNetwork *api.NetworkInterface
	if startReq.Live {
		var err error
		targetNetwork, err = s.context.NetworkManager.CreateInterface(ctx, instanceID)
		if err != nil {
			receiveErr = status.Errorf(codes.Internal, "failed to allocate network interface: %v", err)
			return receiveErr
		}
		rb.targetNetwork = targetNetwork
		s.log.InfoContext(ctx, "live: allocated target network", "instance", instanceID, "ip", targetNetwork.IP.IPV4)
	}

	// Send ready response
	if err := stream.Send(&api.ReceiveVMResponse{
		Type: &api.ReceiveVMResponse_Ready{
			Ready: &api.ReceiveVMReady{
				HasBaseImage:  hasBaseImage,
				TargetNetwork: targetNetwork,
			},
		},
	}); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to send ready: %v", err)
		return receiveErr
	}

	s.log.DebugContext(ctx, "sent ready response", "instance", instanceID, "has_base_image", hasBaseImage, "live", startReq.Live)

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

	// Pre-create instance directory with correct permissions so that snapshot chunk
	// handling (which creates a subdirectory) doesn't implicitly create it with 0700.
	instanceDir := s.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to create instance dir: %v", err)
		return receiveErr
	}
	rb.instanceDirCreated = true

	// Use a unique snapshot directory per migration attempt to avoid conflicts
	// if two sources migrate the same instance concurrently or a retry overlaps.
	var snapshotSuffix [4]byte
	if _, err := rand.Read(snapshotSuffix[:]); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to generate snapshot dir suffix: %v", err)
		return receiveErr
	}
	snapshotDir := filepath.Join(instanceDir, "snapshot-"+hex.EncodeToString(snapshotSuffix[:]))

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

		case *api.ReceiveVMRequest_PhaseComplete:
			// Phase complete: close the current zfs recv.
			// The next Data chunk will lazily start a new receiver if needed
			// (e.g., phase 2 incremental). For live migration, the final phase is
			// followed by snapshot chunks, not ZFS data, so no new receiver is needed.
			if instanceRecv == nil {
				receiveErr = status.Error(codes.FailedPrecondition,
					"received PhaseComplete before any instance data")
				return receiveErr
			}
			s.log.InfoContext(ctx, "completing phase zfs receive", "instance", instanceID)
			if err := waitRecvComplete(instanceRecv); err != nil {
				rb.zfsDatasetCreated = true
				receiveErr = status.Errorf(codes.Internal, "zfs recv failed: %v", err)
				return receiveErr
			}
			rb.zfsDatasetCreated = true
			instanceRecv = nil
			currentRecv = nil

		case *api.ReceiveVMRequest_SnapshotData:
			// Live migration: receive CH snapshot file chunks
			if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
				receiveErr = status.Errorf(codes.Internal, "failed to create snapshot dir: %v", err)
				return receiveErr
			}
			rb.snapshotDirCreated = true

			data := v.SnapshotData.Data
			if v.SnapshotData.Compressed {
				var err error
				data, err = zstdDec.DecodeAll(data, nil)
				if err != nil {
					receiveErr = status.Errorf(codes.Internal, "failed to decompress snapshot chunk %s: %v", v.SnapshotData.Filename, err)
					return receiveErr
				}
				if len(data) > sendVMChunkSize {
					receiveErr = status.Errorf(codes.InvalidArgument,
						"decompressed snapshot chunk %s too large: %d bytes (max %d)",
						v.SnapshotData.Filename, len(data), sendVMChunkSize)
					return receiveErr
				}
			}

			filePath := filepath.Join(snapshotDir, v.SnapshotData.Filename)
			f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				receiveErr = status.Errorf(codes.Internal, "failed to open snapshot file %s: %v", v.SnapshotData.Filename, err)
				return receiveErr
			}
			if _, err := f.Write(data); err != nil {
				f.Close()
				receiveErr = status.Errorf(codes.Internal, "failed to write snapshot file %s: %v", v.SnapshotData.Filename, err)
				return receiveErr
			}
			f.Close()

		case *api.ReceiveVMRequest_Complete:
			expectedChecksum = v.Complete.Checksum
		}
	}

	// Validate that we received disk data - without this we'd create a broken instance
	if !rb.zfsDatasetCreated && instanceRecv == nil {
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

	// Wait for instance zfs recv to complete (may already be done if PhaseComplete closed it)
	if instanceRecv != nil {
		if err := waitRecvComplete(instanceRecv); err != nil {
			rb.zfsDatasetCreated = true // Attempt cleanup even on partial failure
			receiveErr = status.Errorf(codes.Internal, "zfs recv failed: %v", err)
			return receiveErr
		}
		rb.zfsDatasetCreated = true
	}

	// Verify checksum
	actualChecksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if actualChecksum != expectedChecksum {
		receiveErr = status.Errorf(codes.DataLoss,
			"checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
		return receiveErr
	}

	// Copy kernel to new instance dir
	if err := s.setupKernelForMigration(ctx, startReq.SourceInstance.VMConfig, instanceDir); err != nil {
		receiveErr = status.Errorf(codes.Internal, "failed to setup kernel: %v", err)
		return receiveErr
	}

	sourceInstance := startReq.SourceInstance

	// Live migration: edit snapshot config, restore from snapshot, resume VM
	if startReq.Live {
		newInstance, coldBooted, err := s.finalizeLiveReceive(ctx, instanceID, instanceDir, snapshotDir, sourceInstance, startReq.GroupID, targetNetwork, rb)
		if err != nil {
			receiveErr = err
			return receiveErr
		}

		if err := stream.Send(&api.ReceiveVMResponse{
			Type: &api.ReceiveVMResponse_Result{
				Result: &api.ReceiveVMResult{Instance: newInstance, ColdBooted: coldBooted},
			},
		}); err != nil {
			receiveErr = status.Errorf(codes.Internal, "failed to send result: %v", err)
			return receiveErr
		}

		s.log.InfoContext(ctx, "ReceiveVM (live) completed",
			"instance", instanceID,
			"total_bytes", totalBytes,
			"checksum", actualChecksum)
		return nil
	}

	// Cold/two-phase: save instance as STOPPED
	// Note: SSHPort is NOT preserved - the target exelet will allocate a new port
	// when the VM is started, since the source port may conflict on the target.
	// exed must be updated with the new ctrhost and ssh_port after migration.
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

// finalizeLiveReceive edits the CH snapshot config, restores the VM, and saves instance config as RUNNING.
func (s *Service) finalizeLiveReceive(ctx context.Context, instanceID, instanceDir, snapshotDir string, sourceInstance *api.Instance, groupID string, targetNetwork *api.NetworkInterface, rb *receiveVMRollback) (*api.Instance, bool, error) {
	// Load disk to get the target zvol path
	instanceFS, err := s.context.StorageManager.Load(ctx, instanceID)
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to load storage: %v", err)
	}

	kernelPath := filepath.Join(instanceDir, kernelName)

	// Edit CH snapshot config to fix disk path, kernel path, and boot args
	if err := editSnapshotConfig(snapshotDir, instanceFS.Path, kernelPath, sourceInstance.VMConfig, targetNetwork); err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to edit snapshot config: %v", err)
	}

	// Restore from snapshot (starts CH daemon, restores, resumes)
	vmmgr, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to create VMM: %v", err)
	}

	s.log.InfoContext(ctx, "live: restoring VM from snapshot", "instance", instanceID)
	restoreErr := vmmgr.RestoreFromSnapshot(ctx, instanceID, snapshotDir)

	// Build the instance config (needed for both restore and fallback paths)
	vmConfig := s.adaptVMConfigForTarget(sourceInstance.VMConfig, instanceID, instanceDir)
	vmConfig.NetworkInterface = targetNetwork
	vmConfig.RootDiskPath = instanceFS.Path

	// Register a stop function so rollback can stop the CH process if a later step fails.
	// Must be set before the restoreErr check so the cold-boot fallback path doesn't trigger it.
	if restoreErr == nil {
		rb.stopVM = func() {
			s.log.WarnContext(ctx, "live: stopping restored VM due to rollback", "instance", instanceID)
			if err := vmmgr.Stop(ctx, instanceID); err != nil {
				s.log.WarnContext(ctx, "live: failed to stop restored VM during rollback", "instance", instanceID, "error", err)
			}
		}
	}

	if restoreErr != nil {
		// CH snapshot restore failed (e.g., memory region issues on Apple Virtualization).
		// Fall back to cold boot: save as STOPPED, then start normally.
		// Process state is lost but disk data is intact from the ZFS transfer.
		s.log.WarnContext(ctx, "live: snapshot restore failed, falling back to cold boot",
			"instance", instanceID, "error", restoreErr)

		// Clean up the failed CH process and live migration network interface
		if stopErr := vmmgr.Stop(ctx, instanceID); stopErr != nil {
			s.log.WarnContext(ctx, "live: failed to stop failed CH process", "instance", instanceID, "error", stopErr)
		}
		os.RemoveAll(snapshotDir)

		// Delete the network interface allocated for live migration so
		// startInstance can create a fresh one.
		if targetNetwork != nil && targetNetwork.IP != nil {
			delIP := targetNetwork.IP.IPV4
			if idx := strings.Index(delIP, "/"); idx > 0 {
				delIP = delIP[:idx]
			}
			if err := s.context.NetworkManager.DeleteInterface(ctx, instanceID, delIP); err != nil {
				s.log.WarnContext(ctx, "live: failed to delete live migration network interface", "instance", instanceID, "error", err)
			}
		}

		newInstance := &api.Instance{
			ID:        instanceID,
			Name:      sourceInstance.Name,
			Image:     sourceInstance.Image,
			VMConfig:  vmConfig,
			CreatedAt: sourceInstance.CreatedAt,
			UpdatedAt: time.Now().UnixNano(),
			State:     api.VMState_STOPPED,
			GroupID:   groupID,
		}
		if err := s.saveInstanceConfig(newInstance); err != nil {
			return nil, false, status.Errorf(codes.Internal, "failed to save instance config after restore failure: %v", err)
		}

		// Cold boot the VM — startInstance handles migrated VMs (creates VMM config, allocates network)
		s.log.InfoContext(ctx, "live: cold-booting VM after restore failure", "instance", instanceID)
		if err := s.startInstance(ctx, instanceID); err != nil {
			return nil, false, status.Errorf(codes.Internal,
				"snapshot restore failed (%v) and cold boot also failed: %v", restoreErr, err)
		}

		// Reload the instance config (startInstance updated state, network, ssh port)
		coldInstance, err := s.loadInstanceConfig(instanceID)
		if err != nil {
			return nil, false, status.Errorf(codes.Internal, "failed to reload instance after cold boot: %v", err)
		}

		return coldInstance, true, nil
	}

	// Snapshot files are no longer needed after a successful restore.
	// Also clean up any stale snapshot dirs from previous migrations.
	entries, _ := filepath.Glob(filepath.Join(instanceDir, "snapshot-*"))
	for _, e := range entries {
		os.RemoveAll(e)
	}
	// Remove legacy "snapshot" dir from before unique naming was added.
	os.RemoveAll(filepath.Join(instanceDir, "snapshot"))

	// Set up SSH proxy
	sshPort, err := s.portAllocator.Allocate()
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to allocate SSH port: %v", err)
	}

	vmIP := ""
	if targetNetwork.IP != nil && targetNetwork.IP.IPV4 != "" {
		ipAddr, _, err := net.ParseCIDR(targetNetwork.IP.IPV4)
		if err != nil {
			return nil, false, status.Errorf(codes.Internal, "failed to parse VM IP: %v", err)
		}
		vmIP = ipAddr.String()
	} else {
		return nil, false, status.Errorf(codes.Internal, "no IP address in target network")
	}

	if err := s.proxyManager.CreateProxy(instanceID, vmIP, sshPort, instanceDir); err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to start SSH proxy: %v", err)
	}

	newInstance := &api.Instance{
		ID:        instanceID,
		Name:      sourceInstance.Name,
		Image:     sourceInstance.Image,
		VMConfig:  vmConfig,
		CreatedAt: sourceInstance.CreatedAt,
		UpdatedAt: time.Now().UnixNano(),
		State:     api.VMState_RUNNING,
		SSHPort:   int32(sshPort),
		GroupID:   groupID,
	}

	// Save instance and VMM configs
	if err := s.saveInstanceConfig(newInstance); err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to save instance config: %v", err)
	}

	// Save VMM config (so cold boot on target works correctly later)
	if err := vmmgr.Update(ctx, vmConfig); err != nil {
		s.log.WarnContext(ctx, "live: failed to save VMM config", "instance", instanceID, "error", err)
	}

	return newInstance, false, nil
}

// editSnapshotConfig modifies the CH snapshot's config.json to point to the target's
// disk path, kernel path, and updated boot args (with new IP).
func editSnapshotConfig(snapshotDir, diskPath, kernelPath string, srcVMConfig *api.VMConfig, targetNetwork *api.NetworkInterface) error {
	configPath := filepath.Join(snapshotDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read snapshot config: %w", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse snapshot config: %w", err)
	}

	// Update disk path: disks[0].path
	disks, ok := config["disks"].([]any)
	if !ok || len(disks) == 0 {
		return fmt.Errorf("snapshot config missing disks array")
	}
	disk, ok := disks[0].(map[string]any)
	if !ok {
		return fmt.Errorf("snapshot config disks[0] is not an object")
	}
	disk["path"] = diskPath

	// Update kernel path and cmdline
	payload, ok := config["payload"].(map[string]any)
	if !ok {
		return fmt.Errorf("snapshot config missing payload object")
	}
	payload["kernel"] = kernelPath

	// Update cmdline: replace ip= boot arg with target IP
	if cmdline, ok := payload["cmdline"].(string); ok {
		payload["cmdline"] = replaceIPBootArg(cmdline, srcVMConfig.Name, targetNetwork)
	}

	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot config: %w", err)
	}

	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		return fmt.Errorf("failed to write snapshot config: %w", err)
	}

	return nil
}

// replaceIPBootArg replaces the ip= kernel boot argument with one derived from the target network.
func replaceIPBootArg(cmdline, hostname string, targetNetwork *api.NetworkInterface) string {
	// Build new ip= arg from target network
	newIPArg := buildIPBootArg(hostname, targetNetwork)

	// Replace existing ip= arg
	var parts []string
	replaced := false
	for _, part := range strings.Fields(cmdline) {
		if strings.HasPrefix(part, "ip=") {
			if newIPArg != "" {
				parts = append(parts, newIPArg)
				replaced = true
			}
		} else {
			parts = append(parts, part)
		}
	}
	if !replaced && newIPArg != "" {
		parts = append(parts, newIPArg)
	}
	return strings.Join(parts, " ")
}

// buildIPBootArg generates the ip= kernel boot argument from a network interface.
// Format: ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0-ip>:<dns1-ip>:<ntp0-ip>
func buildIPBootArg(hostname string, iface *api.NetworkInterface) string {
	if iface == nil || iface.IP == nil || iface.IP.IPV4 == "" {
		return ""
	}

	ipSubnet := iface.IP.IPV4
	gw := iface.IP.GatewayV4
	iIP, ipnet, err := net.ParseCIDR(ipSubnet)
	if err != nil {
		return ""
	}
	netmask := net.IP(ipnet.Mask).String()
	ip := iIP.String()

	device := iface.DeviceName
	primaryNS := "1.1.1.1"
	backupNS := "8.8.8.8"
	switch len(iface.Nameservers) {
	case 0:
	case 1:
		primaryNS = iface.Nameservers[0]
	default:
		primaryNS = iface.Nameservers[0]
		backupNS = iface.Nameservers[1]
	}
	ntpServer := iface.NTPServer

	return fmt.Sprintf("ip=%s:%s:%s:%s:%s:%s:%s:%s:%s:%s",
		ip, gw, gw, netmask, hostname, device, "none", primaryNS, backupNS, ntpServer)
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
	networkManager interface {
		DeleteInterface(ctx context.Context, id, ip string) error
	}
	instanceID           string
	instanceDir          string
	baseImageID          string
	targetNetwork        *api.NetworkInterface
	stopVM               func() // set after successful live restore to stop CH process on rollback
	encryptionKeyCreated bool
	baseImageCreated     bool
	zfsDatasetCreated    bool
	instanceDirCreated   bool
	snapshotDirCreated   bool
}

func (r *receiveVMRollback) Rollback() {
	r.log.WarnContext(r.ctx, "rolling back receive VM", "instance", r.instanceID)

	// Use a background context to ensure cleanup completes
	ctx := context.WithoutCancel(r.ctx)

	// Stop the VM if live restore succeeded but a later step failed
	if r.stopVM != nil {
		r.stopVM()
	}

	// Delete any partially created ZFS dataset for the instance
	if r.zfsDatasetCreated {
		if err := r.storageManager.Delete(ctx, r.instanceID); err != nil {
			r.log.WarnContext(ctx, "failed to delete ZFS dataset during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Note: We intentionally do NOT delete the base image during rollback.
	// Base images are shared resources that may be used by other VMs and are
	// expensive to re-transfer. Leaving them around doesn't cause problems.

	// Remove instance directory (includes snapshot dir)
	if r.instanceDirCreated || r.snapshotDirCreated {
		if err := os.RemoveAll(r.instanceDir); err != nil {
			r.log.WarnContext(ctx, "failed to remove instance dir during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Clean up network interface allocated for live migration
	if r.targetNetwork != nil {
		ip := ""
		if r.targetNetwork.IP != nil {
			ip = r.targetNetwork.IP.IPV4
			if idx := strings.Index(ip, "/"); idx > 0 {
				ip = ip[:idx]
			}
		}
		if err := r.networkManager.DeleteInterface(ctx, r.instanceID, ip); err != nil {
			r.log.WarnContext(ctx, "failed to delete network interface during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Note: encryption key is stored in storage volumes dir, which is cleaned up with the dataset
}

// checkAvailableMemory reads /proc/meminfo and returns an error if the host
// does not have enough available memory for a VM of the given size (bytes).
func checkAvailableMemory(requiredBytes uint64) error {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil // best-effort; skip check if we can't read meminfo
	}

	var availableKB uint64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availableKB)
			break
		}
	}

	if availableKB == 0 {
		return nil // field not found, skip check
	}

	availableBytes := availableKB * 1024
	if availableBytes < requiredBytes {
		return fmt.Errorf("need %d MB but only %d MB available",
			requiredBytes/(1024*1024), availableBytes/(1024*1024))
	}
	return nil
}
