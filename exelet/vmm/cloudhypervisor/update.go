package cloudhypervisor

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
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

// ResizeDisk notifies cloud-hypervisor that a disk has been resized.
// This calls the cloud-hypervisor resize-disk API to notify the guest kernel
// about the new disk size.
//
// NOTE: The cloud-hypervisor resize-disk API calls ftruncate() on the disk backend,
// which fails with EINVAL on block devices (like ZFS zvols). The error occurs before
// the config change notification is sent to the guest, so online resize does NOT work
// for ZFS zvol-backed disks. The guest will see the new size after a reboot.
//
// We still attempt the API call in case future versions of cloud-hypervisor fix this,
// or in case the disk is file-backed.
func (v *VMM) ResizeDisk(ctx context.Context, id, diskID string, newSize uint64) error {
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return fmt.Errorf("failed to connect to cloud-hypervisor API: %w", err)
	}
	defer c.Close()

	size := int64(newSize)
	resp, err := c.PutVmResizeDisk(ctx, client.VmResizeDisk{
		Id:          &diskID,
		DesiredSize: &size,
	})
	if err != nil {
		return fmt.Errorf("failed to call resize-disk API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		v.log.WarnContext(ctx, "resize-disk API returned error (expected for block devices)",
			"id", id, "disk_id", diskID, "new_size", newSize,
			"status", resp.StatusCode, "body", string(body))
		// Don't return error - the zvol is already expanded, guest may need reboot
		return nil
	}

	v.log.InfoContext(ctx, "resize-disk API succeeded", "id", id, "disk_id", diskID, "new_size", newSize)
	return nil
}
