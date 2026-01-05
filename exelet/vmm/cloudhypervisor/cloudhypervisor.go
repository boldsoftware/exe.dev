package cloudhypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exe.dev/exelet/config"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	cloudHypervisorExecutableName = "cloud-hypervisor"
	virtiofsdExecutableName       = "virtiofsd"
	bootLogName                   = "boot.log"
)

// NetworkManager is imported from parent vmm package to avoid import cycle
type NetworkManager interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error)
	DeleteInterface(ctx context.Context, id, ip string) error
}

type VMM struct {
	dataDir         string
	networkManager  NetworkManager
	enableHugepages bool
	log             *slog.Logger
}

// NewVMM returns a new CloudHypervisor based VMM
func NewVMM(addr string, nm NetworkManager, enableHugepages bool, log *slog.Logger) (*VMM, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	dataDir := u.Path
	if dataDir == "" {
		return nil, fmt.Errorf("cloudhypervisor runtime data path cannot be blank")
	}

	return &VMM{
		dataDir:         dataDir,
		networkManager:  nm,
		enableHugepages: enableHugepages,
		log:             log,
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
	// Filter out ip= args - network config is derived from NetworkInterface at runtime
	var filteredArgs []string
	for _, arg := range req.Args {
		if !strings.HasPrefix(arg, "ip=") {
			filteredArgs = append(filteredArgs, arg)
		}
	}
	req.Args = filteredArgs

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

// apiSocketPath returns the path to the cloudhypervisor API socket for the specified VM id.
// Beware that Cloud Hypervisor has a limit of 107 characters for the socket path (SUN_LEN).
func (v *VMM) apiSocketPath(id string) string {
	return filepath.Join(v.getDataPath(id), "chh.sock")
}

func (v *VMM) bootLogPath(id string) string {
	return filepath.Join(v.getDataPath(id), bootLogName)
}

func (v *VMM) waitForReady(ctx context.Context, id string) error {
	// Create a timeout context for the overall wait
	waitCtx, cancel := context.WithTimeout(ctx, config.InstanceStartTimeout)
	defer cancel()

	apiSocketPath := v.apiSocketPath(id)

	// Use retry=true to keep trying with exponential backoff until context timeout
	c, err := client.NewCloudHypervisorClient(waitCtx, apiSocketPath, true, v.log)
	if err != nil {
		return fmt.Errorf("timeout waiting on instance api: %w", err)
	}
	defer c.Close()

	// Verify with a ping
	resp, err := c.GetVmmPingWithResponse(waitCtx)
	if err != nil {
		return fmt.Errorf("unable to ping api: %w", err)
	}
	v.log.DebugContext(ctx, "connected to api", "version", resp.JSON200.Version)
	return nil
}

// waitForShutdown waits until the API is no longer responding
// this enables cloudhypervisor to shutdown properly. otherwise
// a delete can remove the socket too quickly and keep the cloud
// hypervisor api process running
func (v *VMM) waitForShutdown(ctx context.Context, id string) error {
	t := time.NewTicker(time.Millisecond * 250)
	defer t.Stop()

	apiSocketPath := v.apiSocketPath(id)
	timeout := time.After(config.InstanceStopTimeout)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting on vmm shutdown")
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			// Use retry=false for fast fail - we want to detect when socket is gone
			c, err := client.NewCloudHypervisorClient(ctx, apiSocketPath, false, v.log)
			if err != nil {
				// Socket is gone or can't connect - VMM has shut down
				return nil
			}

			// Try to ping the VMM
			_, err = c.GetVmmPingWithResponse(ctx)
			c.Close()
			if err != nil {
				// VMM not responding - shutdown complete
				return nil
			}
		}
	}
}

func (v *VMM) shutdownVMM(ctx context.Context, id string) error {
	// Use retry=false - instance should already be running
	c, err := client.NewCloudHypervisorClient(ctx, v.apiSocketPath(id), false, v.log)
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
