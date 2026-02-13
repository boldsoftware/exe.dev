//go:build linux

package desiredsync

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"exe.dev/exelet/storage"
)

// StorageDeviceResolver implements DeviceResolver by looking up the VM's
// filesystem path via a StorageManager and stat-ing the block device.
type StorageDeviceResolver struct {
	StorageManager storage.StorageManager
}

// ResolveDevice returns the "MAJ:MIN" string for a VM's block device.
func (r *StorageDeviceResolver) ResolveDevice(ctx context.Context, vmID string) (string, error) {
	fs, err := r.StorageManager.Get(ctx, vmID)
	if err != nil {
		return "", fmt.Errorf("get filesystem for %s: %w", vmID, err)
	}
	return getDeviceMajorMinor(fs.Path)
}

// getDeviceMajorMinor returns "MAJ:MIN" for a device path.
// If the path is a symlink (like /dev/zvol/...), it resolves to the real device.
func getDeviceMajorMinor(devicePath string) (string, error) {
	realPath, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", fmt.Errorf("resolve symlink %s: %w", devicePath, err)
	}

	var stat unix.Stat_t
	if err := unix.Stat(realPath, &stat); err != nil {
		return "", fmt.Errorf("stat device %s: %w", realPath, err)
	}
	major := unix.Major(uint64(stat.Rdev))
	minor := unix.Minor(uint64(stat.Rdev))
	return strconv.FormatUint(uint64(major), 10) + ":" + strconv.FormatUint(uint64(minor), 10), nil
}
