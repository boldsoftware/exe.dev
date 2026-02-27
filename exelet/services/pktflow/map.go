package pktflow

import (
	"fmt"
	"os"
	"path/filepath"

	"exe.dev/exelet/utils"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// VMInfo describes a VM mapped to a tap device.
type VMInfo struct {
	VMID   string
	UserID string
	Tap    string
}

func LoadInstanceMap(dataDir string) (map[string]VMInfo, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data dir is required")
	}
	instancesDir := filepath.Join(dataDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read instances dir: %w", err)
	}

	result := make(map[string]VMInfo, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		configPath := filepath.Join(instancesDir, entry.Name(), "config.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read instance config %s: %w", configPath, err)
		}

		var inst api.Instance
		if err := inst.Unmarshal(data); err != nil {
			return nil, fmt.Errorf("parse instance config %s: %w", configPath, err)
		}

		vmID := inst.GetID()
		if vmID == "" {
			return nil, fmt.Errorf("instance config %s missing id", configPath)
		}
		userID := inst.GetGroupID()
		if userID == "" {
			continue
		}

		tap := utils.GetTapName(vmID)
		result[tap] = VMInfo{
			VMID:   vmID,
			UserID: userID,
			Tap:    tap,
		}
	}

	return result, nil
}
