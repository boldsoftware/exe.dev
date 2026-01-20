package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) State(ctx context.Context, id string) (api.VMState, error) {
	apiSocketPath := v.apiSocketPath(id)
	// Use retry=false for fast fail - if socket is unavailable, instance is stopped
	c, err := client.NewCloudHypervisorClient(ctx, apiSocketPath, false, v.log)
	if isStopped(err) {
		return api.VMState_STOPPED, nil
	}
	if err != nil {
		return api.VMState_UNKNOWN, err
	}
	defer c.Close()

	resp, err := c.GetVmInfoWithResponse(ctx)
	if isStopped(err) {
		return api.VMState_STOPPED, nil
	}
	if err != nil {
		return api.VMState_UNKNOWN, err
	}

	// wait until populated
	if resp.JSON200 == nil {
		// the instance is created but not fully ready
		return api.VMState_STARTING, err
	}

	switch resp.JSON200.State {
	case client.Created:
		// CloudHypervisor will return stopped instances as "Created".
		// We expect this state to be "Starting". Since we always
		// start the VM as part of the instantiation we return
		// CloudHypervisor's "Created" state as our "Stopped" state since
		// we know this has either started or errored as part of the init.
		return api.VMState_STARTING, nil
	case client.Paused:
		return api.VMState_PAUSED, nil
	case client.Running:
		return api.VMState_RUNNING, nil
	case client.Shutdown:
		return api.VMState_STOPPING, nil
	}
	return api.VMState_UNKNOWN, fmt.Errorf("unknown state: %+v", resp.JSON200.State)
}

// isStopped reports whether err indicates that a VM is stopped.
// This includes connection errors, EOF errors, and socket not found errors.
func isStopped(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, client.ErrNotConnected) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		// When the VMM is stopped it removes the socket, producing a file not found error.
		errors.Is(err, fs.ErrNotExist) {
		return true
	}
	// The HTTP client may wrap errors in ways that errors.Is doesn't catch.
	errStr := err.Error()
	// TODO: should this check textual equivalents of all the errors.Is checks above?
	if strings.Contains(errStr, "EOF") || strings.Contains(errStr, "connection reset") {
		return true
	}
	return false
}
