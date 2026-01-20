package storage

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *Service) LoadFilesystem(ctx context.Context, req *api.LoadFilesystemRequest) (*api.LoadFilesystemResponse, error) {
	s.log.DebugContext(ctx, "loading image into storage", "image", req.Image)
	platform := fmt.Sprintf("linux/%s", runtime.GOARCH)

	imageID, err := s.LoadImage(ctx, req.Image, platform)
	if errors.Is(err, api.ErrResourceExists) {
		return nil, status.Error(codes.AlreadyExists, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &api.LoadFilesystemResponse{
		ID: imageID,
	}, nil
}
