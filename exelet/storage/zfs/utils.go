//go:build linux

package zfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
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
	dsName := s.getDSName(id)

	// Get origin before destroying (zfs get is faster than zfs list at scale)
	origin := s.getDatasetOrigin(dsName)

	// Promote any dependent clones before destroying
	// This handles the case where this dataset was cloned and we need to
	// "unlink" the clones so they become independent datasets
	if err := s.promoteDependentClones(dsName); err != nil {
		return fmt.Errorf("error promoting dependent clones for %s: %w", id, err)
	}

	// Remove the instance data directly using zfs destroy
	// NOTE: this has to be done before removing the image snapshot because
	// otherwise it will report an error as there is still a dependent clone
	if err := s.destroyDataset(dsName); err != nil {
		return fmt.Errorf("error removing instance filesystem %s: %w", id, err)
	}

	// Try to remove origin snapshot (single attempt, no retries)
	// This often fails silently if promoted clones now depend on it, which is expected
	if origin != "" {
		cmd := exec.Command("zfs", "destroy", origin)
		_ = cmd.Run()
	}

	return nil
}

// getDatasetOrigin returns the origin property of a dataset, or empty string if none/error
func (s *ZFS) getDatasetOrigin(dsName string) string {
	cmd := exec.Command("zfs", "get", "-Hp", "-o", "value", "origin", dsName)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	origin := strings.TrimSpace(string(out))
	if origin == "-" {
		return ""
	}
	return origin
}

// getDependentClones returns all clones that depend on this dataset's snapshots.
// It does this in a single ZFS command by listing snapshots with their clones property.
// Returns (nil, nil) only if the dataset doesn't exist. Other errors are propagated.
func (s *ZFS) getDependentClones(dsName string) ([]string, error) {
	// Get all snapshots and their clones in one command
	// Format: snapshot_name<tab>clones_value
	cmd := exec.Command("zfs", "list", "-t", "snapshot", "-H", "-o", "name,clones", "-d", "1", dsName)
	out, err := cmd.Output()
	if err != nil {
		// Check if this is a "does not exist" error - that's expected and safe
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "does not exist") {
				return nil, nil
			}
		}
		// Any other error should be propagated - could be transient failure
		return nil, fmt.Errorf("zfs list snapshots for %s failed: %w", dsName, err)
	}

	var clones []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		clonesStr := strings.TrimSpace(parts[1])
		if clonesStr == "-" || clonesStr == "" {
			continue
		}
		// Clones are comma-separated
		for _, clone := range strings.Split(clonesStr, ",") {
			clone = strings.TrimSpace(clone)
			if clone != "" {
				clones = append(clones, clone)
			}
		}
	}
	return clones, nil
}

// promoteDataset promotes a clone to be independent of its origin
func (s *ZFS) promoteDataset(dsName string) error {
	cmd := exec.Command("zfs", "promote", dsName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs promote %s failed: %w (%s)", dsName, err, string(out))
	}
	return nil
}

// promoteDependentClones promotes all clones that depend on this dataset's snapshots.
// This allows the dataset to be deleted even if it has been cloned.
func (s *ZFS) promoteDependentClones(dsName string) error {
	clones, err := s.getDependentClones(dsName)
	if err != nil {
		return err
	}

	for _, clone := range clones {
		s.log.Debug("promoting dependent clone", "clone", clone)
		if err := s.promoteDataset(clone); err != nil {
			return fmt.Errorf("failed to promote clone %s: %w", clone, err)
		}
	}

	return nil
}

// destroyDataset destroys a ZFS dataset directly by name with retry logic
func (s *ZFS) destroyDataset(dsName string) error {
	const maxAttempts = 10
	const maxTimeout = 30 * time.Second
	initialDelay := 100 * time.Millisecond
	startTime := time.Now()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("zfs", "destroy", "-r", dsName)
		if err := cmd.Run(); err != nil {
			lastErr = err

			// Check if dataset doesn't exist - that's success
			if strings.Contains(err.Error(), "does not exist") {
				return nil
			}

			// Check if we've exceeded the max timeout
			elapsed := time.Since(startTime)
			if elapsed >= maxTimeout {
				return fmt.Errorf("destroy %s failed after %d attempts (timeout %v): %w", dsName, attempt, elapsed, lastErr)
			}

			// Check if this is the last attempt
			if attempt == maxAttempts {
				return fmt.Errorf("destroy %s failed after %d attempts: %w", dsName, attempt, lastErr)
			}

			// Calculate backoff delay (exponential: 100ms, 200ms, 400ms, ...)
			delay := initialDelay * time.Duration(1<<(attempt-1))
			s.log.Debug("retrying destroy", "dataset", dsName, "attempt", attempt, "delay", delay, "error", err)

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
			s.log.Debug("destroy succeeded", "dataset", dsName, "attempt", attempt)
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
		return fmt.Errorf("timeout waiting on zvol %s on %s", id, s.hostname)
	}

	// Wait for udev to finish processing the device
	if err := exec.Command("udevadm", "settle").Run(); err != nil {
		return fmt.Errorf("udevadm settle failed: %w", err)
	}

	s.log.Debug("zvol available", "id", id)

	return nil
}

