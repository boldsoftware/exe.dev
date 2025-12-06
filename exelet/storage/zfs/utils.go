//go:build linux

package zfs

import (
	"context"
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
		props := map[string]string{
			"compression":  "lz4",
			"volblocksize": "4K",       // default to 4K size blocks for small random write optimization
			"primarycache": "metadata", // prevent double-caching
			"logbias":      "latency",  // default for random style workloads
			"sync":         "standard", // zfs handle fsyncs
		}
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
		volSize := align4K(size)
		ds, err := zfs.CreateVolume(dsName, volSize, props)
		if err != nil {
			return err
		}

		// make volume sparse (thin-provisioned) by removing refreservation
		// ZFS sets refreservation=volsize by default, making volumes thick-provisioned
		if err := ds.SetProperty("refreservation", "none"); err != nil {
			return fmt.Errorf("error setting refreservation for %s: %w", id, err)
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

// ensureFSExists checks if the dataset exists and is loaded and ready for use
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

	// Retry unmount with exponential backoff to handle "device or resource busy" errors
	const maxAttempts = 10
	const maxTimeout = 5 * time.Second
	initialDelay := 50 * time.Millisecond
	startTime := time.Now()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := unix.Unmount(mountpoint, 0); err != nil {
			// Check if it's a "not mounted" error - this is fine, just continue to cleanup
			if err == unix.EINVAL || err == unix.ENOENT {
				s.log.Debug("mountpoint not mounted or doesn't exist, continuing", "id", id, "mountpoint", mountpoint)
				break
			}

			// Check if it's a "device busy" error - retry
			if err == unix.EBUSY {
				elapsed := time.Since(startTime)
				if elapsed >= maxTimeout {
					return fmt.Errorf("error unmounting %s after %d attempts (timeout %v): device or resource busy", id, attempt, elapsed)
				}

				if attempt == maxAttempts {
					return fmt.Errorf("error unmounting %s after %d attempts: device or resource busy", id, attempt)
				}

				// Calculate backoff delay
				delay := initialDelay * time.Duration(1<<(attempt-1))
				s.log.Debug("retrying unmount", "id", id, "attempt", attempt, "delay", delay)

				remainingTime := maxTimeout - time.Since(startTime)
				if delay > remainingTime {
					delay = remainingTime
				}
				time.Sleep(delay)
				continue
			}

			// Other error - return immediately
			return fmt.Errorf("error unmounting %s: %w", id, err)
		}

		// Success
		if attempt > 1 {
			s.log.Debug("unmount succeeded", "id", id, "attempt", attempt)
		}
		break
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

	origin, err := fs.GetProperty("origin")
	if err != nil {
		return err
	}

	// remove the instance data
	// NOTE: this has to be done before removing the image snapshot because
	// otherwise it will report an error as there is still a dependent clone
	if err := s.retryDestroy(id, fs, fmt.Sprintf("instance filesystem %s", id)); err != nil {
		return err
	}

	// remove origin snapshot
	if origin != "" {
		s.log.Debug("removing image fs snapshot", "origin", origin, "id", id)
		imageSnap, err := zfs.GetDataset(origin)
		if err != nil {
			s.log.Warn("unable to get origin dataset for snapshot removal", "origin", origin, "id", id)
		}
		if imageSnap != nil {
			if err := s.retryDestroy(id, imageSnap, fmt.Sprintf("image snapshot %s", origin)); err != nil {
				return err
			}
		}
	}

	return nil
}

// retryDestroy attempts to destroy a ZFS dataset with exponential backoff
func (s *ZFS) retryDestroy(id string, ds *zfs.Dataset, description string) error {
	const maxAttempts = 10
	const maxTimeout = 30 * time.Second
	initialDelay := 100 * time.Millisecond
	startTime := time.Now()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ds.Destroy(zfs.DestroyRecursive); err != nil {
			lastErr = err

			// Check if we've exceeded the max timeout
			elapsed := time.Since(startTime)
			if elapsed >= maxTimeout {
				return fmt.Errorf("error removing %s %s after %d attempts (timeout %v): %w", description, id, attempt, elapsed, lastErr)
			}

			// Check if this is the last attempt
			if attempt == maxAttempts {
				return fmt.Errorf("error removing %s %s after %d attempts: %w", description, id, attempt, lastErr)
			}

			// Calculate backoff delay (exponential: 100ms, 200ms, 400ms, ...)
			delay := initialDelay * time.Duration(1<<(attempt-1))
			s.log.Debug("retrying destroy", "id", id, "description", description, "attempt", attempt, "delay", delay, "error", err)

			// Sleep with timeout awareness
			remainingTime := maxTimeout - time.Since(startTime)
			if delay > remainingTime {
				delay = remainingTime
			}
			time.Sleep(delay)
			continue
		}

		// Success
		if attempt > 1 {
			s.log.Debug("destroy succeeded", "id", id, "description", description, "attempt", attempt)
		}
		return nil
	}

	return lastErr
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

func fsck(ctx context.Context, diskPath string) error {
	cmd := exec.CommandContext(ctx, "e2fsck", "-f", "-p", diskPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error checking instance fs resize: %w (%s)", err, string(out))
	}

	return nil
}

func resize(ctx context.Context, diskPath string, size uint64) error {
	bin := "resize2fs"
	args := []string{diskPath}
	if size > 0 {
		// resize2fs doesn't accept bytes - convert to MB
		args = append(args, fmt.Sprintf("%dM", size/1024/1024))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error resizing fs: %w (%s)", err, string(out))
	}
	return nil
}

func resizeToMin(ctx context.Context, diskPath string) error {
	cmd := exec.CommandContext(ctx, "resize2fs", "-M", diskPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error resizing fs to min: %w (%s)", err, string(out))
	}
	return nil
}
