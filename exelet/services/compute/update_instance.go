package compute

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	exeletfs "exe.dev/exelet/fs"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) UpdateInstance(ctx context.Context, req *api.UpdateInstanceRequest) (*api.UpdateInstanceResponse, error) {
	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance ID is required")
	}

	// Check instance exists
	instanceDir := s.getInstanceDir(req.ID)
	if _, err := os.Stat(instanceDir); os.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "instance %s not found", req.ID)
	}

	// Check if instance is being migrated
	if err := s.checkNotMigrating(req.ID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	if req.Kernel {
		if err := s.updateKernel(ctx, req.ID); err != nil {
			return nil, err
		}
	}

	return &api.UpdateInstanceResponse{}, nil
}

func (s *Service) updateKernel(ctx context.Context, id string) error {
	instanceDir := s.getInstanceDir(id)
	kernelPath := filepath.Join(instanceDir, kernelName)

	s.log.InfoContext(ctx, "updating kernel", "id", id, "path", kernelPath)

	kernel, err := exeletfs.Kernel()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get embedded kernel: %v", err)
	}
	defer kernel.Close()

	if err := atomicWriteFile(kernelPath, kernel); err != nil {
		return status.Errorf(codes.Internal, "failed to write kernel: %v", err)
	}

	s.log.InfoContext(ctx, "kernel updated", "id", id)
	return nil
}

func atomicWriteFile(path string, r io.Reader) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	closed := false

	defer func() {
		if !closed {
			tmpFile.Close()
		}
		os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmpFile, r); err != nil {
		return fmt.Errorf("failed to write: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close: %w", err)
	}
	closed = true

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	return nil
}
