//go:build !linux

package zfs

import (
	"path"
)

func (s *ZFS) createInstanceFS(_ string, _ uint64, _ string, _ bool) error {
	return ErrNotImplemented
}

func (s *ZFS) getInstanceDir(_ string) (string, error) {
	return "", ErrNotImplemented
}

func (s *ZFS) getInstanceEncryptionKeyPath(_ string) (string, error) {
	return "", ErrNotImplemented
}

func (s *ZFS) ensureFSExists(_ string) error {
	return ErrNotImplemented
}

func (s *ZFS) getDSName(id string) string {
	return path.Join(s.dsName, id)
}

func (s *ZFS) mountInstanceFS(_ string) (string, error) {
	return "", ErrNotImplemented
}

func (s *ZFS) unmountInstanceFS(_ string) error {
	return ErrNotImplemented
}

func (s *ZFS) getDSDiskPath(_ string) (string, error) {
	return "", ErrNotImplemented
}

func (s *ZFS) getInstanceFSMountpoint(_ string) (string, error) {
	return "", ErrNotImplemented
}

func (s *ZFS) removeInstanceFS(_ string) error {
	return ErrNotImplemented
}
