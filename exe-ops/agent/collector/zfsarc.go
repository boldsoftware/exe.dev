package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// ZFSArc collects ZFS ARC (Adaptive Replacement Cache) statistics
// from /proc/spl/kstat/zfs/arcstats. Returns nil fields if ZFS is not installed.
type ZFSArc struct {
	Size    *int64
	HitRate *float64

	procPath string
}

func NewZFSArc() *ZFSArc {
	return &ZFSArc{procPath: "/proc/spl/kstat/zfs/arcstats"}
}

func (z *ZFSArc) Name() string { return "zfsarc" }

func (z *ZFSArc) Collect(_ context.Context) error {
	z.Size = nil
	z.HitRate = nil

	f, err := os.Open(z.procPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // ZFS not installed
		}
		return fmt.Errorf("open %s: %w", z.procPath, err)
	}
	defer f.Close()

	var size, hits, misses int64
	var hasSize, hasHits, hasMisses bool

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		val, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "size":
			size = val
			hasSize = true
		case "hits":
			hits = val
			hasHits = true
		case "misses":
			misses = val
			hasMisses = true
		}
	}

	if hasSize {
		z.Size = &size
	}
	if hasHits && hasMisses {
		total := hits + misses
		if total > 0 {
			rate := float64(hits) / float64(total) * 100
			z.HitRate = &rate
		}
	}
	return nil
}
