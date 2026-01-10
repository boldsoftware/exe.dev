package vmm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"exe.dev/exelet/vmm/cloudhypervisor"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// NetworkManager is a minimal interface to avoid import cycle with network package
type NetworkManager interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error)
	DeleteInterface(ctx context.Context, id, ip string) error
}

type VMM interface {
	// Create implements VM creation
	Create(ctx context.Context, req *api.VMConfig) error
	// Get returns the VM config
	Get(ctx context.Context, id string) (*api.VMConfig, error)
	// Start implements VM start
	Start(ctx context.Context, id string) error
	// State implements VM state
	State(ctx context.Context, id string) (api.VMState, error)
	// Update updates the VM instance
	Update(ctx context.Context, req *api.VMConfig) error
	// Logs implements VM logs
	Logs(ctx context.Context, id string) (io.ReadCloser, error)
	// Console returns a pty for the specified id
	Console(ctx context.Context, id string) (string, error)
	// Stop implements VM stop
	Stop(ctx context.Context, id string) error
	// Delete implements VM delete
	Delete(ctx context.Context, id, ip string) error
	// RecoverProcesses adopts running processes and cleans up stale metadata on startup
	RecoverProcesses(ctx context.Context) error
	// StartLogRotation starts background log rotation and returns a function to stop it
	StartLogRotation(ctx context.Context, interval time.Duration, maxBytes int64) func()
}

// NewVMM returns a new Virtual Machine Manager
func NewVMM(addr string, nm NetworkManager, enableHugepages bool, log *slog.Logger) (VMM, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(u.Scheme) {
	case "cloudhypervisor":
		return cloudhypervisor.NewVMM(addr, nm, enableHugepages, log)
	}

	return nil, fmt.Errorf("unsupported VMM %q", u.Scheme)
}
