package cloudhypervisor

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) Update(ctx context.Context, req *api.VMConfig) error {
	if err := req.Validate(); err != nil {
		return err
	}

	if err := v.saveVMConfig(req); err != nil {
		return err
	}

	return nil
}
