package compute

import (
	"fmt"
	"os"
	"path/filepath"

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
			return nil, fmt.Errorf("%w: instance %s", ErrNotFound, id)
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
