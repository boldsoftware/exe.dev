package compute

import (
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/config"
	exeletfs "exe.dev/exelet/fs"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	kernelName = "kernel"
)

// CreateInstance creates a new exelet instance
func (s *Service) CreateInstance(req *api.CreateInstanceRequest, stream api.ComputeService_CreateInstanceServer) error {
	// validate
	if err := req.Validate(); err != nil {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	s.log.Debug("creating instance", "request", req)
	var err error

	ctx := stream.Context()
	if req.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "error generating instance ID: %s", err)
		}
		req.ID = id.String()
	}
	instanceID := req.ID

	// init state
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:    req.ID,
		State: api.CreateInstanceStatus_INIT,
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	instanceDir := s.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// create instance fs
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_INIT,
		Message: "configuring storage",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	s.log.Debug("creating instance fs", "id", instanceID)
	instanceFS, err := s.context.StorageManager.Create(ctx, instanceID, &api.InstanceFilesystemConfig{
		FsType: "ext4",
		Size:   req.Disk,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "error creating instance storage: %s", err)
	}

	// mount
	mountConfig, err := s.context.StorageManager.Mount(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "error mounting instance storage: %s", err)
	}

	mountpoint := mountConfig.Path

	// handle create errors to cleanup if needed
	defer func() {
		if err != nil {
			s.log.Warn("cleaning up failed creation for instance", "id", instanceID)
			_ = s.context.StorageManager.Unmount(ctx, instanceID)
			// remove dataset
			_ = s.context.StorageManager.Delete(ctx, instanceID)
			// cleanup
			_ = os.RemoveAll(instanceDir)
		}
	}()

	// networking
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_NETWORK,
		Message: "configuring networking",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	s.log.Debug("creating network interface", "id", instanceID)
	networkInterface, err := s.networkManager.CreateInterface(ctx, instanceID)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	netConf, err := s.getNetConf(req.Name, networkInterface)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	// linux only supported for now
	platform := fmt.Sprintf("linux/%s", runtime.GOARCH)

	// fetch / unpack image content to snapshot
	s.log.Debug("fetching and unpacking image", "image", req.Image, "mountpoint", mountpoint)

	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_PULLING,
		Message: fmt.Sprintf("pulling %s", req.Image),
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	imageConfig, err := s.imageManager.Fetch(ctx, req.Image, platform, mountpoint)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	s.log.Debug("image config", "config", imageConfig)

	// kernel
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_PULLING,
		Message: "configuring kernel",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	// fetch kernel image
	if req.KernelImage != "" {
		s.log.Debug("fetching kernel image", "image", req.KernelImage, "path", instanceDir)
		if _, err := s.imageManager.Fetch(ctx, req.KernelImage, platform, instanceDir); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	} else { // use embedded
		kernel, err := exeletfs.Kernel()
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		kernelFilePath := filepath.Join(instanceDir, kernelName)
		if err := os.MkdirAll(filepath.Dir(kernelFilePath), 0o755); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		kernelFile, err := os.Create(kernelFilePath)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if _, err := io.Copy(kernelFile, kernel); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	// init
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_CONFIG,
		Message: "configuring init",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	initFiles := map[string]string{
		"exe-init": filepath.Join(mountpoint, config.InstanceExeInitPath),
		"exe-ssh":  filepath.Join(mountpoint, config.InstanceExeSshPath),
	}
	for name, dest := range initFiles {
		s.log.Debug("configuring init file", "name", name, "dest", dest)
		// exe-init
		initFile, err := exeletfs.Get(name)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		// ensure not present
		_ = os.Remove(dest)

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return status.Error(codes.Internal, err.Error())
		}

		exeInitFile, err := os.Create(dest)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}

		if _, err := io.Copy(exeInitFile, initFile); err != nil {
			return status.Error(codes.Internal, err.Error())
		}

		if err := exeInitFile.Close(); err != nil {
			return status.Error(codes.Internal, err.Error())
		}

		// executable
		if err := os.Chmod(dest, 0o755); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	// volumes
	for _, vol := range req.Volumes {
		if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
			ID:      instanceID,
			State:   api.CreateInstanceStatus_CONFIG,
			Message: fmt.Sprintf("configuring volume %s", vol.Source),
		}); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		s.log.Debug("configuring volume", "source", vol.Source)
		// TODO: handle other types (e.g. zfs snapshots)
		switch strings.ToLower(vol.Type) {
		case "image":
			volumeTarget := filepath.Join(mountpoint, filepath.Clean(vol.Mountpoint))
			s.log.Debug("fetching image for volume", "image", vol.Source, "path", volumeTarget)
			if err := os.MkdirAll(volumeTarget, 0o755); err != nil {
				return status.Errorf(codes.Internal, "error creating volume mountpoint: %s", err)
			}
			// fetch
			if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
				ID:      instanceID,
				State:   api.CreateInstanceStatus_PULLING,
				Message: fmt.Sprintf("pulling volume image %s", vol.Source),
			}); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			if _, err := s.imageManager.Fetch(ctx, vol.Source, platform, volumeTarget); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
		default:
			return status.Error(codes.InvalidArgument, "only image volumes are currently supported")
		}
	}

	// inject host key
	s.log.Debug("configuring ssh host identity")
	hostSSHKeyPath := filepath.Join(mountpoint, filepath.Dir(config.InstanceSSHHostKeyPath))
	if err := generateSSHHostKeyPair(hostSSHKeyPath); err != nil {
		return status.Errorf(codes.Internal, "error configuring ssh host identity: %s", err)
	}
	// inject ssh key
	s.log.Debug("configuring ssh keys", "keys", req.SSHKeys)
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_CONFIG,
		Message: "configuring instance",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	instanceSSHKeyPath := filepath.Join(mountpoint, config.InstanceSSHPublicKeysPath)
	if err := os.MkdirAll(filepath.Dir(instanceSSHKeyPath), 0o750); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	sshKeys := strings.Join(req.SSHKeys, "\n")
	if err := os.WriteFile(instanceSSHKeyPath, []byte(sshKeys), 0o600); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// set hostname
	s.log.Debug("configuring hostname", "id", instanceID)
	hostnamePath := filepath.Join(mountpoint, config.HostnamePath)
	if err := os.MkdirAll(filepath.Dir(hostnamePath), 0o755); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if err := os.WriteFile(hostnamePath, fmt.Appendf([]byte{}, "%s.%s", req.Name, config.DefaultInstanceDomain), 0o644); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// set /etc/hosts
	s.log.Debug("configuring hosts", "id", instanceID)
	hostsPath := filepath.Join(mountpoint, config.HostsPath)
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o755); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	hostsContents := `# managed by exe.dev
127.0.0.1 localhost
%s %s %s
`
	ip := "127.0.0.1"
	if v := networkInterface.IP; v != nil {
		nIP, _, err := net.ParseCIDR(v.IPV4)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		ip = nIP.String()
	}
	if err := os.WriteFile(hostsPath, fmt.Appendf([]byte{}, hostsContents, ip, req.Name, fmt.Sprintf("%s.%s", req.Name, config.DefaultInstanceDomain)), 0o644); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// set /etc/resolv.conf
	s.log.Debug("configuring resolv.conf", "id", instanceID)
	resolvConfPath := filepath.Join(mountpoint, config.ResolvConfPath)
	if err := os.MkdirAll(filepath.Dir(resolvConfPath), 0o755); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	nameservers := []string{config.DefaultNameserver}
	if len(networkInterface.Nameservers) > 0 {
		nameservers = networkInterface.Nameservers
	}
	// remove existing
	_ = os.RemoveAll(resolvConfPath)
	nsF, err := os.Create(resolvConfPath)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	for _, ns := range nameservers {
		if _, err := nsF.Write([]byte("nameserver " + ns + "\n")); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
	if err := nsF.Close(); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// set instance env
	s.log.Debug("configuring instance environment", "id", instanceID)
	envConfPath := filepath.Join(mountpoint, config.EnvConfigPath)
	if err := os.MkdirAll(filepath.Dir(envConfPath), 0o755); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	_ = os.RemoveAll(envConfPath)

	envF, err := os.Create(envConfPath)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	for _, envVar := range req.Env {
		if _, err := envF.Write([]byte(envVar + "\n")); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
	if err := envF.Close(); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// set image config for init
	s.log.Debug("configuring instance image config", "id", instanceID)
	if imageConfig != nil {
		imageConfPath := filepath.Join(mountpoint, config.ImageConfigPath)
		if err := os.MkdirAll(filepath.Dir(imageConfPath), 0o755); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		imageConfData, err := json.Marshal(imageConfig.Config)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := os.WriteFile(imageConfPath, imageConfData, 0o644); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	// unmount
	s.log.Debug("unmounting instance storage", "id", instanceID)
	if err := s.context.StorageManager.Unmount(ctx, instanceID); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:      instanceID,
		State:   api.CreateInstanceStatus_BOOT,
		Message: "booting instance",
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	// boot args
	bootArgs := s.getBootArgs(netConf)
	// TODO: handle duplicates (e.g. if the user specifies init= etc.)
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
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	s.log.Debug("vm config", "config", vmCfg)

	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.config.NetworkManagerAddress, s.log)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	if err := vmm.Create(ctx, vmCfg); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// start vm
	if err := vmm.Start(ctx, vmCfg.ID); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// TODO: add agent vsock

	// return instance info
	created := time.Now().UnixNano()

	i := &api.Instance{
		ID:        instanceID,
		Name:      req.Name,
		Image:     req.Image,
		VMConfig:  vmCfg,
		Node:      s.config.Name,
		CreatedAt: created,
		UpdatedAt: created,
		State:     api.VMState_STARTING,
	}

	if err := s.saveInstanceConfig(i); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// complete
	if err := s.updateCreateStatus(stream, &api.CreateInstanceStatus{
		ID:    instanceID,
		State: api.CreateInstanceStatus_COMPLETE,
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// send instance
	if err := stream.Send(&api.CreateInstanceResponse{
		Type: &api.CreateInstanceResponse_Instance{
			Instance: i,
		},
	}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	return nil
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

func (s *Service) getNetConf(hostname string, i *api.NetworkInterface) (string, error) {
	// ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0-ip>:<dns1-ip>:<ntp0-ip>
	if i == nil {
		return "", nil
	}
	ip := ""
	gw := ""
	netmask := ""
	conf := "dhcp"
	if v := i.IP; v != nil && v.IPV4 != "" {
		ipSubnet := i.IP.IPV4
		gw = i.IP.GatewayV4
		iIP, ipnet, err := net.ParseCIDR(ipSubnet)
		if err != nil {
			return "", fmt.Errorf("invalid IP Address: %w", err)
		}
		netmask = net.IP(ipnet.Mask).String()
		ip = iIP.String()
		conf = "none"
	}

	device := i.DeviceName
	primaryNS := "1.1.1.1"
	backupNS := "8.8.8.8"
	switch len(i.Nameservers) {
	case 0:
	case 1:
		primaryNS = i.Nameservers[0]
	default:
		primaryNS = i.Nameservers[0]
		backupNS = i.Nameservers[1]
	}
	ntpServer := i.NTPServer
	return fmt.Sprintf("ip=%s:%s:%s:%s:%s:%s:%s:%s:%s:%s",
		ip,
		gw,
		gw,
		netmask,
		hostname,
		device,
		conf,
		primaryNS,
		backupNS,
		ntpServer,
	), nil
}

func generateSSHHostKeyPair(path string) error {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	p, err := ssh.MarshalPrivateKey(crypto.PrivateKey(priv), "")
	if err != nil {
		return err
	}
	privateKeyPem := pem.EncodeToMemory(p)
	publicKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	// write keys
	privateKeyName := filepath.Base(config.InstanceSSHHostKeyPath)
	publicKeyName := privateKeyName + ".pub"
	privateKeyPath := filepath.Join(path, privateKeyName)
	publicKeyPath := filepath.Join(path, publicKeyName)
	_ = os.Remove(privateKeyPath)
	_ = os.Remove(publicKeyPath)

	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(privateKeyPath, []byte(string(privateKeyPem)), 0o600); err != nil {
		return err
	}
	publicKeyStr := "ssh-ed25519" + " " + base64.StdEncoding.EncodeToString(publicKey.Marshal())
	if err := os.WriteFile(publicKeyPath, []byte(string(publicKeyStr)), 0o600); err != nil {
		return err
	}

	return nil
}
