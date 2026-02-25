package cloudhypervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"syscall"

	"exe.dev/exelet/config"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) Create(ctx context.Context, req *api.VMConfig) error {
	// create cloudhypervisor instance and store config and state
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.InstanceStartTimeout)
	defer cancel()

	if err := os.MkdirAll(v.getDataPath(req.ID), 0o770); err != nil {
		return err
	}

	if err := v.saveVMConfig(req); err != nil {
		return err
	}

	if err := v.runAPIInstance(ctx, req.ID); err != nil {
		return err
	}

	return nil
}

// startCHProcess starts the cloud-hypervisor daemon and waits for the API socket to be ready.
// It does NOT create or boot a VM — callers must do that separately (e.g., CreateVM+BootVM for
// fresh VMs, or PutVmRestore for snapshot restore).
// Returns alreadyRunning=true if the process was already running (and the VM may already be created).
func (v *VMM) startCHProcess(ctx context.Context, id string) (alreadyRunning bool, err error) {
	vmDataPath := v.getDataPath(id)
	apiSocketPath := v.apiSocketPath(id)

	// check if already running
	if _, err := os.Stat(apiSocketPath); err == nil {
		// attempt to connect - use retry=false for quick check
		c, err := client.NewCloudHypervisorClient(ctx, apiSocketPath, false, v.log)
		if err == nil {
			defer c.Close()
			if _, err := c.GetVmmPingWithResponse(ctx); err == nil {
				v.log.DebugContext(ctx, "cloudhypervisor api socket connected; skipping start")
				return true, nil
			}
		}
		// not connected; continue
	}

	binPath, err := exec.LookPath(cloudHypervisorExecutableName)
	if err != nil {
		return false, err
	}

	if err := os.MkdirAll(vmDataPath, 0o700); err != nil {
		return false, err
	}

	args := []string{
		"--api-socket",
		fmt.Sprintf("path=%s", apiSocketPath),
		"--seccomp=false",
	}

	// bootlog - use O_APPEND so writes always go to end of file,
	// which allows truncation-based log rotation to work correctly
	bootLogPath := v.bootLogPath(id)
	if _, err := os.Stat(bootLogPath); err == nil {
		if err := os.Remove(bootLogPath); err != nil {
			return false, err
		}
	}
	bootLog, err := os.OpenFile(bootLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, err
	}
	defer bootLog.Close()

	// Use exec.Command (not CommandContext) because cloud-hypervisor is a
	// long-running daemon that should outlive the create context. CommandContext
	// would kill the process when the context times out.
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = bootLog
	cmd.Stderr = bootLog
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group
	}
	v.log.DebugContext(ctx, "running cloudhypervisor api instance")
	if err := cmd.Start(); err != nil {
		return false, err
	}

	// capture PID immediately after start
	pid := cmd.Process.Pid
	v.log.DebugContext(ctx, "cloud-hypervisor started", "id", id, "pid", pid, "args", args)

	// wait for api to be ready
	if err := v.waitForReady(ctx, id); err != nil {
		v.log.ErrorContext(ctx, "cloud-hypervisor api not ready", "id", id, "pid", pid, "error", err)

		// check if process is still alive
		proc, procErr := os.FindProcess(pid)
		if procErr != nil {
			v.log.ErrorContext(ctx, "failed to find cloud-hypervisor process", "id", id, "pid", pid, "error", procErr)
		} else {
			// Signal 0 checks if process exists without sending a signal
			if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
				v.log.ErrorContext(ctx, "cloud-hypervisor process is not running", "id", id, "pid", pid, "error", sigErr)
			} else {
				v.log.ErrorContext(ctx, "cloud-hypervisor process is still running", "id", id, "pid", pid)
			}
		}

		// log boot log contents
		bootLogPath := v.bootLogPath(id)
		if bootLogData, readErr := os.ReadFile(bootLogPath); readErr != nil {
			v.log.ErrorContext(ctx, "failed to read boot log", "id", id, "path", bootLogPath, "error", readErr)
		} else {
			v.log.ErrorContext(ctx, "cloud-hypervisor boot log", "id", id, "contents", string(bootLogData))
		}

		return false, err
	}

	if err := cmd.Process.Release(); err != nil {
		return false, err
	}

	// persist process metadata to disk
	if err := v.saveProcessMetadata(id, pid, "cloud-hypervisor"); err != nil {
		// try to kill the process we just started
		v.log.WarnContext(ctx, "failed to save cloud-hypervisor process metadata, cleaning up", "id", id, "pid", pid, "error", err)
		if killErr := killProcess(pid); killErr != nil {
			v.log.WarnContext(ctx, "failed to kill cloud-hypervisor process during cleanup", "id", id, "pid", pid, "error", killErr)
		}
		return false, fmt.Errorf("failed to save process metadata: %w", err)
	}

	return false, nil
}

func (v *VMM) runAPIInstance(ctx context.Context, id string) error {
	vmCfg, err := v.loadVMConfig(id)
	if err != nil {
		return err
	}

	alreadyRunning, err := v.startCHProcess(ctx, id)
	if err != nil {
		return err
	}

	// If CH was already running, the VM is already created — skip CreateVM
	if alreadyRunning {
		return nil
	}

	// create - use retry=false since startCHProcess already confirmed socket is available
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	v.log.DebugContext(ctx, "creating VM in hypervisor", "id", id)
	virtiofsInstances, err := v.configureVirtiofs(ctx, id, int(vmCfg.CPUs), vmCfg.Shares)
	if err != nil {
		return err
	}

	// TODO: use FD from network interface for macvtap
	chVMCfg, err := v.toVmConfig(vmCfg, virtiofsInstances)
	if err != nil {
		return err
	}

	v.log.DebugContext(ctx, "vm configuration", "config", chVMCfg)

	cResp, err := c.CreateVMWithResponse(ctx, *chVMCfg)
	if err != nil {
		return err
	}

	if v := cResp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error creating VM: status=%d %s", v, string(cResp.Body))
	}

	return nil
}
