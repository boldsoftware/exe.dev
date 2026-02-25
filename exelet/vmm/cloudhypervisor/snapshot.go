package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

func (v *VMM) Snapshot(ctx context.Context, id, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("failed to create snapshot dir: %w", err)
	}

	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to VM %s: %w", id, err)
	}
	defer c.Close()

	destURL := "file://" + destDir
	resp, err := c.PutVmSnapshotWithResponse(ctx, client.VmSnapshotConfig{
		DestinationUrl: &destURL,
	})
	if err != nil {
		return fmt.Errorf("failed to snapshot VM %s: %w", id, err)
	}

	if v := resp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error snapshotting VM %s: status=%d %s", id, v, string(resp.Body))
	}

	return nil
}

func (v *VMM) RestoreFromSnapshot(ctx context.Context, id, snapshotDir string) error {
	// Start the CH daemon (reuse the same logic as runAPIInstance but without CreateVM/BootVM)
	if _, err := v.startCHProcess(ctx, id); err != nil {
		return fmt.Errorf("failed to start CH process for restore: %w", err)
	}

	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to VM %s after CH start: %w", id, err)
	}
	defer c.Close()

	sourceURL := "file://" + snapshotDir
	prefault := true
	restoreResp, err := c.PutVmRestoreWithResponse(ctx, client.RestoreConfig{
		SourceUrl: sourceURL,
		Prefault:  &prefault,
	})
	if err != nil {
		return fmt.Errorf("failed to restore VM %s: %w", id, err)
	}

	if v := restoreResp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error restoring VM %s: status=%d %s", id, v, string(restoreResp.Body))
	}

	// Restored VM starts in paused state — resume it
	resumeResp, err := c.ResumeVMWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to resume restored VM %s: %w", id, err)
	}

	if v := resumeResp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error resuming restored VM %s: status=%d %s", id, v, string(resumeResp.Body))
	}

	return nil
}
