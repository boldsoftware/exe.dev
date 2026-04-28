package compute

import (
	"context"
	"os"
	"path/filepath"

	api "exe.dev/pkg/api/exe/compute/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CountInstances implements the grpc CountInstances call.
func (s *Service) CountInstances(ctx context.Context, req *api.CountInstancesRequest) (*api.CountInstancesResponse, error) {
	count, err := s.countInstances(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &api.CountInstancesResponse{
		Count: int32(count),
	}
	return ret, nil
}

// countInstances returns the number of instances on this exelet.
// This does not verify that the instances are running,
// but it does check that they exist in a minimal sense.
func (s *Service) countInstances(ctx context.Context) (int, error) {
	configs, err := filepath.Glob(filepath.Join(s.getInstanceDir("*"), "config.json"))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, config := range configs {
		id := filepath.Base(filepath.Dir(config))
		configPath := s.getInstanceConfigPath(id)
		f, err := os.Open(configPath)
		if err != nil {
			continue
		}
		f.Close()

		count++
	}

	return count, nil
}
