package execonnect

import (
	"context"
	"fmt"
)

// LocalStatus is the daemon's structured local service snapshot.
type LocalStatus struct {
	// Service is the daemon lifecycle state.
	Service string
	// Enrollment is enrolled or unenrolled.
	Enrollment string
	// Enrolled reports whether the daemon has an active durable identity.
	Enrolled bool
	// ExternalConnectionID identifies the enrolled External Connection.
	ExternalConnectionID string
	// ExedURL is the enrolled exed URL.
	ExedURL string
	// ServeRunning reports whether the daemon reported its running state.
	ServeRunning bool
	// EndpointCount is the number of pending or published endpoints.
	EndpointCount int
	// Control is the remote connector control-session state.
	Control string
	// Tunnel is the remote tunnel state.
	Tunnel string
	// Error is the latest connector-session error, if any.
	Error string
}

// StatusOptions identifies the local daemon socket to inspect.
type StatusOptions struct {
	// StateDir explicitly selects the daemon socket when SocketPath is empty.
	StateDir string
	// SocketPath selects the daemon control socket.
	SocketPath string
}

// InspectLocalStatus queries the daemon. It never reads daemon-owned state directly.

func InspectLocalStatus(ctx context.Context, options StatusOptions) (LocalStatus, error) {
	socketCandidates, err := resolveClientSocketCandidates(options.StateDir, options.SocketPath)
	if err != nil {
		return LocalStatus{}, err
	}
	response, _, err := callClientControl(ctx, socketCandidates, "GET", "/status", nil)
	if err != nil {
		return LocalStatus{}, err
	}
	if response.Status == nil || response.Status.Service == "" || response.Status.Enrollment == "" || response.Status.Control == "" || response.Status.Tunnel == "" {
		return LocalStatus{}, fmt.Errorf("invalid control status response")
	}
	return LocalStatus{
		Service:              response.Status.Service,
		Enrollment:           response.Status.Enrollment,
		Enrolled:             response.Status.Enrollment == "enrolled",
		ExternalConnectionID: response.Status.ExternalConnectionID,
		ExedURL:              response.Status.ExedURL,
		ServeRunning:         response.Status.Service == "running",
		EndpointCount:        response.Status.EndpointCount,
		Control:              response.Status.Control,
		Tunnel:               response.Status.Tunnel,
		Error:                response.Status.Error,
	}, nil
}
