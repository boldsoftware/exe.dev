package compute

import (
	"context"
	"log/slog"
	"os"

	"exe.dev/exelet/services"
	"exe.dev/exelet/sshproxy"
	"exe.dev/exelet/vmm"
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
	proxyManager       *sshproxy.Manager
	portAllocator      *PortAllocator
	proxyCreated       bool
	allocatedPort      int
	vmCreated          bool
	vmStarted          bool
	runtimeAddress     string
	networkIP          string
}

// Rollback cleans up resources in reverse order of creation
func (r *createInstanceRollback) Rollback() {
	// Stop and remove proxy if created
	if r.proxyCreated && r.proxyManager != nil {
		if _, err := r.proxyManager.StopProxy(r.instanceID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to remove proxy", "id", r.instanceID, "error", err)
		}
	}

	// Always release allocated port if we allocated one
	// This ensures port is freed even if proxy creation or stopping failed
	if r.allocatedPort > 0 && r.portAllocator != nil {
		r.portAllocator.Release(r.allocatedPort)
		r.log.DebugContext(r.ctx, "rollback: released allocated port", "port", r.allocatedPort)
	}

	// Stop and delete VM if created
	if r.vmStarted || r.vmCreated {
		if r.runtimeAddress != "" && r.serviceContext != nil {
			v, err := vmm.NewVMM(r.runtimeAddress, r.serviceContext.NetworkManager, r.log)
			if err != nil {
				r.log.ErrorContext(r.ctx, "rollback: failed to create VMM client", "error", err)
			} else {
				// Stop VM (ignore error if already stopped)
				if r.vmStarted {
					if err := v.Stop(r.ctx, r.instanceID); err != nil {
						r.log.WarnContext(r.ctx, "rollback: failed to stop VM", "id", r.instanceID, "error", err)
					} else {
						r.log.DebugContext(r.ctx, "rollback: stopped VM", "id", r.instanceID)
					}
				}

				// Delete VM
				if r.vmCreated {
					if err := v.Delete(r.ctx, r.instanceID, r.networkIP); err != nil {
						r.log.ErrorContext(r.ctx, "rollback: failed to delete VM", "id", r.instanceID, "error", err)
					} else {
						r.log.DebugContext(r.ctx, "rollback: deleted VM", "id", r.instanceID)
					}
				}
			}
		}
	}

	// Unmount instance filesystem if mounted
	if r.instanceMounted {
		if err := r.serviceContext.StorageManager.Unmount(r.ctx, r.instanceID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to unmount instance", "id", r.instanceID, "error", err)
		}
	}

	// Delete cloned instance storage if created
	if r.instanceCloned {
		if err := r.serviceContext.StorageManager.Delete(r.ctx, r.instanceID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to delete instance storage", "id", r.instanceID, "error", err)
		}
	}

	// Unmount image filesystem if mounted
	if r.imageFSMounted {
		if err := r.serviceContext.StorageManager.Unmount(r.ctx, r.imageFSID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to unmount image filesystem", "id", r.imageFSID, "error", err)
		}
	}

	// Delete image filesystem if we created it
	if r.imageFSCreated {
		if err := r.serviceContext.StorageManager.Delete(r.ctx, r.imageFSID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to delete image filesystem", "id", r.imageFSID, "error", err)
		}
	}

	// Delete network interface
	if r.networkCreated {
		// Pass empty IP since we may not have allocated one yet during rollback
		if err := r.serviceContext.NetworkManager.DeleteInterface(r.ctx, r.instanceID, ""); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to delete network interface", "id", r.instanceID, "error", err)
		}
	}

	// Remove instance directory
	if r.instanceDirCreated {
		if err := os.RemoveAll(r.instanceDir); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to remove instance directory", "dir", r.instanceDir, "error", err)
		}
	}
}
