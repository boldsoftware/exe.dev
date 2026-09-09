package execonnect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"
)

// DefaultExedURL is the default public exed endpoint used for enrollment.
const DefaultExedURL = "https://exe.dev"

const (
	linuxDefaultStateDir   = "/var/lib/execonnect"
	linuxDefaultSocketPath = "/run/execonnect/control.sock"
)

// DefaultStateDir returns the platform-resolved daemon state directory.
func DefaultStateDir() (string, error) {
	return defaultStateDir(runtime.GOOS, os.Geteuid, os.UserHomeDir, os.LookupEnv)
}

// DefaultSocketPath returns the platform-resolved local control socket path.
func DefaultSocketPath() (string, error) {
	return defaultSocketPath(runtime.GOOS, os.Geteuid, os.UserHomeDir, os.LookupEnv)
}

func defaultPaths(
	goos string,
	effectiveUID func() int,
	userHomeDir func() (string, error),
	lookupEnv func(string) (string, bool),
) (string, string, error) {
	stateDir, err := defaultStateDir(goos, effectiveUID, userHomeDir, lookupEnv)
	if err != nil {
		return "", "", err
	}
	socketPath, err := defaultSocketPath(goos, effectiveUID, userHomeDir, lookupEnv)
	if err != nil {
		return "", "", err
	}
	return stateDir, socketPath, nil
}

func defaultStateDir(
	goos string,
	effectiveUID func() int,
	userHomeDir func() (string, error),
	lookupEnv func(string) (string, bool),
) (string, error) {
	if goos == "darwin" {
		homeDir, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(homeDir, "Library", "Application Support", "execonnect"), nil
	}
	if goos != "linux" || effectiveUID() == 0 {
		return linuxDefaultStateDir, nil
	}
	if xdgStateHome, ok := lookupEnv("XDG_STATE_HOME"); ok && filepath.IsAbs(xdgStateHome) {
		return filepath.Join(xdgStateHome, "execonnect"), nil
	}
	homeDir, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if !filepath.IsAbs(homeDir) {
		return "", fmt.Errorf("home directory must be absolute")
	}
	return filepath.Join(homeDir, ".local", "state", "execonnect"), nil
}

func defaultSocketPath(
	goos string,
	effectiveUID func() int,
	userHomeDir func() (string, error),
	lookupEnv func(string) (string, bool),
) (string, error) {
	if goos == "darwin" {
		stateDir, err := defaultStateDir(goos, effectiveUID, userHomeDir, lookupEnv)
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, controlSocketName), nil
	}
	if goos != "linux" || effectiveUID() == 0 {
		return linuxDefaultSocketPath, nil
	}
	if xdgRuntimeDir, ok := lookupEnv("XDG_RUNTIME_DIR"); ok && filepath.IsAbs(xdgRuntimeDir) {
		return filepath.Join(xdgRuntimeDir, "execonnect", controlSocketName), nil
	}
	stateDir, err := defaultStateDir(goos, effectiveUID, userHomeDir, lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, controlSocketName), nil
}

// EnrollOptions configures one daemon enrollment operation.
type EnrollOptions struct {
	// StateDir explicitly selects the daemon state directory and, when SocketPath is empty, its control socket.
	StateDir string
	// SocketPath selects the daemon control socket.
	SocketPath string
	// ExedURL selects the exed HTTPS endpoint.
	ExedURL string
	// ExpectedOldID is the enrollment identity observed during preflight, or empty when unenrolled.
	ExpectedOldID string
	// ReadToken reads the one-time enrollment token after daemon and CAS preflight.
	ReadToken func() (string, error)
}

type connectorClient interface {
	connectapi.ExternalConnectionServiceClient
	io.Closer
}

// dialExedFunc creates one remote enrollment or connector client.
type dialExedFunc func(string) (connectorClient, error)

