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
	ctx, cancel := context.WithTimeout(context.Background(), config.InstanceStartTimeout)
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

func (v *VMM) runAPIInstance(ctx context.Context, id string) error {
	vmDataPath := v.getDataPath(id)
	apiSocketPath := v.apiSocketPath(id)

	vmCfg, err := v.loadVMConfig(id)
	if err != nil {
		return err
	}

	// check if already running
	if _, err := os.Stat(apiSocketPath); err == nil {
		// attempt to connect
		c, err := client.NewCloudHypervisorClient(apiSocketPath, v.log)
		if err == nil {
			defer c.Close()
			if _, err := c.GetVmmPingWithResponse(ctx); err == nil {
				v.log.Debug("cloudhypervisor api socket connected; skipping start")
				return nil
			}
		}
		// not connected; continue
	}

	// TODO: find a better way to handle cloudhypervisor api zombies
	initPath, args, err := backgroundInit()
	if err != nil {
		return err
	}

	binPath, err := exec.LookPath(cloudHypervisorExecutableName)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(vmDataPath, 0o700); err != nil {
		return err
	}

	args = append(args, []string{
		binPath,
		"--api-socket",
		fmt.Sprintf("path=%s", apiSocketPath),
		"--seccomp=false",
	}...)

	// bootlog
	bootLogPath := v.bootLogPath(id)
	if _, err := os.Stat(bootLogPath); err == nil {
		if err := os.Remove(bootLogPath); err != nil {
			return err
		}
	}
	bootLog, err := os.Create(bootLogPath)
	if err != nil {
		return err
	}
	defer bootLog.Close()

	cmd := exec.CommandContext(ctx, initPath, args...)
	cmd.Stdout = bootLog
	cmd.Stderr = bootLog
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Foreground: false,
		Setsid:     true,
		Setctty:    false,
	}
	v.log.Debug("running cloudhypervisor api instance")
	if err := cmd.Start(); err != nil {
		return err
	}
	// wait for api to be ready
	if err := v.waitForReady(ctx, id); err != nil {
		return err
	}

	if err := cmd.Process.Release(); err != nil {
		return err
	}

	// create
	c, err := client.NewCloudHypervisorClient(v.apiSocketPath(id), v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	v.log.Debug("creating VM in hypervisor", "id", id)
	virtiofsInstances, err := v.configureVirtiofs(ctx, id, int(vmCfg.CPUs), vmCfg.Shares)
	if err != nil {
		return err
	}

	// TODO: use FD from network interface for macvtap
	chVMCfg, err := v.toVmConfig(vmCfg, virtiofsInstances)
	if err != nil {
		return err
	}

	v.log.Debug("vm configuration", "config", chVMCfg)

	cResp, err := c.CreateVMWithResponse(ctx, chVMCfg)
	if err != nil {
		return err
	}

	if v := cResp.StatusCode(); v != http.StatusNoContent {
		return fmt.Errorf("error creating VM: status=%d %s", v, string(cResp.Body))
	}

	return nil
}
