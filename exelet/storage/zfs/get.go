//go:build linux

package zfs

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/mistifyio/go-zfs/v3"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// GetAll returns volsize/path metadata for every instance volume in the
// pool in a single `zfs list` invocation. Base images (sha256:*) and
// the pool root are excluded.
func (s *ZFS) GetAll(ctx context.Context) (map[string]*api.Filesystem, error) {
	cmd := exec.CommandContext(ctx, "zfs", "list", "-Hp",
		"-o", "name,volsize",
		"-t", "volume,filesystem",
		"-r", "-d", "1", s.dsName)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("zfs list failed for %s: %w: %s", s.dsName, err, strings.TrimSpace(stderr.String()))
	}

	prefix := s.dsName + "/"
	result := make(map[string]*api.Filesystem)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if !strings.HasPrefix(name, prefix) {
			continue // pool root
		}
		id := strings.TrimPrefix(name, prefix)
		if strings.ContainsRune(id, '/') {
			continue // -d 1 should prevent, but be defensive
		}
		if strings.HasPrefix(id, "sha256:") {
			continue // base images
		}
		var size uint64
		if fields[1] != "-" { // filesystems have no volsize
			if v, perr := strconv.ParseUint(fields[1], 10, 64); perr == nil {
				size = v
			}
		}
		// getDSDiskPath today is pure string concat and never errors; if
		// that ever changes we'd rather skip one entry than lose every
		// VM's bulk metric for the pool, so don't abort on error.
		diskPath, err := s.getDSDiskPath(id)
		if err != nil {
			continue
		}
		result[id] = &api.Filesystem{
			ID:   id,
			Path: diskPath,
			Size: size,
		}
	}
	return result, nil
}

func (s *ZFS) Get(ctx context.Context, id string) (*api.Filesystem, error) {
	dsName := s.getDSName(id)
	ds, err := zfs.GetDataset(dsName)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, api.ErrNotFound
		}
		return nil, err
	}

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return nil, err
	}

	volSize, err := ds.GetProperty("volsize")
	if err != nil {
		return nil, fmt.Errorf("error getting volsize: %w", err)
	}

	size, err := humanize.ParseBytes(volSize)
	if err != nil {
		return nil, fmt.Errorf("error parsing volsize: %w", err)
	}

	return &api.Filesystem{
		ID:   id,
		Path: diskPath,
		Size: size,
	}, nil
}