func zfsNotExist(err error) bool {
	return strings.Contains(err.Error(), "does not exist")
}

// baseImageMinAge is the minimum age a base image must have before it can be pruned.
// This prevents deleting recently created base images that simply haven't been used yet.
const baseImageMinAge = 14 * 24 * time.Hour // 2 weeks

// PruneOrphanedBaseImages removes base image datasets (sha256:xxx) that have no dependent clones
// and are older than baseImageMinAge. Returns the number of datasets pruned.
func (s *ZFS) PruneOrphanedBaseImages(ctx context.Context) (int, error) {
	// List all child datasets of the pool
	cmd := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", "-t", "filesystem,volume", "-r", "-d", "1", s.dsName)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to list datasets: %w", err)
	}

	var pruned int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dsName := strings.TrimSpace(line)
		if dsName == "" || dsName == s.dsName {
			continue
		}

		// Extract the child name (remove pool prefix)
		childName := strings.TrimPrefix(dsName, s.dsName+"/")

		// Only consider sha256: prefixed datasets (base images)
		if !strings.HasPrefix(childName, "sha256:") {
			continue
		}

		// Lock the base image to prevent races with concurrent clone operations.
		// Clone() locks on srcID (which would be this childName for base images).
		unlock := s.lockVolume(childName)

		// Check if the base image is old enough to be pruned
		creation, err := s.getDatasetCreation(ctx, dsName)
		if err != nil {
			s.log.WarnContext(ctx, "failed to get creation time for base image, skipping", "dataset", dsName, "error", err)
			unlock()
			continue
		}
		age := time.Since(creation)
		if age < baseImageMinAge {
			s.log.DebugContext(ctx, "skipping base image that is too new", "dataset", dsName, "age", age.Round(time.Hour))
			unlock()
			continue
		}

		// Check if dataset has any dependent clones
		clones, err := s.getDependentClones(dsName)
		if err != nil {
			s.log.WarnContext(ctx, "failed to get dependent clones for base image, skipping", "dataset", dsName, "error", err)
			unlock()
			continue
		}

		if len(clones) > 0 {
			s.log.DebugContext(ctx, "skipping base image with dependent clones", "dataset", dsName, "clones", len(clones))
			unlock()
			continue
		}

		// No dependents and old enough - safe to delete
		// Try to get the image reference for logging (best effort)
		imageRef, _ := s.GetUserProperty(ctx, childName, "exe:imageref")
		s.log.InfoContext(ctx, "pruning orphaned base image", "dataset", dsName, "imageref", imageRef, "age", age.Round(time.Hour))
		if err := s.destroyDataset(dsName); err != nil {
			s.log.ErrorContext(ctx, "failed to prune orphaned base image", "dataset", dsName, "error", err)
			unlock()
			continue
		}
		pruned++
		unlock()
	}

	return pruned, nil
}

// ListDatasets returns the IDs of all datasets in the pool, excluding the pool
// root and base images (sha256:xxx).
func (s *ZFS) ListDatasets(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", "-t", "filesystem,volume", "-r", "-d", "1", s.dsName)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list datasets: %w", err)
	}

	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dsName := strings.TrimSpace(line)
		if dsName == "" || dsName == s.dsName {
			continue
		}

		childName := strings.TrimPrefix(dsName, s.dsName+"/")
		if strings.HasPrefix(childName, "sha256:") {
			continue
		}

		ids = append(ids, childName)
	}

	return ids, nil
}

// getDatasetCreation returns the creation time of a ZFS dataset.
func (s *ZFS) getDatasetCreation(ctx context.Context, dsName string) (time.Time, error) {
	cmd := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "creation", dsName)
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("zfs get creation failed: %w", err)
	}
	timestamp := strings.TrimSpace(string(out))
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse creation timestamp %q: %w", timestamp, err)
	}
	return time.Unix(sec, 0), nil
}

// SetUserProperty sets a user-defined property on a dataset.
// Property names should be namespaced (e.g., "exe:imageref").
func (s *ZFS) SetUserProperty(ctx context.Context, id, property, value string) error {
	dsName := s.getDSName(id)
	cmd := exec.CommandContext(ctx, "zfs", "set", fmt.Sprintf("%s=%s", property, value), dsName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs set %s failed: %w (%s)", property, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// GetUserProperty gets a user-defined property from a dataset.
// Returns empty string if the property is not set (value is "-").
func (s *ZFS) GetUserProperty(ctx context.Context, id, property string) (string, error) {
	dsName := s.getDSName(id)
	cmd := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", property, dsName)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("zfs get %s failed: %w", property, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "-" {
		return "", nil
	}
	return value, nil
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
