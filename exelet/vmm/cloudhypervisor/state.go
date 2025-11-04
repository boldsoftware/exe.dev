package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (v *VMM) State(ctx context.Context, id string) (api.VMState, error) {
	apiSocketPath := v.apiSocketPath(id)
	c, err := client.NewCloudHypervisorClient(apiSocketPath, v.log)
	if err != nil {
		// check if not connected from client
		if errors.Is(err, client.ErrNotConnected) {
			return api.VMState_STOPPED, nil
		}
		return api.VMState_UNKNOWN, err
	}
	defer c.Close()

	resp, err := c.GetVmInfoWithResponse(ctx)
	if err != nil {
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
	return false
}
