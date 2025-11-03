package v1

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
)

// ByInstanceName sorts Instances by Name
type ByInstanceName []*Instance

func (v ByInstanceName) Len() int      { return len(v) }
func (v ByInstanceName) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func (v ByInstanceName) Less(i, j int) bool {
	// if same name; return by ID
	if v[i].Name == v[j].Name {
		return v[i].ID < v[j].ID
	}
	return v[i].Name < v[j].Name
}

func (v *CreateInstanceRequest) Validate() error {
	if v.Name == "" {
		return fmt.Errorf("name cannot be blank")
	}

	if v.Image == "" {
		return fmt.Errorf("image cannot be blank")
	}

	if v.CPUs == 0 {
		return fmt.Errorf("cpus cannot be 0")
	}

	if v.Memory == 0 {
		return fmt.Errorf("memory cannot be 0")
	}

	if v.Disk == 0 {
		return fmt.Errorf("disk cannot be 0")
	}

	minDiskSize := uint64(1000000000) // 1G
	if v.Disk < minDiskSize {
		return fmt.Errorf("minimum disk size is %d", minDiskSize)
	}

	return nil
}

func (v *Instance) Validate() error {
	if v.Name == "" {
		return fmt.Errorf("name cannot be blank")
	}

	if v.Image == "" {
		return fmt.Errorf("image cannot be blank")
	}

	if v.VMConfig == nil {
		return fmt.Errorf("VMConfig cannot be nil")
	}

	if err := v.VMConfig.Validate(); err != nil {
		return err
	}

	return nil
}

func (v *Instance) Marshal() ([]byte, error) {
	return protojson.Marshal(v)
}

func (v *Instance) Unmarshal(data []byte) error {
	return protojson.Unmarshal(data, v)
}
