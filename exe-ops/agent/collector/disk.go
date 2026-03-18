package collector

import (
	"context"
	"fmt"
	"syscall"
)

// Disk collects disk usage via syscall.Statfs.
type Disk struct {
	Total int64
	Used  int64
	Free  int64
	path  string
}

func NewDisk() *Disk { return &Disk{path: "/"} }

func (d *Disk) Name() string { return "disk" }

func (d *Disk) Collect(_ context.Context) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(d.path, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", d.path, err)
	}

	d.Total = int64(stat.Blocks) * int64(stat.Bsize)
	d.Free = int64(stat.Bfree) * int64(stat.Bsize)
	d.Used = d.Total - d.Free
	return nil
}
