package cloudhypervisor

import (
	"context"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) Console(ctx context.Context, id string) (string, error) {
	apiSocketPath := v.apiSocketPath(id)
	// Use retry=false - instance should be running
	c, err := client.NewCloudHypervisorClient(ctx, apiSocketPath, false, v.log)
	if err != nil {
		return "", err
	}
	defer c.Close()

	resp, err := c.GetVmInfoWithResponse(ctx)
	if err != nil {
		return "", err
	}

	return *resp.JSON200.Config.Console.File, nil
}
