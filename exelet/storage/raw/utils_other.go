//go:build !linux

package raw

func (s *Raw) mountInstanceFS(id string) (string, error) {
	return "", ErrNotImplemented
}

func (s *Raw) unmountInstanceFS(id string) error {
	return ErrNotImplemented
}

func (s *Raw) allocateDisk(diskPath string, size uint64) error {
	return ErrNotImplemented
}
