package v1

import (
	"fmt"
	"runtime"

	"google.golang.org/protobuf/encoding/protojson"
)

func (v *VMConfig) Validate() error {
	if v.CPUs == 0 {
		return fmt.Errorf("CPUs cannot be 0")
	}

	if v.CPUs > uint64(runtime.NumCPU()) {
		return fmt.Errorf("maximum number of CPUs is %d", runtime.NumCPU())
	}

	if v.Memory == 0 {
		return fmt.Errorf("memory cannot be 0")
	}

	if v.KernelPath == "" {
		return fmt.Errorf("kernel path cannot be blank")
	}

	return nil
}

func (v *VMConfig) Marshal() ([]byte, error) {
	return protojson.Marshal(v)
}

func (v *VMConfig) Unmarshal(data []byte) error {
	return protojson.Unmarshal(data, v)
}
