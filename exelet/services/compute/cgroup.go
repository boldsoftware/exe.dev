package compute

import (
	"context"
	"errors"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// cgroupPathForVM resolves the cgroup directory that cloud-hypervisor should
// be spawned into for the given VM. It looks up the VM's persisted group ID
// (if any) and delegates to the ServiceContext's CgroupPreparer (typically
// the resource manager).
//
// Returning ("", nil) disables CLONE_INTO_CGROUP placement for this VM — the
// VMM then starts in the exelet's current cgroup and the resource manager
// will move it later. This is the pre-fix behavior and is only used as a
// fallback (no preparer configured, no persisted config, etc.).
//
// Runs on the hot path of Create / Start / RestoreFromSnapshot: correctness
// here determines whether guest-RAM page faults are charged to the VM's
// cgroup or to whatever cgroup the exelet happened to be in.
func (s *Service) cgroupPathForVM(ctx context.Context, id string) (string, error) {
	if s == nil || s.context == nil {
		return "", nil
	}
	preparer := s.context.CgroupPreparer
	if preparer == nil {
		return "", nil
	}
	// Best-effort: read the persisted instance config for the group ID. New
	// VMs may not have a config yet; fall back to empty groupID which maps
	// to the default group slice in the resource manager.
	groupID := ""
	if inst, err := s.loadInstanceConfig(id); err == nil {
		groupID = inst.GroupID
	} else if !errors.Is(err, api.ErrNotFound) {
		return "", err
	}
	return preparer.PrepareVMCgroup(ctx, id, groupID)
}
