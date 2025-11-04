package cloudhypervisor

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) Get(ctx context.Context, id string) (*api.VMConfig, error) {
	cfg, err := v.loadVMConfig(id)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
