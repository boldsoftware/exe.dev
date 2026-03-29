package compute

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/exelet/atomicfile"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) getInstanceDir(id string) string {
	return filepath.Join(s.config.DataDir, instanceDataDir, id)
}

func (s *Service) getInstanceConfigPath(id string) string {
	return filepath.Join(s.getInstanceDir(id), "config.json")
}

// saveInstanceConfig persists the instance configuration in the local configuration store.
// Uses atomic write (write-to-tmp + rename) to prevent data loss on crash.
func (s *Service) saveInstanceConfig(i *api.Instance) error {
	data, err := i.Marshal()
	if err != nil {
		return err
	}

	configPath := s.getInstanceConfigPath(i.ID)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(configPath, data, 0o660)
}

// loadInstanceConfig loads the instance config for the id from the local config store
func (s *Service) loadInstanceConfig(id string) (*api.Instance, error) {
	var i api.Instance
	configPath := s.getInstanceConfigPath(id)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: instance %s", api.ErrNotFound, id)
		}
		return nil, err
	}
	if err := i.Unmarshal(data); err != nil {
		return nil, err
	}

	return &i, nil
}

func getBootArgs() []string {
	return []string{
		"console=hvc0",
		"root=/dev/vda",
		"init=/exe.dev/bin/exe-init",
		//"init=/bin/sh", // debug
		"rw",
		// increase RCU stall warning threshold from 21s to 60s
		"rcupdate.rcu_cpu_stall_timeout=60",
		// offload RCU callbacks to kthreads, reducing stalls from vCPU scheduling delays
		"rcu_nocbs=all",
	}
}

// Instances returns the instances known to the service.
func (s *Service) Instances(ctx context.Context) ([]*api.Instance, error) {
	return s.listInstances(ctx)
}

// GetInstanceByID returns instance details by ID (for InstanceLookup interface)
func (s *Service) GetInstanceByID(ctx context.Context, id string) (*api.Instance, error) {
	return s.getInstance(ctx, id)
}

// StopInstanceByID stops an instance by ID (for InstanceLookup interface)
func (s *Service) StopInstanceByID(ctx context.Context, id string) error {
	unlock := s.lockInstance(id)
	defer unlock()

	if err := s.checkNotMigrating(id); err != nil {
		return err
	}

	i, err := s.getInstance(ctx, id)
	if err != nil {
		return err
	}

	if i.State == api.VMState_STOPPED {
		return nil // Already stopped
	}

	if i.State != api.VMState_RUNNING {
		return fmt.Errorf("instance in invalid state to stop: %s", i.State)
	}

	return s.stopInstance(ctx, id)
}

// StartInstanceByID starts an instance by ID (for InstanceLookup interface)
func (s *Service) StartInstanceByID(ctx context.Context, id string) error {
	unlock := s.lockInstance(id)
	defer unlock()

	if err := s.checkNotMigrating(id); err != nil {
		return err
	}

	i, err := s.getInstance(ctx, id)
	if err != nil {
		return err
	}

	if i.State == api.VMState_RUNNING {
		return nil // Already running
	}

	if i.State != api.VMState_STOPPED {
		return fmt.Errorf("instance in invalid state to start: %s", i.State)
	}

	return s.startInstance(ctx, id)
}

// GetInstanceByIP looks up an instance by its assigned IP address.
func (s *Service) GetInstanceByIP(ctx context.Context, ip string) (string, string, error) {
	// TODO(philip): This is linear in number of instances,
	// and those are read from JSON files at the moment.
	instances, err := s.listInstances(ctx)
	if err != nil {
		return "", "", err
	}

	for _, instance := range instances {
		// Skip instances that are not actively using an IP. Stale config
		// files can linger briefly during deletion; only RUNNING and
		// STARTING instances have a valid IP lease.
		if instance.State != api.VMState_RUNNING && instance.State != api.VMState_STARTING {
			continue
		}
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil {
			if instance.VMConfig.NetworkInterface.IP != nil {
				// Extract IP from CIDR notation (e.g., "10.42.0.2/16" -> "10.42.0.2")
				instanceIP := instance.VMConfig.NetworkInterface.IP.IPV4
				if idx := strings.Index(instanceIP, "/"); idx > 0 {
					instanceIP = instanceIP[:idx]
				}
				if instanceIP == ip {
					return instance.ID, instance.Name, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("no instance found with IP %s", ip)
}

// copyFileIfChanged copies src to dest (mode 0755) only if dest does not exist
// or its SHA-256 digest differs from src. Returns true if a copy was performed.
func copyFileIfChanged(src, dest string) (bool, error) {
	srcHash, err := sha256File(src)
	if err != nil {
		return false, fmt.Errorf("hash src: %w", err)
	}
	dstHash, err := sha256File(dest)
	if err == nil && srcHash == dstHash {
		return false, nil
	}

	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()

	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return false, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func sha256File(path string) ([sha256.Size]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
