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
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/atomicfile"
	exeletfs "exe.dev/exelet/fs"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// finalizeLiveReceive edits the CH snapshot config, restores the VM, and saves instance config as RUNNING.
func (s *Service) finalizeLiveReceive(ctx context.Context, instanceID, instanceDir, snapshotDir string, sourceInstance *api.Instance, groupID string, targetNetwork *api.NetworkInterface, rb *receiveVMRollback) (*api.Instance, bool, error) {
	// Load disk to get the target zvol path
	instanceFS, err := s.context.StorageManager.Load(ctx, instanceID)
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to load storage: %v", err)
	}

	kernelPath := filepath.Join(instanceDir, kernelName)

	// Edit CH snapshot config to fix disk path, kernel path, and boot args
	if err := editSnapshotConfig(snapshotDir, instanceFS.Path, kernelPath, s.vmm.OperatorSSHSocketPath(instanceID), sourceInstance.VMConfig, targetNetwork); err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to edit snapshot config: %v", err)
	}

	// Persist a minimal instance config BEFORE RestoreFromSnapshot so that
	// cloud-hypervisor is spawned into the correct per-VM cgroup via
	// CLONE_INTO_CGROUP. PrepareVMCgroup loads the persisted config to learn
	// the VM's GroupID; without this early write the file is absent and the
	// VMM falls back to spawning in the exelet's cgroup, so guest RAM pages
	// get mis-attributed for the lifetime of the VM. The final config (with
	// RUNNING/STOPPED state, SSH port, network, etc.) is saved below.
	earlyInstance := &api.Instance{
		ID:        instanceID,
		Name:      sourceInstance.Name,
		Image:     sourceInstance.Image,
		Node:      s.config.Name,
		GroupID:   groupID,
		State:     api.VMState_CREATING,
		CreatedAt: sourceInstance.CreatedAt,
		UpdatedAt: time.Now().UnixNano(),
	}
	if err := s.saveInstanceConfig(earlyInstance); err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to save early instance config: %v", err)
	}

	// Restore from snapshot (starts CH daemon, restores, resumes)
	s.log.InfoContext(ctx, "live: restoring VM from snapshot", "instance", instanceID)
	restoreErr := s.vmm.RestoreFromSnapshot(ctx, instanceID, snapshotDir)

	// Build the instance config (needed for both restore and fallback paths)
	vmConfig := s.adaptVMConfigForTarget(sourceInstance.VMConfig, instanceID, instanceDir)
	vmConfig.NetworkInterface = targetNetwork
	vmConfig.RootDiskPath = instanceFS.Path

	// Register a stop function so rollback can stop the CH process if a later step fails.
	// Must be set before the restoreErr check so the cold-boot fallback path doesn't trigger it.
	if restoreErr == nil {
		rb.stopVM = func() {
			s.log.WarnContext(ctx, "live: stopping restored VM due to rollback", "instance", instanceID)
			if err := s.vmm.Stop(ctx, instanceID); err != nil {
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
		if stopErr := s.vmm.Stop(ctx, instanceID); stopErr != nil {
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
			if err := s.context.NetworkManager.DeleteInterface(ctx, instanceID, delIP, targetNetwork.MACAddress); err != nil {
				s.log.WarnContext(ctx, "live: failed to delete live migration network interface", "instance", instanceID, "error", err)
			}
		}

		// Clear the stale network interface from vmConfig before persisting.
		// The IP lease was released (or failed) above, so persisting the old
		// targetNetwork IP would leave a config referencing an IP no longer
		// held in leases.json. A concurrent migration could then allocate that
		// same IP, resulting in two instances with duplicate IPs on disk.
		// startInstance will allocate a fresh interface.
		vmConfig.NetworkInterface = nil

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

		// Reconcile IPAM leases: if DeleteInterface above failed silently,
		// the old live-migration IP is orphaned. startInstance already persisted
		// the new IP to config, so the reconciler can safely identify the orphan.
		go s.reconcileIPLeases()

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

	if err := s.proxyManager.CreateProxy(ctx, instanceID, vmIP, sshPort, instanceDir); err != nil {
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
	if err := s.vmm.Update(ctx, vmConfig); err != nil {
		s.log.WarnContext(ctx, "live: failed to save VMM config", "instance", instanceID, "error", err)
	}

	return newInstance, false, nil
}

// editSnapshotConfig modifies the CH snapshot's config.json to point to the target's
// disk path, kernel path, and updated boot args (with new IP).
func editSnapshotConfig(snapshotDir, diskPath, kernelPath, operatorSSHSocket string, srcVMConfig *api.VMConfig, targetNetwork *api.NetworkInterface) error {
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

	// Update the operator-SSH vsock socket path so CH re-binds it under this
	// exelet's data dir rather than the source exelet's.
	if operatorSSHSocket != "" {
		if vs, ok := config["vsock"].(map[string]any); ok {
			vs["socket"] = operatorSSHSocket
		}
	}

	// Update cmdline: replace ip= boot arg with target IP (skip when targetNetwork is nil,
	// e.g., during local tier migration where the IP doesn't change)
	if targetNetwork != nil {
		if cmdline, ok := payload["cmdline"].(string); ok {
			payload["cmdline"] = replaceIPBootArg(cmdline, srcVMConfig.Name, targetNetwork)
		}
	}

	// Update network tap name: the source exelet may use a different tap naming
	// scheme (e.g. NAT uses random IDs like "tap-5c4c99", netns uses deterministic
	// names like "tap-vm000001"). CHV restores will create a new tap with the old
	// name if we don't patch it, leaving the VM disconnected from the target bridge.
	if targetNetwork != nil && targetNetwork.Name != "" {
		if nets, ok := config["net"].([]any); ok && len(nets) > 0 {
			if netCfg, ok := nets[0].(map[string]any); ok {
				netCfg["tap"] = targetNetwork.Name
			}
		}
	}

	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot config: %w", err)
	}

	if err := atomicfile.WriteFile(configPath, updated, 0o600); err != nil {
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
		DeleteInterface(ctx context.Context, id, ip, mac string) error
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
	// activeRecvWriters tracks pipe writers feeding in-flight zfs recv processes.
	// Rollback closes them to unblock zfs recv before attempting zfs destroy,
	// preventing a hang when the dataset is busy.
	mu                sync.Mutex
	activeRecvWriters []*io.PipeWriter
}

// trackRecvWriter registers a pipe writer so Rollback can close it.
func (r *receiveVMRollback) trackRecvWriter(pw *io.PipeWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeRecvWriters = append(r.activeRecvWriters, pw)
}

// closeRecvWriters closes all tracked pipe writers, terminating any in-flight
// zfs recv processes so that zfs destroy doesn't block on a busy dataset.
func (r *receiveVMRollback) closeRecvWriters() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pw := range r.activeRecvWriters {
		pw.CloseWithError(errors.New("rollback: closing in-flight zfs recv"))
	}
	r.activeRecvWriters = nil
}

func (r *receiveVMRollback) Rollback() {
	r.log.WarnContext(r.ctx, "rolling back receive VM", "instance", r.instanceID)

	// Use a background context to ensure cleanup completes
	ctx := context.WithoutCancel(r.ctx)

	// Stop the VM if live restore succeeded but a later step failed
	if r.stopVM != nil {
		r.stopVM()
	}

	// Close any in-flight zfs recv pipe writers BEFORE attempting zfs destroy.
	// Without this, zfs destroy blocks indefinitely on a busy dataset held
	// open by a still-running zfs recv process.
	r.closeRecvWriters()

	// Delete any partially created ZFS dataset for the instance.
	if r.zfsDatasetCreated {
		if err := r.storageManager.Delete(ctx, r.instanceID); err != nil {
			r.log.WarnContext(ctx, "failed to delete ZFS dataset during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Note: We intentionally do NOT delete the base image during rollback.
	// Base images are shared resources that may be used by other VMs and are
	// expensive to re-transfer. Leaving them around doesn't cause problems.

	// Release the IPAM lease BEFORE removing the instance directory so that
	// there is no window in which the on-disk config (claiming this IP) has
	// been torn down while the lease is still held. Without this ordering,
	// a racing reconciler would see the lease as orphaned (no instance
	// config references it) and could release it itself, briefly creating
	// competing owners; the rolling cursor keeps that from producing an
	// immediate duplicate, but this is the cleaner invariant.
	if r.targetNetwork != nil {
		ip := ""
		if r.targetNetwork.IP != nil {
			ip = r.targetNetwork.IP.IPV4
			if idx := strings.Index(ip, "/"); idx > 0 {
				ip = ip[:idx]
			}
		}
		if err := r.networkManager.DeleteInterface(ctx, r.instanceID, ip, r.targetNetwork.MACAddress); err != nil {
			r.log.WarnContext(ctx, "failed to delete network interface during rollback", "instance", r.instanceID, "error", err)
		}
	}

	// Remove instance directory (includes snapshot dir)
	if r.instanceDirCreated || r.snapshotDirCreated {
		if err := os.RemoveAll(r.instanceDir); err != nil {
			r.log.WarnContext(ctx, "failed to remove instance dir during rollback", "instance", r.instanceID, "error", err)
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

// sameVMIP reports whether the source instance and target network have the
// same VM IP (ignoring the CIDR prefix length). When they match, no in-guest
// IP reconfiguration is needed during live migration.
func sameVMIP(source *api.Instance, target *api.NetworkInterface) bool {
	if source == nil || source.VMConfig == nil || source.VMConfig.NetworkInterface == nil ||
		source.VMConfig.NetworkInterface.IP == nil || target == nil || target.IP == nil {
		return false
	}
	srcIP, _, err1 := net.ParseCIDR(source.VMConfig.NetworkInterface.IP.IPV4)
	dstIP, _, err2 := net.ParseCIDR(target.IP.IPV4)
	if err1 != nil || err2 != nil {
		return false
	}
	return srcIP.Equal(dstIP)
}
