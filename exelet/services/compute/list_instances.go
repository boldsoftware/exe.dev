package compute

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) ListInstances(req *api.ListInstancesRequest, stream api.ComputeService_ListInstancesServer) error {
	ctx := stream.Context()
	instances, err := s.listInstances(ctx)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	for _, i := range instances {
		if err := stream.Send(&api.ListInstancesResponse{
			Instance: i,
		}); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	return nil
}

// listInstances returns all readable instance configs. If one or more configs
// fail to load, those are skipped and a non-nil error is returned alongside
// the partial list. Callers that mutate durable state based on this list
// (e.g. IPAM reconciliation) MUST treat a non-nil error as "do not act" —
// a partial list combined with destructive reconciliation caused the
// duplicate-IP incident that motivated this contract. Callers that only
// read the list (startup recovery, gRPC ListInstances) can proceed on the
// partial list after logging the error.
func (s *Service) listInstances(ctx context.Context) ([]*api.Instance, error) {
	configs, err := filepath.Glob(filepath.Join(s.getInstanceDir("*"), "config.json"))
	if err != nil {
		return nil, err
	}
	instances := []*api.Instance{}
	var loadErrs []string
	for _, config := range configs {
		id := filepath.Base(filepath.Dir(config))
		r, err := s.GetInstance(ctx, &api.GetInstanceRequest{
			ID: id,
		})
		if err != nil {
			// A legitimate race: filepath.Glob saw the directory, but
			// DeleteInstance removed it before GetInstance could read
			// the config. Treat this as "not present" and move on.
			if status.Code(err) == codes.NotFound {
				continue
			}
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		// update instance placement
		r.Instance.Placement = &api.Placement{
			Region: s.config.Region,
			Zone:   s.config.Zone,
		}

		instances = append(instances, r.Instance)
	}

	if len(loadErrs) > 0 {
		return instances, fmt.Errorf("failed to load %d instance config(s): %s", len(loadErrs), loadErrs)
	}
	return instances, nil
}
