package compute

import (
	"context"
	"fmt"
	"runtime"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) UpdateInstance(ctx context.Context, req *api.UpdateInstanceRequest) (*api.UpdateInstanceResponse, error) {
	// update instance
	if err := s.updateInstance(ctx, req.ID, req.KernelImage, req.InitImage); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &api.UpdateInstanceResponse{}, nil
}

func (s *Service) updateInstance(ctx context.Context, id, kernelImage, initImage string) error {
	var err error
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.config.NetworkManagerAddress, s.log)
	if err != nil {
		return fmt.Errorf("error getting vmm: %w", err)
	}

	// stop instance
	s.log.Debug("stopping instance for update", "id", id)
	if err := vmm.Stop(ctx, id); err != nil {
		return fmt.Errorf("error stopping instance: %w", err)
	}

	// handle update errors to cleanup if needed
	defer func() {
		if err != nil {
			s.log.Warn("cleaning up failed update for instance", "id", id)
			_ = s.context.StorageManager.Unmount(ctx, id)
		}
	}()

	platform := fmt.Sprintf("linux/%s", runtime.GOARCH)
	instanceDir := s.getInstanceDir(id)

	// mount
	s.log.Debug("mounting instance storage for update", "id", id)
	mountConfig, err := s.context.StorageManager.Mount(ctx, id)
	if err != nil {
		return fmt.Errorf("error mounting instance storage: %w", err)
	}
	mountpoint := mountConfig.Path

	// kernel
	if kernelImage != "" {
		s.log.Debug("updating instance kernel", "id", id, "image", kernelImage)
		// fetch / unpack image content to snapshot
		s.log.Debug("fetching and unpacking kernel", "image", kernelImage, "path", mountpoint)
		if _, err := s.context.ImageManager.Fetch(ctx, kernelImage, platform, instanceDir); err != nil {
			return fmt.Errorf("error updating kernel: %w", err)
		}
	}
	// init
	if initImage != "" {
		s.log.Debug("updating instance init", "id", id, "image", initImage)
		// fetch / unpack image content to snapshot
		s.log.Debug("fetching and unpacking init", "image", initImage, "path", mountpoint)
		if _, err := s.context.ImageManager.Fetch(ctx, initImage, platform, mountpoint); err != nil {
			return fmt.Errorf("error updating init: %w", err)
		}
	}

	// unmount
	if err := s.context.StorageManager.Unmount(ctx, id); err != nil {
		return fmt.Errorf("error unmounting instance storage: %w", err)
	}

	// start instance
	s.log.Debug("starting instance after update", "id", id)
	if err := vmm.Start(ctx, id); err != nil {
		return fmt.Errorf("error starting instance: %w", err)
	}

	return nil
}
