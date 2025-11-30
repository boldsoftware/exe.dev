package cloudhypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"exe.dev/exelet/config"
	"exe.dev/exelet/network"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	cloudHypervisorExecutableName = "cloud-hypervisor"
	virtiofsdExecutableName       = "virtiofsd"
	bootLogName                   = "boot.log"
)

type VMM struct {
	dataDir        string
	networkManager network.NetworkManager
	log            *slog.Logger
}

// NewVMM returns a new CloudHypervisor based VMM
func NewVMM(addr, nmAddr string, log *slog.Logger) (*VMM, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	dataDir := u.Path
	if dataDir == "" {
		return nil, fmt.Errorf("cloudhypervisor runtime data path cannot be blank")
	}

	nm, err := network.NewNetworkManager(nmAddr, log)
	if err != nil {
		return nil, err
	}

	return &VMM{
		dataDir:        dataDir,
		networkManager: nm,
		log:            log,
	}, nil
}

func (v *VMM) getVMConfigPath(id string) string {
	return filepath.Join(v.getDataPath(id), "config.json")
}

func (v *VMM) loadVMConfig(id string) (*api.VMConfig, error) {
	data, err := os.ReadFile(v.getVMConfigPath(id))
	if err != nil {
		return nil, err
	}

	var cfg api.VMConfig
	if err := cfg.Unmarshal(data); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (v *VMM) saveVMConfig(req *api.VMConfig) error {
	configPath := v.getVMConfigPath(req.ID)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	// remove existing
	if err := os.RemoveAll(configPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	vmF, err := os.Create(v.getVMConfigPath(req.ID))
	if err != nil {
		return err
	}
	defer vmF.Close()

	data, err := req.Marshal()
	if err != nil {
		return err
	}

	if _, err := vmF.Write(data); err != nil {
		return err
	}

	return nil
}

func (v *VMM) getDataPath(id string) string {
	return filepath.Join(v.dataDir, id)
}

func (v *VMM) apiSocketPath(id string) string {
	return filepath.Join(v.getDataPath(id), "cloud-hypervisor.sock")
}

func (v *VMM) bootLogPath(id string) string {
	return filepath.Join(v.getDataPath(id), bootLogName)
}

func (v *VMM) waitForReady(ctx context.Context, id string) error {
	readyCh := make(chan struct{})
	errCh := make(chan error)
	t := time.NewTicker(time.Millisecond * 500)
	defer t.Stop()

	apiSocketPath := v.apiSocketPath(id)
	go func() {
		for range t.C {
			// ping to check ready
			c, err := client.NewCloudHypervisorClient(apiSocketPath, v.log)
			if err != nil {
				errCh <- err
				return
			}

			resp, err := c.GetVmmPingWithResponse(ctx)
			// Close immediately after use (not defer) to avoid FD leak in loop
			c.Close()

			if err != nil {
				v.log.WarnContext(ctx, "unable to connect to api", "id", id)
				continue
			}
			v.log.DebugContext(ctx, "connected to api", "version", resp.JSON200.Version)
			readyCh <- struct{}{}
			return
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-readyCh:
		return nil
	case <-time.After(config.InstanceStartTimeout):
		return fmt.Errorf("timeout waiting on instance api")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *VMM) waitForStopped(ctx context.Context, id string) error {
	readyCh := make(chan struct{})
	errCh := make(chan error)
	t := time.NewTicker(time.Millisecond * 500)
	go func() {
		for range t.C {
			state, err := v.State(ctx, id)
			if err != nil {
				errCh <- err
				return
			}
			switch state {
			case api.VMState_STOPPED:
				v.log.DebugContext(ctx, "vm waitForStopped", "id", id)
				readyCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-readyCh:
		return nil
	case <-time.After(config.InstanceStopTimeout):
		return fmt.Errorf("timeout waiting on instance stop")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForShutdown waits until the API is no longer responding
// this enables cloudhypervisor to shutdown properly. otherwise
// a delete can remove the socket too quickly and keep the cloud
// hypervisor api process running
func (v *VMM) waitForShutdown(ctx context.Context, id string) error {
	readyCh := make(chan struct{})
	t := time.NewTicker(time.Millisecond * 250)
	defer t.Stop()
	go func() {
		for range t.C {
			// ping to check if VMM is still running
			apiSocketPath := v.apiSocketPath(id)
			c, err := client.NewCloudHypervisorClient(apiSocketPath, v.log)
			if err != nil {
				// Socket is gone or can't connect - VMM has shut down
				readyCh <- struct{}{}
				return
			}
			c.Close()

			// Try to ping the VMM
			if _, err := c.GetVmmPingWithResponse(ctx); err != nil {
				// VMM not responding - shutdown complete
				readyCh <- struct{}{}
				return
			}
		}
	}()

	select {
	case <-readyCh:
		return nil
	case <-time.After(config.InstanceStopTimeout):
		return fmt.Errorf("timeout waiting on vmm shutdown")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *VMM) shutdownVMM(ctx context.Context, id string) error {
	c, err := client.NewCloudHypervisorClient(v.apiSocketPath(id), v.log)
	if err != nil {
		return err
	}
	defer c.Close()

	// shutdown VMM
	resp, err := c.ShutdownVMMWithResponse(ctx)
	if err != nil {
		if isNotConnected(err) {
			return nil
		}
		return err
	}

	if v := resp.StatusCode(); v != http.StatusOK {
		return fmt.Errorf("error stopping VMM: status=%d %s", v, string(resp.Body))
	}

	// wait for shutdown
	v.log.DebugContext(ctx, "waiting for clean vmm shutdown", "id", id)
	if err := v.waitForShutdown(ctx, id); err != nil {
		return err
	}

	return nil
}