// Enroll preflights the local daemon, reads a token, and asks the daemon to enroll.
func Enroll(ctx context.Context, options EnrollOptions) (State, error) {
	socketCandidates, err := resolveClientSocketCandidates(options.StateDir, options.SocketPath)
	if err != nil {
		return State{}, err
	}
	statusResponse, socketPath, err := callClientControl(ctx, socketCandidates, "GET", "/status", nil)
	if err != nil {
		return State{}, err
	}
	if statusResponse.Status == nil {
		return State{}, fmt.Errorf("invalid control status response")
	}
	currentID := statusResponse.Status.ExternalConnectionID
	if currentID != options.ExpectedOldID {
		return State{}, enrollmentConflict(options.ExpectedOldID, currentID)
	}
	exedURL := options.ExedURL
	if exedURL == "" {
		exedURL = DefaultExedURL
	}
	exedURL, err = NormalizeExedURL(exedURL)
	if err != nil {
		return State{}, err
	}
	if options.ReadToken == nil {
		return State{}, fmt.Errorf("enrollment token reader is required")
	}
	token, err := options.ReadToken()
	if err != nil {
		return State{}, err
	}
	response, err := callControl(ctx, socketPath, "POST", "/enroll", controlEnrollRequest{
		ExpectedOldID: options.ExpectedOldID,
		ExedURL:       exedURL,
		Token:         token,
	})
	if err != nil {
		return State{}, err
	}
	if response.Enrollment == nil || response.Enrollment.ExternalConnectionID == "" {
		return State{}, fmt.Errorf("invalid control enrollment response")
	}
	return State{Enrolled: true, ExternalConnectionID: response.Enrollment.ExternalConnectionID}, nil
}

// EndpointOptions identifies an endpoint and its daemon socket.
type EndpointOptions struct {
	// StateDir explicitly selects the daemon socket when SocketPath is empty.
	StateDir string
	// SocketPath selects the daemon control socket.
	SocketPath string
	// Name is the endpoint's immutable durable key.
	Name string
	// URL is the endpoint's local target URL.
	URL string
	// TLSServerName overrides the derived HTTPS server name.
	TLSServerName string
}

// EndpointNameOptions identifies one named endpoint through the daemon.
type EndpointNameOptions struct {
	// StateDir explicitly selects the daemon socket when SocketPath is empty.
	StateDir string
	// SocketPath selects the daemon control socket.
	SocketPath string
	// Name is the endpoint's immutable durable key.
	Name string
}

// EndpointListOptions identifies a daemon endpoint registry.
type EndpointListOptions struct {
	// StateDir explicitly selects the daemon socket when SocketPath is empty.
	StateDir string
	// SocketPath selects the daemon control socket.
	SocketPath string
}

func AddEndpoint(ctx context.Context, options EndpointOptions) (EndpointRegistrySnapshot, bool, error) {
	return mutateEndpoint(ctx, options.StateDir, options.SocketPath, "/endpoints/add", controlEndpointRequest{
		Name: options.Name, URL: options.URL, TLSServerName: options.TLSServerName,
	})
}

func UpdateEndpoint(ctx context.Context, options EndpointOptions) (EndpointRegistrySnapshot, bool, error) {
	return mutateEndpoint(ctx, options.StateDir, options.SocketPath, "/endpoints/update", controlEndpointRequest{
		Name: options.Name, URL: options.URL, TLSServerName: options.TLSServerName,
	})
}

func RemoveEndpoint(ctx context.Context, options EndpointNameOptions) (EndpointRegistrySnapshot, error) {
	snapshot, _, err := mutateEndpoint(ctx, options.StateDir, options.SocketPath, "/endpoints/remove", controlEndpointRequest{Name: options.Name})
	return snapshot, err
}

func ListEndpoints(ctx context.Context, options EndpointListOptions) (EndpointRegistrySnapshot, error) {
	socketCandidates, err := resolveClientSocketCandidates(options.StateDir, options.SocketPath)
	if err != nil {
		return EndpointRegistrySnapshot{}, err
	}
	response, _, err := callClientControl(ctx, socketCandidates, "GET", "/endpoints", nil)
	if err != nil {
		return EndpointRegistrySnapshot{}, err
	}
	endpoints, err := endpointsFromRecords(response.Endpoints)
	if err != nil {
		return EndpointRegistrySnapshot{}, fmt.Errorf("invalid control response: %w", err)
	}
	return EndpointRegistrySnapshot{Endpoints: endpoints}, nil
}

