package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) Start(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(v.apiSocketPath(id), v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	// check if already running (e.g. from an agent restart)
	state, err := v.State(ctx, id)
	if err != nil {
		return err
	}

	v.log.DebugContext(ctx, "VM state", "id", id, "state", state)

	if state == api.VMState_RUNNING {
		return nil
	}

	// if stopped, start new api socket
	if err := v.runAPIInstance(ctx, id); err != nil {
		return err
	}

	resp, err := c.BootVMWithResponse(ctx)
	if err != nil {
		return err
	}

	if v := resp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error booting VM: status=%d %s", v, string(resp.Body))
	}

	return nil
}
