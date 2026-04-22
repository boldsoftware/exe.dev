//go:build !linux

package cloudhypervisor

import (
	"context"
	"syscall"
)

// applyCgroupPlacement is a no-op on non-Linux platforms. CLONE_INTO_CGROUP
// is a Linux-specific feature and cgroup v2 doesn't exist on other operating
// systems.
func (v *VMM) applyCgroupPlacement(_ context.Context, _ string, _ *syscall.SysProcAttr) (func(), error) {
	return func() {}, nil
}
