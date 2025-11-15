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
		if err := v.virtiofsd(ctx,
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
		); err != nil {
			return nil, err
		}
		instances = append(instances, i)
	}

	return instances, nil
}

func virtiofsdSocketName(tag string) string {
	id := utils.GetID(tag)[:8]
	return fmt.Sprintf("%s-virtiofsd.sock", id)
}

func (v *VMM) virtiofsd(ctx context.Context, vArgs ...string) error {
	initPath, args, err := backgroundInit()
	if err != nil {
		return err
	}

	binPath, err := exec.LookPath(virtiofsdExecutableName)
	if err != nil {
		return err
	}

	args = append(args, binPath)
	args = append(args, vArgs...)

	v.log.DebugContext(ctx, "running virtiofsd", "path", binPath, "args", args)

	cmd := exec.CommandContext(ctx, initPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Foreground: false,
		Setsid:     true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}

	return nil
}
