package raw

import (
	"context"
	"fmt"
	"os"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// Load ensures the instance fs is loaded and ready
func (s *Raw) Get(ctx context.Context, id string) (*api.Filesystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	statePath := s.getInstanceStatePath(id)
	if _, err := os.Stat(statePath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: filesystem %s", api.ErrNotFound, id)
		}
		return nil, err
	}

	// load loop from state
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}

	return &api.Filesystem{
		ID:   id,
		Path: string(stateData),
	}, nil
}
