package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) State(ctx context.Context, id string) (api.VMState, error) {
	apiSocketPath := v.apiSocketPath(id)
	// Use retry=false for fast fail - if socket is unavailable, instance is stopped
	c, err := client.NewCloudHypervisorClient(ctx, apiSocketPath, false, v.log)
	// check if not connected from client
	if errors.Is(err, client.ErrNotConnected) {
		return api.VMState_STOPPED, nil
	}
	if err != nil {
		return api.VMState_UNKNOWN, err
	}
	defer c.Close()

	resp, err := c.GetVmInfoWithResponse(ctx)
	// Check for EOF errors first (most common case when VMM crashes)
	// These can be wrapped in various ways by the HTTP client
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		return api.VMState_STOPPED, nil
	}
	if err != nil {
		// Check error string for EOF-related messages
		// The HTTP client may wrap these in ways that errors.Is doesn't catch
		errStr := err.Error()
		if errStr == "EOF" || errStr == "unexpected EOF" ||
			strings.Contains(errStr, "EOF") || strings.Contains(errStr, "connection reset") {
			return api.VMState_STOPPED, nil
		}
		// check for connect error and assume stopped if missing
		if isNotConnected(err) {
			return api.VMState_STOPPED, nil
		}
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

// when the VMM is stopped it removes the socket which will produce
// a file not found error
func isNotConnected(err error) bool {
	if errors.Is(err, client.ErrNotConnected) {
		return true
	}
	// check for connect error and assume stopped if missing
	uErr, ok := err.(*url.Error)
	if ok {
		// unwrap the OpError
		nErr, nOk := uErr.Err.(*net.OpError)
		if nOk {
			// when the VMM is stopped it removes the socket which will produce
			// a file not found error
			if os.IsNotExist(nErr.Unwrap()) {
				return true
			}
		}
	}
	// EOF errors indicate the connection was closed (VMM likely shut down)
	// Treat as already stopped to avoid spurious errors under load
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Check for EOF in error string (some wrapped EOF errors)
	errStr := err.Error()
	if errStr == "EOF" || errStr == "unexpected EOF" {
		return true
	}
	return false
}