func mutateEndpoint(ctx context.Context, configuredStateDir, configuredSocket, path string, request controlEndpointRequest) (EndpointRegistrySnapshot, bool, error) {
	socketCandidates, err := resolveClientSocketCandidates(configuredStateDir, configuredSocket)
	if err != nil {
		return EndpointRegistrySnapshot{}, false, err
	}
	response, _, err := callClientControl(ctx, socketCandidates, "POST", path, request)
	if err != nil {
		return EndpointRegistrySnapshot{}, false, err
	}
	endpoints, err := endpointsFromRecords(response.Endpoints)
	if err != nil {
		return EndpointRegistrySnapshot{}, false, fmt.Errorf("invalid control response: %w", err)
	}
	return EndpointRegistrySnapshot{Endpoints: endpoints}, response.Changed, nil
}

func resolveStateDir(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured, nil
	}
	return DefaultStateDir()
}

func resolveSocketPath(configuredStateDir, configuredSocket string) (string, error) {
	configuredSocket = strings.TrimSpace(configuredSocket)
	if configuredSocket != "" {
		if !filepath.IsAbs(configuredSocket) {
			return "", fmt.Errorf("control socket path must be absolute")
		}
		return configuredSocket, nil
	}
	configuredStateDir = strings.TrimSpace(configuredStateDir)
	if configuredStateDir != "" {
		if !filepath.IsAbs(configuredStateDir) {
			absolute, err := filepath.Abs(configuredStateDir)
			if err != nil {
				return "", fmt.Errorf("resolve state directory: %w", err)
			}
			configuredStateDir = absolute
		}
		return filepath.Join(configuredStateDir, controlSocketName), nil
	}
	return DefaultSocketPath()
}

func resolveClientSocketCandidates(configuredStateDir, configuredSocket string) ([]string, error) {
	return clientSocketCandidates(configuredStateDir, configuredSocket, runtime.GOOS, os.Geteuid, os.UserHomeDir, os.LookupEnv)
}

func clientSocketCandidates(
	configuredStateDir, configuredSocket, goos string,
	effectiveUID func() int,
	userHomeDir func() (string, error),
	lookupEnv func(string) (string, bool),
) ([]string, error) {
	if strings.TrimSpace(configuredSocket) != "" || strings.TrimSpace(configuredStateDir) != "" {
		socketPath, err := resolveSocketPath(configuredStateDir, configuredSocket)
		if err != nil {
			return nil, err
		}
		return []string{socketPath}, nil
	}

	uid := effectiveUID()
	fixedUID := func() int { return uid }
	userSocket, err := defaultSocketPath(goos, fixedUID, userHomeDir, lookupEnv)
	if goos != "linux" || uid == 0 {
		if err != nil {
			return nil, err
		}
		return []string{userSocket}, nil
	}

	candidates := make([]string, 0, 2)
	if err == nil {
		candidates = append(candidates, userSocket)
	}
	if len(candidates) == 0 || candidates[0] != linuxDefaultSocketPath {
		candidates = append(candidates, linuxDefaultSocketPath)
	}
	return candidates, nil
}

func callClientControl(ctx context.Context, socketCandidates []string, method, path string, requestValue any) (controlResponse, string, error) {
	for _, socketPath := range socketCandidates {
		response, err := callControl(ctx, socketPath, method, path, requestValue)
		switch {
		case err == nil:
			return response, socketPath, nil
		case !clientSocketUnavailable(err):
			return controlResponse{}, "", err
		}
	}
	quotedCandidates := make([]string, 0, len(socketCandidates))
	for _, socketPath := range socketCandidates {
		quotedCandidates = append(quotedCandidates, fmt.Sprintf("%q", socketPath))
	}
	return controlResponse{}, "", fmt.Errorf("%w; checked control sockets: %s", ErrServeNotRunning, strings.Join(quotedCandidates, ", "))
}

func clientSocketUnavailable(err error) bool {
	return controlUnavailable(err) || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOTSOCK)
}
