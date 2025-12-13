package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) Stop(ctx context.Context, id string) error {
	// Use retry=false - instance should be running
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
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

	// cleanup any orphaned processes and remove their metadata
	if err := v.cleanupOrphanedProcesses(ctx, id); err != nil {
		v.log.WarnContext(ctx, "failed to cleanup orphaned processes", "id", id, "error", err)
	}

	return nil
}
