//go:build linux

package raw

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func (s *Raw) mountInstanceFS(id string) (string, error) {
	stateData, err := os.ReadFile(s.getInstanceStatePath(id))
	if err != nil {
		return "", nil
	}
	loopPath := string(stateData)
	mountpoint, err := s.getInstanceFSMountpoint(id)
	if err != nil {
		return "", fmt.Errorf("error getting instance fs mountpoint for %s: %w", id, err)
	}
	if err := os.MkdirAll(mountpoint, 0o770); err != nil {
		return "", fmt.Errorf("error creating mountpoint for %s: %w", id, err)
	}

	// mount
	if err := unix.Mount(loopPath, mountpoint, "ext4", uintptr(0), ""); err != nil {
		// already mounted
		if err != unix.EBUSY {
			return "", fmt.Errorf("error mounting instance FS %s: %w", id, err)
		}
	}

	return mountpoint, nil
}

func (s *Raw) unmountInstanceFS(id string) error {
	mountpoint, err := s.getInstanceFSMountpoint(id)
	if err != nil {
		return err
	}

	if err := unix.Unmount(mountpoint, 0); err != nil {
		return err
	}

	// remove mountpoint
	if err := os.RemoveAll(mountpoint); err != nil {
		return err
	}

	return nil
}

func (s *Raw) allocateDisk(diskPath string, size uint64) error {
	f, err := os.OpenFile(diskPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// native fallocate first
	if err := unix.Fallocate(int(f.Fd()), 0, 0, int64(size)); err != nil {
		if err == unix.EOPNOTSUPP {
			// fallback to truncate
			return f.Truncate(int64(size))
		}
		return err
	}
	return nil
}
