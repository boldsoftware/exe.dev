package v1

import (
	"fmt"
	"strings"
)

func (v *Volume) Validate() error {
	if v.Type == "" {
		return fmt.Errorf("volume type cannot be blank")
	}
	if v.Source == "" {
		return fmt.Errorf("volume source cannot be blank")
	}
	if v.Mountpoint == "" {
		return fmt.Errorf("volume mountpoint cannot be blank")
	}

	if strings.ToLower(v.Type) != "image" {
		return fmt.Errorf("only image:// volumes are currently supported")
	}
	return nil
}
