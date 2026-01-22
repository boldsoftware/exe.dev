package compute

import (
	"context"
	"fmt"

	"github.com/dustin/go-humanize"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	// minDiskGrowth is the minimum amount by which a disk can be grown (1GB)
	minDiskGrowth = 1 * 1024 * 1024 * 1024 // 1GB in bytes
	// maxDiskGrowth is the maximum amount by which a disk can be grown at once (250GB)
	maxDiskGrowth = 250 * 1024 * 1024 * 1024 // 250GB in bytes
)

func (s *Service) GrowDisk(ctx context.Context, req *api.GrowDiskRequest) (*api.GrowDiskResponse, error) {
	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance ID is required")
	}
	if req.AdditionalBytes < minDiskGrowth {
		return nil, status.Error(codes.InvalidArgument, "additional_bytes must be at least 1GB")
	}
	if req.AdditionalBytes > maxDiskGrowth {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("additional_bytes cannot exceed %s", humanize.Bytes(maxDiskGrowth)))
	}

	// Check if instance is being migrated
	if err := s.checkNotMigrating(req.ID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	// Get current instance to verify it exists and get current disk size
	instance, err := s.getInstance(ctx, req.ID)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("instance not found: %v", err))
	}

	oldSize := instance.VMConfig.Disk
	newSize := oldSize + req.AdditionalBytes

	s.log.InfoContext(ctx, "growing disk",
		"instance_id", req.ID,
		"old_size", humanize.Bytes(oldSize),
		"new_size", humanize.Bytes(newSize),
		"additional", humanize.Bytes(req.AdditionalBytes),
	)

	// Expand the ZFS volume (this works while the VM is running)
	// Pass resizeFilesystem=false since the filesystem is mounted inside the running VM
	// The user must run resize2fs /dev/vda from inside the VM after restarting
	if err := s.context.StorageManager.Expand(ctx, req.ID, newSize, false); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to expand disk: %v", err))
	}

	// Notify the VMM that the disk has been resized so the guest can see the new size
	vmmgr, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		s.log.WarnContext(ctx, "failed to create VMM for resize notification", "instance_id", req.ID, "error", err)
	} else {
		// The disk ID is "root" as configured in cloudhypervisor/config.go
		if err := vmmgr.ResizeDisk(ctx, req.ID, "root", newSize); err != nil {
			s.log.WarnContext(ctx, "failed to notify VMM of disk resize", "instance_id", req.ID, "error", err)
		}
	}

	// Update the saved VM config with the new disk size
	if err := s.updateInstanceDiskSize(ctx, req.ID, newSize); err != nil {
		// Log but don't fail - the disk is already expanded
		s.log.WarnContext(ctx, "failed to update instance config with new disk size", "instance_id", req.ID, "error", err)
	}

	s.log.InfoContext(ctx, "disk grown successfully",
		"instance_id", req.ID,
		"old_size", humanize.Bytes(oldSize),
		"new_size", humanize.Bytes(newSize),
	)

	return &api.GrowDiskResponse{
		OldSize: oldSize,
		NewSize: newSize,
	}, nil
}

// updateInstanceDiskSize updates the saved VM config with the new disk size
func (s *Service) updateInstanceDiskSize(ctx context.Context, id string, newSize uint64) error {
	// Load the current instance config
	instance, err := s.getInstance(ctx, id)
	if err != nil {
		return err
	}

	// Update the disk size in the config
	instance.VMConfig.Disk = newSize

	// Save the updated config
	return s.saveInstanceConfig(instance)
}
