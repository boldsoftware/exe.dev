package raw

import (
	"context"
	"os"
)

// DeleteInstanceFS removes an instance fs
func (s *Raw) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	statePath := s.getInstanceStatePath(id)
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	loopPath := string(stateData)
	if err := detachLoopDevice(loopPath); err != nil {
		return err
	}

	instanceDir, err := s.getInstanceDir(id)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(instanceDir); err != nil {
		return err
	}

	if err := os.RemoveAll(statePath); err != nil {
		return err
	}
	return nil
}
