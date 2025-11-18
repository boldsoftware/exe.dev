package compute

import (
	"context"
	"log/slog"
	"os"

	"exe.dev/exelet/services"
	"exe.dev/pkg/tcpproxy"
)

// createInstanceRollback tracks resources created during instance creation for cleanup on error
type createInstanceRollback struct {
	ctx                context.Context
	serviceContext     *services.ServiceContext
	log                *slog.Logger
	instanceID         string
	instanceDir        string
	imageFSID          string
	networkCreated     bool
	instanceDirCreated bool
	imageFSCreated     bool
	imageFSMounted     bool
	instanceCloned     bool
	instanceMounted    bool
	proxyManager       *tcpproxy.ProxyManager
	portAllocator      *PortAllocator
	proxyCreated       bool
	allocatedPort      int
}

// Rollback cleans up resources in reverse order of creation
func (r *createInstanceRollback) Rollback() {
	// Stop and remove proxy if created
	if r.proxyCreated && r.proxyManager != nil {
		if port, err := r.proxyManager.RemoveProxy(r.instanceID); err != nil {
			r.log.Error("rollback: failed to remove proxy", "id", r.instanceID, "error", err)
		} else if r.portAllocator != nil {
			r.portAllocator.Release(port)
		}
	}

	// Unmount instance filesystem if mounted
	if r.instanceMounted {
		if err := r.serviceContext.StorageManager.Unmount(r.ctx, r.instanceID); err != nil {
			r.log.Error("rollback: failed to unmount instance", "id", r.instanceID, "error", err)
		}
	}

	// Unmount image filesystem if mounted
	if r.imageFSMounted {
		if err := r.serviceContext.StorageManager.Unmount(r.ctx, r.imageFSID); err != nil {
			r.log.Error("rollback: failed to unmount image filesystem", "id", r.imageFSID, "error", err)
		}
	}

	// Delete image filesystem if we created it
	if r.imageFSCreated {
		if err := r.serviceContext.StorageManager.Delete(r.ctx, r.imageFSID); err != nil {
			r.log.Error("rollback: failed to delete image filesystem", "id", r.imageFSID, "error", err)
		}
	}

	// Delete network interface
	if r.networkCreated {
		if err := r.serviceContext.NetworkManager.DeleteInterface(r.ctx, r.instanceID); err != nil {
			r.log.Error("rollback: failed to delete network interface", "id", r.instanceID, "error", err)
		}
	}

	// Remove instance directory
	if r.instanceDirCreated {
		if err := os.RemoveAll(r.instanceDir); err != nil {
			r.log.Error("rollback: failed to remove instance directory", "dir", r.instanceDir, "error", err)
		}
	}
}
