package storage

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/utils"
	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *Service) LoadFilesystem(ctx context.Context, req *api.LoadFilesystemRequest) (*api.LoadFilesystemResponse, error) {
	s.log.DebugContext(ctx, "loading image into storage", "image", req.Image)
	platform := fmt.Sprintf("linux/%s", runtime.GOARCH)
	imageID, err := utils.LoadImage(ctx, req.Image, platform, s.context.ImageManager, s.context.StorageManager, s.log)
	if err != nil {
		if errors.Is(err, api.ErrResourceExists) {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &api.LoadFilesystemResponse{
		ID: imageID,
	}, nil
}
