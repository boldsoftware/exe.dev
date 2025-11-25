package cloudhypervisor

import (
	"context"
	"fmt"
	"os"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) Delete(ctx context.Context, id, ip string) error {
	c, err := client.NewCloudHypervisorClient(v.apiSocketPath(id), v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	v.log.DebugContext(ctx, "shutting down vmm", "id", id)

	// shutdown VMM
	if err := v.shutdownVMM(ctx, id); err != nil {
		return err
	}

	// cleanup any orphaned processes before deleting data dir
	if err := v.cleanupOrphanedProcesses(ctx, id); err != nil {
		v.log.WarnContext(ctx, "failed to cleanup orphaned processes", "id", id, "error", err)
	}

	// remove tap and release DHCP lease
	if err := v.networkManager.DeleteInterface(ctx, id, ip); err != nil {
		return fmt.Errorf("error deleting network interface for %s: %w", id, err)
	}

	// delete data dir
	if err := os.RemoveAll(v.getDataPath(id)); err != nil {
		return err
	}

	return nil
}
