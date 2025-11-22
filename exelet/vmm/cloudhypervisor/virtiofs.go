package cloudhypervisor

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"

	"exe.dev/exelet/utils"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// configureVirtiofs starts a virtiofsd instance for each share
func (v *VMM) configureVirtiofs(ctx context.Context, id string, threadPoolSize int, shares []*api.DirectoryShare) ([]*virtiofsInstance, error) {
	instances := []*virtiofsInstance{}
	for _, share := range shares {
		i := &virtiofsInstance{
			tag:        share.Tag,
			socketPath: filepath.Join(v.getDataPath(id), virtiofsdSocketName(share.Tag)),
		}

		// start virtiofsd
		pid, err := v.virtiofsd(ctx, id, share.Tag,
			"--syslog",
			"--shared-dir",
			share.HostPath,
			"--socket-path",
			i.socketPath,
			"--thread-pool-size",
			fmt.Sprintf("%d", threadPoolSize),
			"--sandbox=none",
			"--seccomp=none",
			"--cache=never",
			"--allow-mmap",
			"--allow-direct-io",
		)
		if err != nil {
			return nil, err
		}

		v.log.DebugContext(ctx, "virtiofsd started", "id", id, "tag", share.Tag, "pid", pid)
		instances = append(instances, i)
	}

	return instances, nil
}

func virtiofsdSocketName(tag string) string {
	id := utils.GetID(tag)[:8]
	return fmt.Sprintf("%s-virtiofsd.sock", id)
}

func (v *VMM) virtiofsd(ctx context.Context, id, tag string, vArgs ...string) (int, error) {
	binPath, err := exec.LookPath(virtiofsdExecutableName)
	if err != nil {
		return 0, err
	}

	v.log.DebugContext(ctx, "running virtiofsd", "path", binPath, "args", vArgs)

	cmd := exec.CommandContext(ctx, binPath, vArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	// capture PID immediately after start
	pid := cmd.Process.Pid

	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}

	// persist process metadata to disk
	processName := fmt.Sprintf("virtiofsd-%s", tag)
	if err := v.saveProcessMetadata(id, pid, processName); err != nil {
		// try to kill the process we just started
		v.log.WarnContext(ctx, "failed to save virtiofsd process metadata, cleaning up", "id", id, "tag", tag, "pid", pid, "error", err)
		if killErr := killProcess(pid); killErr != nil {
			v.log.WarnContext(ctx, "failed to kill virtiofsd process during cleanup", "id", id, "tag", tag, "pid", pid, "error", killErr)
		}
		return 0, fmt.Errorf("failed to save process metadata: %w", err)
	}

	return pid, nil
}
