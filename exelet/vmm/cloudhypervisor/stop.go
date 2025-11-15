package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) Stop(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(v.apiSocketPath(id), v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	v.log.DebugContext(ctx, "vm stop", "id", id)
	dResp, err := c.DeleteVMWithResponse(ctx)
	if err != nil {
		// instance already stopped
		if isNotConnected(err) {
			return nil
		}
		return err
	}

	if v := dResp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error deleting VM: status=%d %s", v, string(dResp.Body))
	}

	// stop the VMM
	if err := v.shutdownVMM(ctx, id); err != nil {
		return err
	}

	return nil
}
