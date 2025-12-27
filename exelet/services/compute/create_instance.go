package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/config"
	exeletfs "exe.dev/exelet/fs"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

const (
	kernelName = "kernel"
)

// isValidShellIdentifier checks if a string is a valid shell variable name
// Valid identifiers: start with letter or underscore, followed by letters, digits, or underscores
func isValidShellIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}
	// First character must be letter or underscore
	first := s[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return false
	}
	// Remaining characters must be alphanumeric or underscore
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// shellEscapeValue escapes a value for safe use in a shell script
// Uses single quotes and escapes any single quotes in the value
func shellEscapeValue(s string) string {
	// Replace each single quote with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// CreateInstance creates a new exelet instance
func (s *Service) CreateInstance(req *api.CreateInstanceRequest, stream api.ComputeService_CreateInstanceServer) (err error) {
	// validate
	if err := req.Validate(); err != nil {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	ctx := stream.Context()
	s.log.DebugContext(ctx, "creating instance", "request", req)
	if req.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "error generating instance ID: %s", err)
		}
		req.ID = id.String()
	}
	instanceID := req.ID

	// Check if instance already exists (but allow CREATING state to be resumed via singleflight)
	if existingInstance, err := s.getInstance(ctx, instanceID); err == nil {
		// Instance exists - but if it's in CREATING state, allow singleflight to handle it
		// (this handles the case where exelet crashed during creation)
		if existingInstance.State != api.VMState_CREATING {
			return status.Errorf(codes.AlreadyExists, "instance %s already exists", instanceID)
		}
		// CREATING state: fall through to singleflight which will either:
		// - Join an in-flight creation, or
		// - Start a fresh creation (cleaning up the stale state)
	} else if !errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.Internal, "error checking instance existence: %s", err)
	}

	// Use singleflight to ensure only one creation per instance ID
	instance, err, shared := s.instanceCreateGroup.Do(instanceID, func() (*api.Instance, error) {
		return s.createInstance(ctx, req, stream)
	})
	if err != nil {
		return err
	}

	// If this was a shared result (deduplicated request), send the final status and instance
	if shared {
		if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
			ID:    instanceID,
			State: api.CreateInstanceStatus_COMPLETE,
		}); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := stream.Send(&api.CreateInstanceResponse{
			Type: &api.CreateInstanceResponse_Instance{
				Instance: instance,
			},
		}); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	return nil
}

