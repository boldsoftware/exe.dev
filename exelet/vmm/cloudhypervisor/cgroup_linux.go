//go:build linux

package cloudhypervisor

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

// applyCgroupPlacement opens the target cgroup directory for the VM (if the
// VMM has a CgroupProvider configured) and mutates sys so that the child
// process is placed into that cgroup via CLONE_INTO_CGROUP. Returns a cleanup
// function that MUST be called after cmd.Start() to close the fd regardless
// of success.
//
// If no provider is configured or the provider returns an empty path, this is
// a no-op and the returned cleanup is also a no-op.
func (v *VMM) applyCgroupPlacement(ctx context.Context, id string, sys *syscall.SysProcAttr) (cleanup func(), err error) {
	cleanup = func() {}
	if v.cgroupPathFunc == nil {
		return cleanup, nil
	}
	path, err := v.cgroupPathFunc(ctx, id)
	if err != nil {
		return cleanup, fmt.Errorf("cgroup path for VM %s: %w", id, err)
	}
	if path == "" {
		return cleanup, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		// Not fatal — fall back to starting in the current cgroup. The
		// resource manager will later move the pid, which was the pre-fix
		// behavior, so this keeps recovery/edge cases working.
		v.log.WarnContext(ctx, "failed to open VM cgroup for CLONE_INTO_CGROUP; guest pages may be mis-attributed",
			"id", id, "path", path, "error", err)
		return cleanup, nil
	}
	sys.UseCgroupFD = true
	sys.CgroupFD = int(f.Fd())
	cleanup = func() {
		if cerr := f.Close(); cerr != nil {
			v.log.DebugContext(ctx, "close cgroup fd", "id", id, "error", cerr)
		}
	}
	v.log.DebugContext(ctx, "placing cloud-hypervisor into cgroup at exec", "id", id, "path", path)
	return cleanup, nil
}
