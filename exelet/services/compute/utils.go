package compute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) getInstanceDir(id string) string {
	return filepath.Join(s.config.DataDir, instanceDataDir, id)
}

func (s *Service) getInstanceConfigPath(id string) string {
	return filepath.Join(s.getInstanceDir(id), "config.json")
}

// saveInstanceConfig persists the instance configuration in the local configuration store
func (s *Service) saveInstanceConfig(i *api.Instance) error {
	data, err := i.Marshal()
	if err != nil {
		return err
	}

	configPath := s.getInstanceConfigPath(i.ID)
	if err := os.RemoveAll(configPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	// update
	if err := os.WriteFile(configPath, data, 0o660); err != nil {
		return err
	}

	return nil
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

func getBootArgs(netConf string) []string {
	return []string{
		"console=hvc0",
		"root=/dev/vda",
		"init=/exe.dev/bin/exe-init",
		//"init=/bin/sh", // debug
		netConf,
		"rw",
	}
}

// Instances returns the instances known to the service.
func (s *Service) Instances(ctx context.Context) ([]*api.Instance, error) {
	return s.listInstances(ctx)
}

// GetInstanceByIP looks up an instance by its assigned IP address
// TODO(philip): Beware that this is linear in number of instances,
// and those are read from JSON files at the moment!
func (s *Service) GetInstanceByIP(ctx context.Context, ip string) (string, string, error) {
	instances, err := s.listInstances(ctx)
	if err != nil {
		return "", "", err
	}

	for _, instance := range instances {
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil {
			if instance.VMConfig.NetworkInterface.IP != nil {
				// Extract IP from CIDR notation (e.g., "192.168.70.2/24" -> "192.168.70.2")
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
