package sshproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"exe.dev/exepipe/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// exepipeManager manages SSH proxies for instances,
// using exepipe to handle the actual connections.
type exepipeManager struct {
	exepipeAddress string
	bindIP         string
	lg             *slog.Logger

	cliMu sync.Mutex
	cli   *client.Client

	portsMu sync.Mutex
	ports   map[string]int // instanceID -> port
}

// NewExepipeManager creates a new SSH proxy manager using exepipe.
func NewExepipeManager(ctx context.Context, exepipeAddress, bindIP string, lg *slog.Logger) Manager {
	epm := &exepipeManager{
		exepipeAddress: exepipeAddress,
		bindIP:         bindIP,
		lg:             lg,
		ports:          make(map[string]int),
	}
	epm.getClient(ctx)
	return epm
}

// getClient returns the exepipe client, opening it if necessary.
// On error this may return nil.
func (epm *exepipeManager) getClient(ctx context.Context) *client.Client {
	epm.cliMu.Lock()
	defer epm.cliMu.Unlock()

	if epm.cli != nil {
		return epm.cli
	}

	// Allow some time for exepipe to restart.
	for i := range 20 {
		cli, err := client.NewClient(ctx, epm.exepipeAddress, epm.lg)
		if err == nil {
			epm.cli = cli
			return cli
		}

		epm.lg.ErrorContext(ctx, "failed to open exepipe client", "exepipeAddress", epm.exepipeAddress, "error", err)

		time.Sleep(time.Duration(i) * time.Millisecond)
	}

	return nil
}

// CreateProxy starts a new SSH proxy for an instance.
// The SSH proxy listens on the given port on epm.bindIP.
// It opens connections to port 22 on targetIP.
func (epm *exepipeManager) CreateProxy(ctx context.Context, instanceID, targetIP string, port int, instanceDir string) error {
	cli := epm.getClient(ctx)
	if cli == nil {
		return errors.New("unable to reach exepipe to start instance SSH proxy")
	}

	cli.Unlisten(ctx, instanceID) // ignore error

	epm.portsMu.Lock()
	delete(epm.ports, instanceID)
	epm.portsMu.Unlock()

	if err := epm.startProxy(ctx, cli, instanceID, targetIP, port); err != nil {
		return err
	}

	epm.portsMu.Lock()
	epm.ports[instanceID] = port
	epm.portsMu.Unlock()

	return nil
}

// startProxy starts an SSH proxy.
func (epm *exepipeManager) startProxy(ctx context.Context, cli *client.Client, instanceID, targetIP string, port int) error {
	var ip net.IP
	if epm.bindIP == "" {
		ip = net.IPv4zero
	} else {
		ip = net.ParseIP(epm.bindIP)
	}
	addr := &net.TCPAddr{
		IP:   ip,
		Port: port,
	}

	ln, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start ssh proxy listener: %v", err)
	}

	// The target port is always port 22, for ssh.
	if err := cli.Listen(ctx, instanceID, ln, targetIP, 22, "ssh"); err != nil {
		ln.Close()
		return fmt.Errorf("failed to start ssh proxy: %v", err)
	}

	return nil
}

// StopProxy stops an SSH proxy.
func (epm *exepipeManager) StopProxy(ctx context.Context, instanceID string) (int, error) {
	cli := epm.getClient(ctx)
	if cli == nil {
		return 0, errors.New("unable to reach exepipe to stop SSH proxy")
	}

	epm.portsMu.Lock()
	port, ok := epm.ports[instanceID]
	delete(epm.ports, instanceID)
	epm.portsMu.Unlock()

	if !ok {
		return 0, fmt.Errorf("no proxy found for instance %s", instanceID)
	}

	if err := cli.Unlisten(ctx, instanceID); err != nil {
		return 0, fmt.Errorf("failed to stop proxy: %v", err)
	}

	return port, nil
}

// GetPort returns the port that the proxy for an instance is listening on.
func (epm *exepipeManager) GetPort(ctx context.Context, instanceID string) (int, bool) {
	epm.portsMu.Lock()
	defer epm.portsMu.Unlock()
	port, exists := epm.ports[instanceID]
	return port, exists
}

// RecoverProxies fetches all the active listeners from exepipe.
// This is called on exelet startup to restore proxy state.
func (epm *exepipeManager) RecoverProxies(ctx context.Context, instances []*api.Instance) error {
	cli := epm.getClient(ctx)
	if cli == nil {
		return errors.New("unable to reach exepipe to recover SSH proxies")
	}

	m := make(map[string]client.Listener)
	for ln, err := range cli.Listeners(ctx) {
		if err != nil {
			return fmt.Errorf("error retrieving active ssh proxies: %v", err)
		}

		m[ln.Key] = ln
	}

	epm.portsMu.Lock()
	defer epm.portsMu.Unlock()

	for _, instance := range instances {
		if instance.State == api.VMState_STOPPED {
			continue
		}

		port := int(instance.SSHPort)
		if port == 0 {
			epm.lg.WarnContext(ctx, "instance has no SSH port configured", "instance", instance.ID)
			continue
		}

		targetIP := ""
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil && instance.VMConfig.NetworkInterface.IP != nil {
			if ipStr := instance.VMConfig.NetworkInterface.IP.IPV4; ipStr != "" {
				ipAddr, _, err := net.ParseCIDR(ipStr)
				if err != nil {
					epm.lg.WarnContext(ctx, "failed to parse VM IP", "instance", instance.ID, "ip", ipStr, "error", err)
					continue
				}
				targetIP = ipAddr.String()
			}
		}
		if targetIP == "" {
			epm.lg.WarnContext(ctx, "instance has no target IP configured", "instance", instance.ID)
			continue
		}

		ln, ok := m[instance.ID]
		if ok {
			epm.lg.InfoContext(ctx, "recovered running proxy", "instance", instance.ID, "port", ln.Port)
			epm.ports[instance.ID] = ln.Port
			delete(m, instance.ID)
		} else {
			epm.lg.InfoContext(ctx, "starting proxy for running instance", "instance", instance.ID, "port", port)
			if err := epm.startProxy(ctx, cli, instance.ID, targetIP, port); err != nil {
				epm.lg.ErrorContext(ctx, "failed to start SSH proxy", "instance", instance.ID, "error", err)
				continue
			}
			epm.ports[instance.ID] = port
		}
	}

	// Tell exepipe to stop listening on any instances we don't know about.
	for instanceID := range m {
		if err := cli.Unlisten(ctx, instanceID); err != nil {
			epm.lg.WarnContext(ctx, "failed to stop unknown ssh proxy", "instanceID", instanceID, "error", err)
		}
	}

	return nil
}