// createInstance performs the actual instance creation logic
func (s *Service) createInstance(ctx context.Context, req *api.CreateInstanceRequest, stream api.ComputeService_CreateInstanceServer) (instance *api.Instance, err error) {
	instanceID := req.ID

	// Re-check existence inside singleflight (in case concurrent request created it before we entered)
	if existingInstance, err := s.getInstance(ctx, instanceID); err == nil {
		if existingInstance.State == api.VMState_CREATING {
			// Stale CREATING state from a previous crashed creation - clean up and start fresh
			s.log.WarnContext(ctx, "found stale CREATING instance, cleaning up", "id", instanceID)
			instanceDir := s.getInstanceDir(instanceID)
			if rmErr := os.RemoveAll(instanceDir); rmErr != nil {
				s.log.ErrorContext(ctx, "failed to clean up stale instance directory", "id", instanceID, "error", rmErr)
			}
			// Extract IP from stale instance for proper cleanup (iptables rule + DHCP lease)
			staleIP := ""
			if existingInstance.VMConfig != nil && existingInstance.VMConfig.NetworkInterface != nil && existingInstance.VMConfig.NetworkInterface.IP != nil {
				if ipAddr, _, err := net.ParseCIDR(existingInstance.VMConfig.NetworkInterface.IP.IPV4); err == nil {
					staleIP = ipAddr.String()
				}
			}
			// Also clean up network interface if it exists
			if delErr := s.context.NetworkManager.DeleteInterface(ctx, instanceID, staleIP); delErr != nil {
				s.log.DebugContext(ctx, "no network interface to clean up for stale instance", "id", instanceID)
			}
		} else {
			return nil, status.Errorf(codes.AlreadyExists, "instance %s already exists", instanceID)
		}
	} else if !errors.Is(err, api.ErrNotFound) {
		return nil, status.Errorf(codes.Internal, "error checking instance existence: %s", err)
	}

	// Setup rollback to cleanup resources on error
	rb := &createInstanceRollback{
		ctx:             ctx,
		log:             s.log,
		serviceContext:  s.context,
		instanceID:      instanceID,
		proxyManager:    s.proxyManager,
		portAllocator:   s.portAllocator,
		runtimeAddress:  s.config.RuntimeAddress,
		enableHugepages: s.config.EnableHugepages,
	}
	defer func() {
		if err != nil {
			s.log.WarnContext(ctx, "instance creation failed, rolling back", "id", instanceID, "error", err)
			err = rb.EnhanceErrorWithBootLog(err)
			rb.Rollback()
		}
	}()

	// init state
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:    req.ID,
		State: api.CreateInstanceStatus_INIT,
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	instanceDir := s.getInstanceDir(instanceID)
	s.log.DebugContext(ctx, "instance dir", "id", instanceID, "path", instanceDir)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.instanceDir = instanceDir
	rb.instanceDirCreated = true

	// Persist instance config early with CREATING state so GetInstance can find it during creation
	created := time.Now().UnixNano()
	earlyInstance := &api.Instance{
		ID:        instanceID,
		Name:      req.Name,
		Image:     req.Image,
		State:     api.VMState_CREATING,
		Node:      s.config.Name,
		CreatedAt: created,
		UpdatedAt: created,
	}
	if err := s.saveInstanceConfig(earlyInstance); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// networking
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_NETWORK,
		Message: "configuring networking",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.log.DebugContext(ctx, "creating network interface", "id", instanceID)
	networkInterface, err := s.context.NetworkManager.CreateInterface(ctx, instanceID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.networkCreated = true
	if networkInterface.IP != nil {
		rb.networkIP = networkInterface.IP.IPV4
	}

	// ensure gateway IP (for shelley config)
	gatewayIP := ""
	if ip := networkInterface.IP; ip != nil {
		gatewayIP = ip.GatewayV4
	}
	if gatewayIP == "" {
		return nil, status.Error(codes.Internal, "unable to get gateway IP for network interface")
	}

	// create instance fs
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_INIT,
		Message: "configuring storage",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// linux only supported for now
	platform := fmt.Sprintf("linux/%s", runtime.GOARCH)
	s.log.DebugContext(ctx, "creating instance fs", "id", instanceID)

	imageMetadata, err := s.context.ImageManager.FetchManifestForPlatform(ctx, req.Image, platform)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error fetching image manifest: %s", err)
	}
	s.log.DebugContext(ctx, "loaded image manifest", "image", req.Image, "digest", imageMetadata.Digest)

	// config
	imageConfig := imageMetadata.Config
	imageFSID := imageMetadata.Digest

	// attempt to get the base disk and create if not found

	// if not found provision image
	if _, err := s.context.StorageManager.Get(ctx, imageFSID); err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			return nil, status.Errorf(codes.Internal, "error getting storage filesystem for %s: %s", req.Image, err)
		}

		if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
			ID:      instanceID,
			State:   api.CreateInstanceStatus_PULLING,
			Message: fmt.Sprintf("fetching %s", req.Image),
		}); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// use ImageLoader which has singleflight coordination
		imageID, err := s.context.ImageLoader.LoadImage(ctx, req.Image, platform)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		// update to ensure latest ID
		imageFSID = imageID
		// update rollback configs
		rb.imageFSID = imageID
		rb.imageFSMounted = false
	}

	// clone
	if err = s.context.StorageManager.Clone(ctx, imageFSID, instanceID); err != nil {
		return nil, status.Errorf(codes.Internal, "error cloning instance storage for image %s: %s", req.Image, err)
	}
	rb.instanceCloned = true

	// resize
	if err = s.context.StorageManager.Expand(ctx, instanceID, req.Disk); err != nil {
		return nil, status.Errorf(codes.Internal, "error resizing instance filesystem storage: %s", err)
	}

	// get instance fs
	instanceFS, err := s.context.StorageManager.Get(ctx, instanceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error getting cloned instance fs: %s", err)
	}

	// mount
	mountConfig, err := s.context.StorageManager.Mount(ctx, instanceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error mounting instance filesystem: %s", err)
	}
	rb.instanceMounted = true
	mountpoint := mountConfig.Path

	// kernel
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_PULLING,
		Message: "configuring kernel",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// fetch kernel image
	if req.KernelImage != "" {
		s.log.DebugContext(ctx, "fetching kernel image", "image", req.KernelImage, "path", instanceDir)
		if _, err := s.context.ImageManager.Fetch(ctx, req.KernelImage, platform, instanceDir); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else { // use embedded
		s.log.DebugContext(ctx, "using default kernel image from exelet")
		kernel, err := exeletfs.Kernel()
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		kernelFilePath := filepath.Join(instanceDir, kernelName)
		s.log.DebugContext(ctx, "instance kernel path", "path", kernelFilePath)
		if err := os.MkdirAll(filepath.Dir(kernelFilePath), 0o775); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
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
	}

	// init
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_CONFIG,
		Message: "configuring init",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// inject rovol
	if err := exeletfs.CopyRovol(filepath.Join(mountpoint, "/exe.dev")); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// configs
	for _, cfg := range req.Configs {
		targetPath := filepath.Join(mountpoint, filepath.Clean(cfg.Destination))

		switch v := cfg.Source.(type) {
		case *api.Config_File:
			mode := os.FileMode(int(cfg.Mode))
			if err := os.WriteFile(targetPath, v.File.Data, mode); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "config type %T not supported", v)
		}
	}

	// volumes
	for _, vol := range req.Volumes {
		if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
			ID:      instanceID,
			State:   api.CreateInstanceStatus_CONFIG,
			Message: fmt.Sprintf("configuring volume %s", vol.Source),
		}); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		s.log.DebugContext(ctx, "configuring volume", "source", vol.Source)
		// TODO: handle other types (e.g. zfs snapshots)
		switch strings.ToLower(vol.Type) {
		case "image":
			volumeTarget := filepath.Join(mountpoint, filepath.Clean(vol.Mountpoint))
			s.log.DebugContext(ctx, "fetching image for volume", "image", vol.Source, "path", volumeTarget)
			if err := os.MkdirAll(volumeTarget, 0o755); err != nil {
				return nil, status.Errorf(codes.Internal, "error creating volume mountpoint: %s", err)
			}
			// fetch
			if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
				ID:      instanceID,
				State:   api.CreateInstanceStatus_PULLING,
				Message: fmt.Sprintf("pulling volume image %s", vol.Source),
			}); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			if _, err := s.context.ImageManager.Fetch(ctx, vol.Source, platform, volumeTarget); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		default:
			return nil, status.Error(codes.InvalidArgument, "only image volumes are currently supported")
		}
	}

	// set hostname
	s.log.DebugContext(ctx, "configuring hostname", "id", instanceID)
	hostnamePath := filepath.Join(mountpoint, config.HostnamePath)
	if err := os.MkdirAll(filepath.Dir(hostnamePath), 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.WriteFile(hostnamePath, fmt.Appendf([]byte{}, "%s", req.Name), 0o644); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// set /etc/hosts
	s.log.DebugContext(ctx, "configuring hosts", "id", instanceID)
	hostsPath := filepath.Join(mountpoint, config.HostsPath)
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	hostsContents := `# managed by exe.dev
127.0.0.1 localhost
%s %s %s
`
	ip := "127.0.0.1"
	if v := networkInterface.IP; v != nil {
		nIP, _, err := net.ParseCIDR(v.IPV4)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		ip = nIP.String()
	}
	if err := os.WriteFile(hostsPath, fmt.Appendf([]byte{}, hostsContents, ip, fmt.Sprintf("%s.%s", req.Name, s.config.InstanceDomain), req.Name), 0o644); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// set /etc/resolv.conf
	s.log.DebugContext(ctx, "configuring resolv.conf", "id", instanceID)
	resolvConfPath := filepath.Join(mountpoint, config.ResolvConfPath)
	if err := os.MkdirAll(filepath.Dir(resolvConfPath), 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	nameservers := []string{config.DefaultNameserver}
	if len(networkInterface.Nameservers) > 0 {
		nameservers = networkInterface.Nameservers
	}
	// remove existing
	_ = os.RemoveAll(resolvConfPath)
	nsF, err := os.Create(resolvConfPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for _, ns := range nameservers {
		if _, err := nsF.Write([]byte("nameserver " + ns + "\n")); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if err := nsF.Close(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// set instance env
	s.log.DebugContext(ctx, "configuring instance environment", "id", instanceID)
	envConfPath := filepath.Join(mountpoint, config.EnvConfigPath)
	if err := os.MkdirAll(filepath.Dir(envConfPath), 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	_ = os.RemoveAll(envConfPath)

	envF, err := os.Create(envConfPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for _, envVar := range req.Env {
		if _, err := envF.Write([]byte(envVar + "\n")); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if err := envF.Close(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Also write environment variables to /etc/profile.d/ so they're available to user sessions
	if len(req.Env) > 0 {
		profileDPath := filepath.Join(mountpoint, "etc", "profile.d")
		if err := os.MkdirAll(profileDPath, 0o755); err != nil {
			s.log.WarnContext(ctx, "failed to create /etc/profile.d", "error", err)
		} else {
			exeEnvPath := filepath.Join(profileDPath, "exe-env.sh")
			exeEnvF, err := os.Create(exeEnvPath)
			if err != nil {
				s.log.WarnContext(ctx, "failed to create /etc/profile.d/exe-env.sh", "error", err)
			} else {
				for _, envVar := range req.Env {
					// Parse KEY=VALUE
					parts := strings.SplitN(envVar, "=", 2)
					if len(parts) != 2 {
						s.log.WarnContext(ctx, "skipping invalid environment variable (no =)", "var", envVar)
						continue
					}
					key := parts[0]
					value := parts[1]

					// Validate key is a valid shell identifier
					if !isValidShellIdentifier(key) {
						s.log.WarnContext(ctx, "skipping invalid shell identifier", "key", key)
						continue
					}

					// Write with proper shell escaping using single quotes
					if _, err := fmt.Fprintf(exeEnvF, "export %s=%s\n", key, shellEscapeValue(value)); err != nil {
						s.log.WarnContext(ctx, "failed to write to /etc/profile.d/exe-env.sh", "error", err)
					}
				}
				if err := exeEnvF.Close(); err != nil {
					s.log.WarnContext(ctx, "failed to close /etc/profile.d/exe-env.sh", "error", err)
				} else {
					// Make the file executable
					if err := os.Chmod(exeEnvPath, 0o644); err != nil {
						s.log.WarnContext(ctx, "failed to chmod /etc/profile.d/exe-env.sh", "error", err)
					}
				}
			}
		}
	}

	// set image config for init
	s.log.DebugContext(ctx, "configuring instance image config", "id", instanceID)
	if imageConfig != nil {
		imageConfPath := filepath.Join(mountpoint, config.ImageConfigPath)
		if err := os.MkdirAll(filepath.Dir(imageConfPath), 0o755); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		imageConfData, err := json.Marshal(imageConfig.Config)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if err := os.WriteFile(imageConfPath, imageConfData, 0o644); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	// unmount
	s.log.DebugContext(ctx, "unmounting instance storage", "id", instanceID)
	if err := s.context.StorageManager.Unmount(ctx, instanceID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.instanceMounted = false

	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_BOOT,
		Message: "booting instance",
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// boot args (network config is derived from NetworkInterface at runtime)
	bootArgs := getBootArgs()
	// TODO: handle duplicates (e.g. init= etc.)
	bootArgs = append(bootArgs, req.BootArgs...)

	// create vm configuration
	vmCfg := &api.VMConfig{
		ID:               instanceID,
		Name:             req.Name,
		CPUs:             req.CPUs,
		Memory:           req.Memory,
		Disk:             req.Disk,
		RootDiskPath:     instanceFS.Path,
		KernelPath:       filepath.Join(instanceDir, kernelName),
		Args:             bootArgs,
		NetworkInterface: networkInterface,
	}

	if err := vmCfg.Validate(); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	s.log.DebugContext(ctx, "vm config", "config", vmCfg)

	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if err := vmm.Create(ctx, vmCfg); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.vmCreated = true

	// start vm
	if err := vmm.Start(ctx, vmCfg.ID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rb.vmStarted = true

	// allocate a port for SSH proxy
	sshPort, err := s.portAllocator.Allocate()
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "failed to allocate SSH port: %s", err)
	}
	rb.allocatedPort = sshPort

	// parse VM IP from network interface
	vmIP := ""
	if networkInterface.IP != nil && networkInterface.IP.IPV4 != "" {
		ipAddr, _, err := net.ParseCIDR(networkInterface.IP.IPV4)
		if err != nil {
			s.portAllocator.Release(sshPort)
			return nil, status.Errorf(codes.Internal, "failed to parse VM IP: %s", err)
		}
		vmIP = ipAddr.String()
	} else {
		s.portAllocator.Release(sshPort)
		return nil, status.Error(codes.Internal, "no IP address assigned to VM")
	}

	// create and start SSH proxy using socat
	s.log.DebugContext(ctx, "starting SSH proxy", "instance", instanceID, "port", sshPort, "target", fmt.Sprintf("%s:22", vmIP))
	if err := s.proxyManager.CreateProxy(instanceID, vmIP, sshPort, instanceDir); err != nil {
		s.portAllocator.Release(sshPort)
		return nil, status.Errorf(codes.Internal, "failed to start SSH proxy: %s", err)
	}
	rb.proxyCreated = true

	// Parse exposed ports from image config
	var exposedPorts []*api.ExposedPort
	if imageMetadata.Config != nil && imageMetadata.Config.Config.ExposedPorts != nil {
		for portSpec := range imageMetadata.Config.Config.ExposedPorts {
			// Parse "80/tcp" format
			parts := strings.Split(portSpec, "/")
			if len(parts) == 2 {
				if portNum, err := strconv.ParseUint(parts[0], 10, 32); err == nil {
					exposedPorts = append(exposedPorts, &api.ExposedPort{
						Port:     uint32(portNum),
						Protocol: parts[1],
					})
				}
			}
		}
	}

	// return instance info
	i := &api.Instance{
		ID:           instanceID,
		Name:         req.Name,
		Image:        req.Image,
		VMConfig:     vmCfg,
		Node:         s.config.Name,
		CreatedAt:    created, // preserve original creation time from early save
		UpdatedAt:    time.Now().UnixNano(),
		State:        api.VMState_STARTING,
		SSHPort:      int32(sshPort), // SSH proxy port
		ExposedPorts: exposedPorts,
	}

	if err := s.saveInstanceConfig(i); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// complete
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:    instanceID,
		State: api.CreateInstanceStatus_COMPLETE,
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// send instance
	if err := stream.Send(&api.CreateInstanceResponse{
		Type: &api.CreateInstanceResponse_Instance{
			Instance: i,
		},
	}); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return i, nil
}

func (s *Service) updateCreateStatus(stream api.ComputeService_CreateInstanceServer, status *api.CreateInstanceStatus) error {
	if err := stream.Send(&api.CreateInstanceResponse{
		Type: &api.CreateInstanceResponse_Status{
			Status: status,
		},
	}); err != nil {
		return err
	}
	return nil
}
