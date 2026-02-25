package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) DeflateBalloon(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to VM %s: %w", id, err)
	}
	defer c.Close()

	zero := int64(0)
	resp, err := c.PutVmResizeWithResponse(ctx, client.VmResize{
		DesiredBalloon: &zero,
	})
	if err != nil {
		return fmt.Errorf("failed to deflate balloon for VM %s: %w", id, err)
	}

	if v := resp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error deflating balloon for VM %s: status=%d %s", id, v, string(resp.Body))
	}

	return nil
}

func (v *VMM) Pause(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to VM %s: %w", id, err)
	}
	defer c.Close()

	resp, err := c.PauseVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to pause VM %s: %w", id, err)
	}

	if v := resp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error pausing VM %s: status=%d %s", id, v, string(resp.Body))
	}

	return nil
}

func (v *VMM) Resume(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to VM %s: %w", id, err)
	}
	defer c.Close()

	resp, err := c.ResumeVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to resume VM %s: %w", id, err)
	}

	if v := resp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error resuming VM %s: status=%d %s", id, v, string(resp.Body))
	}

	return nil
}
