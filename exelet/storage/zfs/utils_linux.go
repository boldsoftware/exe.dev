//go:build linux

package zfs

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistifyio/go-zfs/v3"
	"golang.org/x/sys/unix"
)

func (s *ZFS) createInstanceFS(id string, size uint64, fsType string, encrypted bool) error {
	dsName := s.getDSName(id)
	s.log.Debug("creating instance fs", "ds", dsName)

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return err
	}

	s.log.Debug("creating instance fs", "disk", diskPath)

	// check for existing ds
	if _, err := zfs.GetDataset(dsName); err != nil {
		if !zfsNotExist(err) {
			return fmt.Errorf("error getting dataset %s: %w (%T)", dsName, err, err)
		}
		// create
		props := map[string]string{}
		if encrypted {
			ekPath, err := s.getInstanceEncryptionKeyPath(id)
			if err != nil {
				return fmt.Errorf("error getting encryption key for %s: %w", id, err)
			}
			// create temporary
			props["encryption"] = "aes-256-gcm"
			props["keyformat"] = "hex"
			props["keylocation"] = fmt.Sprintf("file://%s", ekPath)
		}
		s.log.Debug("creating zfs volume", "name", dsName)
		if _, err := zfs.CreateVolume(dsName, size, props); err != nil {
			return err
		}
		// there is a race between when the volume is created and when the
		// disk is present in /dev/zvol. add a wait until ready here to check

		s.log.Debug("waiting for zvol to be present in device list", "name", dsName)
		if err := s.waitForZvol(id); err != nil {
			return err
		}

		// format
		s.log.Debug("formatting zvol", "id", id)
		binPath, err := exec.LookPath(fmt.Sprintf("mkfs.%s", fsType))
		if err != nil {
			return fmt.Errorf("mkfs.%s not found in PATH: %w", fsType, err)
		}

		args := []string{
			"-F",
			diskPath,
		}

		cmd := exec.Command(binPath, args...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("error formatting %s: %w", id, err)
		}
	}

	return nil
}

func (s *ZFS) ensureFSExists(id string) error {
	s.log.Debug("ensuring instance fs", "id", id)
	dsName := s.getDSName(id)
	ds, err := zfs.GetDataset(dsName)
	if err != nil {
		return err
	}
	// load key for encrypted if needed (e.g. for restarts)
	encryption, err := ds.GetProperty("encryption")
	if err != nil {
		return err
	}
	if !strings.EqualFold(encryption, "off") {
		// check if loaded
		status, err := ds.GetProperty("keystatus")
		if err != nil {
			return err
		}
		if !strings.EqualFold(status, "available") {
			ekPath, err := s.getInstanceEncryptionKeyPath(id)
			if err != nil {
				return err
			}
			if err := loadKey(dsName, ekPath); err != nil {
				return err
			}
		}
		// wait until ready
		if err := s.waitForZvol(id); err != nil {
			return err
		}
	}

	return nil
}

func (s *ZFS) mountInstanceFS(id string) (string, error) {
	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return "", err
	}
	mountpoint, err := s.getInstanceFSMountpoint(id)
	if err != nil {
		return "", fmt.Errorf("error getting instance fs mountpoint for %s: %w", id, err)
	}
	if err := os.MkdirAll(mountpoint, 0o770); err != nil {
		return "", fmt.Errorf("error creating mountpoint for %s: %w", id, err)
	}

	// mount
	if err := unix.Mount(diskPath, mountpoint, "ext4", uintptr(0), ""); err != nil {
		// already mounted
		if err != unix.EBUSY {
			return "", fmt.Errorf("error mounting instance FS %s: %w", id, err)
		}
	}

	return mountpoint, nil
}

func (s *ZFS) unmountInstanceFS(id string) error {
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

func (s *ZFS) getInstanceDir(id string) (string, error) {
	p := filepath.Join(s.dataDir, "volumes", id)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		return "", err
	}
	return p, nil
}

func (s *ZFS) getDSDiskPath(id string) (string, error) {
	return path.Join("/dev/zvol", s.getDSName(id)), nil
}

func (s *ZFS) getDSName(id string) string {
	return path.Join(s.dsName, id)
}

func (s *ZFS) getInstanceFSMountpoint(id string) (string, error) {
	p := filepath.Join(s.dataDir, "mounts", id)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		return "", err
	}
	return p, nil
}

func (s *ZFS) getInstanceEncryptionKeyPath(id string) (string, error) {
	instanceDir, err := s.getInstanceDir(id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(instanceDir, encryptionKeyName)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		return "", err
	}
	return p, nil
}

func (s *ZFS) removeInstanceFS(id string) error {
	fs, err := zfs.GetDataset(s.getDSName(id))
	if err != nil {
		return err
	}

	return fs.Destroy(zfs.DestroyRecursive)
}

func (s *ZFS) waitForZvol(id string) error {
	s.log.Debug("waiting on zvol", "id", id)
	t := time.NewTicker(time.Millisecond * 200)
	defer t.Stop()

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return err
	}

	readyCh := make(chan struct{})

	go func() {
		for range t.C {
			if _, err := os.Stat(diskPath); err == nil {
				readyCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(time.Second * 5):
		return fmt.Errorf("timeout waiting on zvol: %s", id)
	}
	s.log.Debug("zvol available", "id", id)

	return nil
}

func zfsNotExist(err error) bool {
	return strings.Contains(err.Error(), "does not exist")
}

func loadKey(ds, keyPath string) error {
	cmd := exec.Command("zfs", "load-key", "-L", fmt.Sprintf("file://%s", keyPath), ds)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("load-key failed: %v (%s)", err, out)
	}
	return nil
}
