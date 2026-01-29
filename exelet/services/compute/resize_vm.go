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

func (s *Service) ResizeVM(ctx context.Context, req *api.ResizeVMRequest) (*api.ResizeVMResponse, error) {
	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance ID is required")
	}
	if req.Memory == nil && req.CPUs == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one of memory or cpus must be specified")
	}

	// Check if instance is being migrated
	if err := s.checkNotMigrating(req.ID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	// Get current instance to verify it exists and get current values
	instance, err := s.getInstance(ctx, req.ID)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("instance not found: %v", err))
	}

	resp := &api.ResizeVMResponse{
		OldMemory: instance.VMConfig.Memory,
		OldCPUs:   instance.VMConfig.CPUs,
	}

	// Update memory if specified
	if req.Memory != nil {
		newMemory := *req.Memory
		s.log.InfoContext(ctx, "resizing VM memory",
			"instance_id", req.ID,
			"old_memory", humanize.Bytes(instance.VMConfig.Memory),
			"new_memory", humanize.Bytes(newMemory),
		)
		instance.VMConfig.Memory = newMemory
		resp.NewMemory = newMemory
	} else {
		resp.NewMemory = instance.VMConfig.Memory
	}

	// Update CPUs if specified
	if req.CPUs != nil {
		newCPUs := *req.CPUs
		s.log.InfoContext(ctx, "resizing VM CPUs",
			"instance_id", req.ID,
			"old_cpus", instance.VMConfig.CPUs,
			"new_cpus", newCPUs,
		)
		instance.VMConfig.CPUs = newCPUs
		resp.NewCPUs = newCPUs
	} else {
		resp.NewCPUs = instance.VMConfig.CPUs
	}

	// Save the updated instance config (includes VMConfig)
	if err := s.saveInstanceConfig(instance); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to save instance config: %v", err))
	}

	// Also update the VMM config so changes take effect on restart
	vmmgr, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		s.log.WarnContext(ctx, "failed to create VMM for config update", "instance_id", req.ID, "error", err)
	} else {
		s.log.InfoContext(ctx, "updating VMM config",
			"instance_id", req.ID,
			"vmconfig_id", instance.VMConfig.ID,
			"memory", instance.VMConfig.Memory,
			"cpus", instance.VMConfig.CPUs,
		)
		if err := vmmgr.Update(ctx, instance.VMConfig); err != nil {
			s.log.WarnContext(ctx, "failed to update VMM config", "instance_id", req.ID, "error", err)
		} else {
			s.log.InfoContext(ctx, "VMM config updated successfully", "instance_id", req.ID)
		}
	}

	s.log.InfoContext(ctx, "VM resize config saved successfully",
		"instance_id", req.ID,
		"new_memory", humanize.Bytes(resp.NewMemory),
		"new_cpus", resp.NewCPUs,
	)

	return resp, nil
}
