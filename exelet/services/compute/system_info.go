package compute

import (
	"context"
	"runtime"

	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/version"
)

// GetSystemInfo returns information about the exelet system including version and architecture.
func (s *Service) GetSystemInfo(ctx context.Context, req *api.GetSystemInfoRequest) (*api.GetSystemInfoResponse, error) {
	return &api.GetSystemInfoResponse{
		Version: version.FullVersion(),
		Arch:    runtime.GOARCH,
	}, nil
}
