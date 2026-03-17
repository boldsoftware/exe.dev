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
	// Stop implements VM stop (hard kill)
	Stop(ctx context.Context, id string) error
	// Delete implements VM delete
	Delete(ctx context.Context, id, ip string) error
	// DeflateBalloon resets the balloon to size 0, forcing all memory back into the guest.
	// This should be called before snapshotting to ensure all memory regions are mapped.
	DeflateBalloon(ctx context.Context, id string) error
	// Pause pauses a running VM
	Pause(ctx context.Context, id string) error
	// Resume resumes a paused VM
	Resume(ctx context.Context, id string) error
	// Snapshot creates a CH snapshot of a paused VM to the given directory
	Snapshot(ctx context.Context, id, destDir string) error
	// RestoreFromSnapshot starts a new CH process and restores a VM from a snapshot directory.
	// The restored VM is resumed automatically.
	RestoreFromSnapshot(ctx context.Context, id, snapshotDir string) error
	// ResizeDisk notifies the VMM that a disk has been resized
	ResizeDisk(ctx context.Context, id, diskID string, newSize uint64) error
	// RecoverProcesses adopts running processes and cleans up stale metadata on startup
	RecoverProcesses(ctx context.Context) error
	// StartLogRotation starts background log rotation and returns a function to stop it
	StartLogRotation(ctx context.Context, interval time.Duration, maxBytes, keepBytes int64) func()
}

// NewVMM returns a new Virtual Machine Manager
func NewVMM(addr string, nm NetworkManager, enableHugepages bool, instanceDomain string, log *slog.Logger) (VMM, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(u.Scheme) {
	case "cloudhypervisor":
		return cloudhypervisor.NewVMM(addr, nm, enableHugepages, instanceDomain, log)
	}

	return nil, fmt.Errorf("unsupported VMM %q", u.Scheme)
}
