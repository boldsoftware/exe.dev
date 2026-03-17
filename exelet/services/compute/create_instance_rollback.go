package compute

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"exe.dev/exelet/services"
	"exe.dev/exelet/sshproxy"
	"exe.dev/exelet/vmm"
)

// createInstanceRollback tracks resources created during instance creation for cleanup on error
type createInstanceRollback struct {
	ctx                context.Context
	serviceContext     *services.ServiceContext
	log                *slog.Logger
	vmm                vmm.VMM
	instanceID         string
	instanceDir        string
	imageFSID          string
	networkCreated     bool
	instanceDirCreated bool
	imageFSCreated     bool
	imageFSMounted     bool
	instanceCloned     bool
	instanceMounted    bool
	proxyManager       sshproxy.Manager
	portAllocator      *PortAllocator
	proxyCreated       bool
	allocatedPort      int
	vmCreated          bool
	vmStarted          bool
	networkIP          string
}

// EnhanceErrorWithBootLog reads the VM boot log and appends it to the error for debugging.
// Returns the enhanced error, or the original error if boot log cannot be read.
func (r *createInstanceRollback) EnhanceErrorWithBootLog(err error) error {
	if r.vmm == nil {
		return err
	}

	logReader, logErr := r.vmm.Logs(r.ctx, r.instanceID)
	if logErr != nil {
		r.log.WarnContext(r.ctx, "failed to read instance boot log", "id", r.instanceID, "error", logErr)
		return err
	}

	logData, readErr := io.ReadAll(io.LimitReader(logReader, 4096))
	logReader.Close()
	if readErr != nil {
		r.log.WarnContext(r.ctx, "failed to read instance boot log data", "id", r.instanceID, "error", readErr)
		return err
	}

	if len(logData) > 0 {
		bootLog := string(logData)
		r.log.WarnContext(r.ctx, "instance boot log", "id", r.instanceID, "log", bootLog)
		// Append boot log to message and add as DebugInfo detail for structured access
		if st, ok := status.FromError(err); ok {
			newSt := status.New(st.Code(), fmt.Sprintf("%s; boot log: %s", st.Message(), bootLog))
			// Re-attach existing details and add boot log as DebugInfo
			details := st.Details()
			debugInfo := &errdetails.DebugInfo{Detail: bootLog}
			if len(details) > 0 {
				// Copy existing details to new status
				debugAny, anyErr := anypb.New(debugInfo)
				if anyErr != nil {
					// Fall back to status with just the message if marshal fails
					return newSt.Err()
				}
				protoDetails := st.Proto().Details
				protoDetails = append(protoDetails, debugAny)
				newProto := newSt.Proto()
				newProto.Details = protoDetails
				return status.FromProto(newProto).Err()
			}
			if withLog, detailErr := newSt.WithDetails(debugInfo); detailErr == nil {
				return withLog.Err()
			}
			return newSt.Err()
		}
		return fmt.Errorf("%w; boot log: %s", err, bootLog)
	}

	return err
}

// Rollback cleans up resources in reverse order of creation
func (r *createInstanceRollback) Rollback() {
	// Stop and remove proxy if created
	if r.proxyCreated && r.proxyManager != nil {
		if _, err := r.proxyManager.StopProxy(r.ctx, r.instanceID); err != nil {
			r.log.ErrorContext(r.ctx, "rollback: failed to remove proxy", "id", r.instanceID, "error", err)
		}
	}

	// Always release allocated port if we allocated one
	// This ensures port is freed even if proxy creation or stopping failed
	if r.allocatedPort > 0 && r.portAllocator != nil {
		r.portAllocator.Release(r.allocatedPort)
		r.log.DebugContext(r.ctx, "rollback: released allocated port", "port", r.allocatedPort)
	}

	// Parse IP from CIDR format once for use in cleanup (e.g., "10.42.1.5/16" -> "10.42.1.5")
	bareIP := ""
	if r.networkIP != "" {
		if parsedIP, _, err := net.ParseCIDR(r.networkIP); err == nil {
			bareIP = parsedIP.String()
		}
	}

	// Stop and delete VM if created
	if r.vmStarted || r.vmCreated {
		if r.vmm != nil {
			// Stop VM (ignore error if already stopped)
			if r.vmStarted {
				if err := r.vmm.Stop(r.ctx, r.instanceID); err != nil {
					r.log.WarnContext(r.ctx, "rollback: failed to stop VM", "id", r.instanceID, "error", err)
				} else {
					r.log.DebugContext(r.ctx, "rollback: stopped VM", "id", r.instanceID)
				}
			}

			// Delete VM (vmm.Delete also cleans up network interface)
			if r.vmCreated {
				if err := r.vmm.Delete(r.ctx, r.instanceID, bareIP); err != nil {
					r.log.ErrorContext(r.ctx, "rollback: failed to delete VM", "id", r.instanceID, "error", err)
				} else {
					r.log.DebugContext(r.ctx, "rollback: deleted VM", "id", r.instanceID)
					r.networkCreated = false // vmm.Delete already cleaned up network
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

	// Delete network interface (if not already cleaned up by vmm.Delete)
	if r.networkCreated {
		if err := r.serviceContext.NetworkManager.DeleteInterface(r.ctx, r.instanceID, bareIP); err != nil {
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
