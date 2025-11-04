package raw

import "context"

// Unmount unmounts the specified instance fs
func (s *Raw) Unmount(ctx context.Context, id string) error {
	if err := s.unmountInstanceFS(id); err != nil {
		return err
	}
	return nil
}
