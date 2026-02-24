package compute

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/config"
	exeletfs "exe.dev/exelet/fs"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// CloneInstance clones an existing VM using ZFS copy-on-write snapshots.
// This creates a new VM with a copy of the source VM's disk, updating identity files
// (hostname, hosts, machine-id, SSH host keys) to ensure the clone is distinct.
func (s *Service) CloneInstance(req *api.CloneInstanceRequest, stream api.ComputeService_CloneInstanceServer) error {
	ctx := stream.Context()
	logging.AddFields(ctx, logging.Fields{"container_id", req.NewInstanceID, "vm_name", req.NewName})
	s.log.DebugContext(ctx, "cloning instance", "source_id", req.SourceInstanceID, "new_id", req.NewInstanceID, "new_name", req.NewName)

	// Validate request
	if req.SourceInstanceID == "" {
		return status.Error(codes.InvalidArgument, "source_instance_id is required")
	}
	if req.NewInstanceID == "" {
		return status.Error(codes.InvalidArgument, "new_instance_id is required")
	}
	if req.NewName == "" {
		return status.Error(codes.InvalidArgument, "new_name is required")
	}

	// Get source instance
	sourceInstance, err := s.getInstance(ctx, req.SourceInstanceID)
	if err != nil {
		if errors.Is(err, api.ErrNotFound) {
			return status.Errorf(codes.NotFound, "source instance %q not found", req.SourceInstanceID)
		}
		return status.Errorf(codes.Internal, "failed to get source instance: %v", err)
	}

	// Validate source instance has required config
	if sourceInstance.VMConfig == nil {
		return status.Errorf(codes.FailedPrecondition, "source instance %q has no VM configuration", req.SourceInstanceID)
	}

	// Check if new instance already exists
	if _, err := s.getInstance(ctx, req.NewInstanceID); err == nil {
		return status.Errorf(codes.AlreadyExists, "instance %q already exists", req.NewInstanceID)
	} else if !errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.Internal, "error checking new instance existence: %v", err)
	}

	// Perform the clone
	instance, err := s.cloneInstance(ctx, req, sourceInstance, stream)
	if err != nil {
		return err
	}

	// Send final instance
	if err := stream.Send(&api.CloneInstanceResponse{
		Type: &api.CloneInstanceResponse_Instance{
			Instance: instance,
		},
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	return nil
}

// cloneInstance performs the actual clone operation
func (s *Service) cloneInstance(ctx context.Context, req *api.CloneInstanceRequest, sourceInstance *api.Instance, stream api.ComputeService_CloneInstanceServer) (instance *api.Instance, err error) {
	newInstanceID := req.NewInstanceID

	// Setup rollback to cleanup resources on error
	rb := &createInstanceRollback{
		ctx:             context.WithoutCancel(ctx),
		log:             s.log,
		serviceContext:  s.context,
		instanceID:      newInstanceID,
		proxyManager:    s.proxyManager,
		portAllocator:   s.portAllocator,
		runtimeAddress:  s.config.RuntimeAddress,
		enableHugepages: s.config.EnableHugepages,
	}
	defer func() {
		if err != nil {
			s.log.WarnContext(ctx, "clone failed, rolling back", "id", newInstanceID, "error", err)
			err = rb.EnhanceErrorWithBootLog(err)
			rb.Rollback()
		}
	}()

	// Init status
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:    newInstanceID,
		State: api.CloneInstanceStatus_INIT,
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Create instance directory
	instanceDir := s.getInstanceDir(newInstanceID)
	s.log.DebugContext(ctx, "creating clone instance dir", "id", newInstanceID, "path", instanceDir)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.instanceDir = instanceDir
	rb.instanceDirCreated = true

	// Persist instance config early with CREATING state
	created := time.Now().UnixNano()
	earlyInstance := &api.Instance{
		ID:        newInstanceID,
		Name:      req.NewName,
		Image:     sourceInstance.Image,
		State:     api.VMState_CREATING,
		Node:      s.config.Name,
		CreatedAt: created,
		UpdatedAt: created,
	}
	if err := s.saveInstanceConfig(earlyInstance); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Network setup
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:      newInstanceID,
		State:   api.CloneInstanceStatus_NETWORK,
		Message: "configuring networking",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.log.DebugContext(ctx, "creating network interface for clone", "id", newInstanceID)
	networkInterface, err := s.context.NetworkManager.CreateInterface(ctx, newInstanceID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.networkCreated = true
	if networkInterface.IP != nil {
		rb.networkIP = networkInterface.IP.IPV4
	}

	// Ensure gateway IP
	gatewayIP := ""
	if ip := networkInterface.IP; ip != nil {
		gatewayIP = ip.GatewayV4
	}
	if gatewayIP == "" {
		return nil, status.Error(codes.Internal, "unable to get gateway IP for network interface")
	}

	// ZFS snapshot and clone
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:      newInstanceID,
		State:   api.CloneInstanceStatus_SNAPSHOT,
		Message: "creating snapshot",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Clone the source volume to the new volume using ZFS COW clone
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:      newInstanceID,
		State:   api.CloneInstanceStatus_CLONE,
		Message: "cloning disk",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.log.DebugContext(ctx, "cloning ZFS volume", "source", req.SourceInstanceID, "dest", newInstanceID)
	if err := s.context.StorageManager.Clone(ctx, req.SourceInstanceID, newInstanceID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to clone disk: %v", err)
	}
	rb.instanceCloned = true

	// Expand disk if requested (must happen before mount/boot)
	if req.Disk != nil && *req.Disk > sourceInstance.VMConfig.Disk {
		s.log.DebugContext(ctx, "expanding cloned disk", "id", newInstanceID, "from", sourceInstance.VMConfig.Disk, "to", *req.Disk)
		// resizeFilesystem=false because VM hasn't booted yet and fstab has x-systemd.growfs
		if err := s.context.StorageManager.Expand(ctx, newInstanceID, *req.Disk, false); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to expand disk: %v", err)
		}
	}

	// Mount the cloned volume
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:      newInstanceID,
		State:   api.CloneInstanceStatus_CONFIG,
		Message: "configuring clone",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	mountConfig, err := s.context.StorageManager.Mount(ctx, newInstanceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error mounting cloned filesystem: %v", err)
	}
	rb.instanceMounted = true
	mountpoint := mountConfig.Path

	// Update identity files to make the clone distinct

	// 1. Update /etc/hostname
	s.log.DebugContext(ctx, "updating hostname in clone", "id", newInstanceID)
	hostnamePath := filepath.Join(mountpoint, config.HostnamePath)
	if err := os.WriteFile(hostnamePath, []byte(req.NewName), 0o644); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update hostname: %v", err)
	}

	// 2. Update /etc/hosts
	s.log.DebugContext(ctx, "updating hosts in clone", "id", newInstanceID)
	hostsPath := filepath.Join(mountpoint, config.HostsPath)
	ip := "127.0.0.1"
	if v := networkInterface.IP; v != nil {
		nIP, _, err := net.ParseCIDR(v.IPV4)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		ip = nIP.String()
	}
	hostsContents := fmt.Sprintf(`# managed by exe.dev
127.0.0.1 localhost
%s %s.%s %s
`, ip, req.NewName, s.config.InstanceDomain, req.NewName)
	if err := os.WriteFile(hostsPath, []byte(hostsContents), 0o644); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update hosts: %v", err)
	}

	// 2.5. Ensure /etc/fstab has x-systemd.growfs for auto-resize on boot
	s.log.DebugContext(ctx, "ensuring fstab in clone", "id", newInstanceID)
	fstabPath := filepath.Join(mountpoint, "etc", "fstab")
	fstabContents := `/dev/vda / ext4 defaults,x-systemd.growfs 0 1
`
	if err := os.WriteFile(fstabPath, []byte(fstabContents), 0o644); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to write fstab: %v", err)
	}

	// 3. Generate new /etc/machine-id
	machineIDPath := filepath.Join(mountpoint, "etc", "machine-id")
	s.log.DebugContext(ctx, "generating new machine-id for clone", "id", newInstanceID, "path", machineIDPath)
	machineIDBytes := make([]byte, 16)
	if _, err := rand.Read(machineIDBytes); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate machine-id: %v", err)
	}
	newMachineID := hex.EncodeToString(machineIDBytes) + "\n"
	if err := os.WriteFile(machineIDPath, []byte(newMachineID), 0o444); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to write machine-id: %v", err)
	}

	// 4. Delete SSH host keys (will be regenerated on first boot by exe-init)
	// SSH keys are stored at /exe.dev/etc/ssh/, not /etc/ssh/
	sshKeyPath := filepath.Join(mountpoint, config.InstanceSSHHostKeyPath[1:]) // strip leading /
	s.log.DebugContext(ctx, "removing SSH host key from clone", "id", newInstanceID, "path", sshKeyPath)
	if err := os.Remove(sshKeyPath); err != nil && !os.IsNotExist(err) {
		s.log.WarnContext(ctx, "failed to remove SSH host key", "path", sshKeyPath, "error", err)
	}
	if err := os.Remove(sshKeyPath + ".pub"); err != nil && !os.IsNotExist(err) {
		s.log.WarnContext(ctx, "failed to remove SSH host key pub", "path", sshKeyPath+".pub", "error", err)
	}

	// Write any additional configs (like SSH authorized keys)
	for _, cfg := range req.Configs {
		targetPath := filepath.Join(mountpoint, filepath.Clean(cfg.Destination))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create config directory: %v", err)
		}
		switch v := cfg.Source.(type) {
		case *api.Config_File:
			mode := os.FileMode(int(cfg.Mode))
			if err := os.WriteFile(targetPath, v.File.Data, mode); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to write config file: %v", err)
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "config type %T not supported", v)
		}
	}

	// Copy kernel to new instance directory
	s.log.DebugContext(ctx, "copying kernel to clone", "id", newInstanceID)
	kernel, err := exeletfs.Kernel()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	kernelFilePath := filepath.Join(instanceDir, kernelName)
	kernelFile, err := os.Create(kernelFilePath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if _, err := io.Copy(kernelFile, kernel); err != nil {
		kernelFile.Close()
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := kernelFile.Sync(); err != nil {
		kernelFile.Close()
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := kernelFile.Close(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Unmount
	s.log.DebugContext(ctx, "unmounting clone storage", "id", newInstanceID)
	if err := s.context.StorageManager.Unmount(ctx, newInstanceID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.instanceMounted = false

	// Get instance filesystem info
	instanceFS, err := s.context.StorageManager.Get(ctx, newInstanceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error getting cloned instance fs: %v", err)
	}

	// Boot the clone
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:      newInstanceID,
		State:   api.CloneInstanceStatus_BOOT,
		Message: "booting clone",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Create VM configuration (copy from source, update for new instance)
	// Apply resource overrides if specified
	cpus := sourceInstance.VMConfig.CPUs
	if req.CPUs != nil && *req.CPUs > 0 {
		cpus = *req.CPUs
	}
	memory := sourceInstance.VMConfig.Memory
	if req.Memory != nil && *req.Memory > 0 {
		memory = *req.Memory
	}
	disk := sourceInstance.VMConfig.Disk
	if req.Disk != nil && *req.Disk > 0 {
		// Disk can only grow, not shrink
		if *req.Disk < sourceInstance.VMConfig.Disk {
			return nil, status.Errorf(codes.InvalidArgument, "disk size cannot be smaller than source (%d bytes)", sourceInstance.VMConfig.Disk)
		}

		disk = *req.Disk
	}

	bootArgs := getBootArgs()
	vmCfg := &api.VMConfig{
		ID:               newInstanceID,
		Name:             req.NewName,
		CPUs:             cpus,
		Memory:           memory,
		Disk:             disk,
		RootDiskPath:     instanceFS.Path,
		KernelPath:       kernelFilePath,
		Args:             bootArgs,
		NetworkInterface: networkInterface,
	}

	if err := vmCfg.Validate(); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	s.log.DebugContext(ctx, "creating VMM for clone", "config", vmCfg)
	v, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if err := v.Create(ctx, vmCfg); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.vmCreated = true

	// Start VM
	if err := v.Start(ctx, vmCfg.ID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.vmStarted = true

	// Allocate SSH proxy port
	sshPort, err := s.portAllocator.Allocate()
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "failed to allocate SSH port: %v", err)
	}
	rb.allocatedPort = sshPort

	// Parse VM IP
	vmIP := ""
	if networkInterface.IP != nil && networkInterface.IP.IPV4 != "" {
		ipAddr, _, err := net.ParseCIDR(networkInterface.IP.IPV4)
		if err != nil {
			s.portAllocator.Release(sshPort)
			return nil, status.Errorf(codes.Internal, "failed to parse VM IP: %v", err)
		}
		vmIP = ipAddr.String()
	} else {
		s.portAllocator.Release(sshPort)
		return nil, status.Error(codes.Internal, "no IP address assigned to VM")
	}

	// Create SSH proxy
	s.log.DebugContext(ctx, "starting SSH proxy for clone", "instance", newInstanceID, "port", sshPort, "target", fmt.Sprintf("%s:22", vmIP))
	if err := s.proxyManager.CreateProxy(newInstanceID, vmIP, sshPort, instanceDir); err != nil {
		s.portAllocator.Release(sshPort)
		return nil, status.Errorf(codes.Internal, "failed to start SSH proxy: %v", err)
	}
	rb.proxyCreated = true

	// Copy exposed ports from source
	exposedPorts := sourceInstance.ExposedPorts

	// Create final instance
	i := &api.Instance{
		ID:           newInstanceID,
		Name:         req.NewName,
		Image:        sourceInstance.Image,
		VMConfig:     vmCfg,
		Node:         s.config.Name,
		CreatedAt:    created,
		UpdatedAt:    time.Now().UnixNano(),
		State:        api.VMState_STARTING,
		SSHPort:      int32(sshPort),
		ExposedPorts: exposedPorts,
		GroupID:      req.GroupID,
	}

	if err := s.saveInstanceConfig(i); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Complete
	if err := s.updateCloneStatus(stream, &api.CloneInstanceStatus{
		ID:    newInstanceID,
		State: api.CloneInstanceStatus_COMPLETE,
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return i, nil
}

func (s *Service) updateCloneStatus(stream api.ComputeService_CloneInstanceServer, status *api.CloneInstanceStatus) error {
	if err := stream.Send(&api.CloneInstanceResponse{
		Type: &api.CloneInstanceResponse_Status{
			Status: status,
		},
	}); err != nil {
		return err
	}
	return nil
}
