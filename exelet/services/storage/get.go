package storage

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	exeletstorage "exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *Service) GetFilesystem(ctx context.Context, req *api.GetFilesystemRequest) (*api.GetFilesystemResponse, error) {
	// Resolve correct pool for tiered storage
	sm, err := exeletstorage.ResolveForID(ctx, s.context.StorageManager, req.ID)
	if err != nil {
		if errors.Is(err, api.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "filesystem %s", req.ID)
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	i, err := sm.Get(ctx, req.ID)
	if errors.Is(err, api.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "filesystem %s", req.ID)
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &api.GetFilesystemResponse{
		Filesystem: i,
	}, nil
}
