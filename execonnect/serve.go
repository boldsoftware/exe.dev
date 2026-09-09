package execonnect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"tailscale.com/types/key"
)

// ServeEventKind identifies one foreground daemon lifecycle event.
type ServeEventKind string

const (
	ServeStarted                 ServeEventKind = "started"
	ServeEndpointRegistryUpdated ServeEventKind = "endpoint_registry_updated"
	ServeTunnelConfigured        ServeEventKind = "tunnel_configured"
	ServeControlConnected        ServeEventKind = "control_connected"
	ServeControlDisconnected     ServeEventKind = "control_disconnected"
	ServeControlRetrying         ServeEventKind = "control_retrying"
	ServeEndpointSnapshotSent    ServeEventKind = "endpoint_snapshot_sent"
	ServeOutboundConnected       ServeEventKind = "outbound_connected"
	ServeFlowError               ServeEventKind = "flow_error"
	ServeShuttingDown            ServeEventKind = "shutting_down"
	ServeStopped                 ServeEventKind = "stopped"
)

// ServeEvent describes one foreground daemon lifecycle event.
type ServeEvent struct {
	// Kind identifies the lifecycle transition.
	Kind ServeEventKind
	// ExternalConnectionID identifies the active enrollment for ServeStarted.
	ExternalConnectionID string
	// EndpointCount is populated for endpoint lifecycle events.
	EndpointCount int
	// Attempt is populated for control connection and retry events.
	Attempt int
	// Delay is populated for control retry events.
	Delay time.Duration
	// Err is populated for control, flow, and shutdown errors.
	Err error
	// Terminal reports whether a control disconnection ended its connector session.
	Terminal bool
	// Target identifies a successfully connected local target.
	Target string
}

// ServeOptions configures one foreground daemon run.
type ServeOptions struct {
	// StateDir selects the daemon state directory. An empty value uses the platform default.
	StateDir string
	// SocketPath selects the local control socket. When omitted with an explicit StateDir,
	// the socket is placed in StateDir; otherwise the system runtime path is used.
	SocketPath string
	// OnEvent receives foreground daemon lifecycle events synchronously.
	OnEvent func(ServeEvent)
}

// ServeControlError reports a terminal failure of one exed control session.
type ServeControlError struct{ Err error }

func (err *ServeControlError) Error() string { return "control stream stopped: " + err.Err.Error() }
func (err *ServeControlError) Unwrap() error { return err.Err }

type enrollmentIdentity struct {
	externalConnectionID string
	connectorSecret      string
}

type daemonSession interface {
	Stop() error
	Publish(EndpointRegistrySnapshot)
	Done() <-chan struct{}
	Err() error
}

type daemonDependencies struct {
	persist      func(string, State) error
	enrollRemote func(context.Context, string, string) (enrollmentIdentity, error)
	startSession func(context.Context, State, *EndpointRegistry, func(ServeEvent)) daemonSession
}

type endpointMutationError struct {
	kind string
	err  error
}

func (err *endpointMutationError) Error() string { return err.err.Error() }
func (err *endpointMutationError) Unwrap() error { return err.err }

func mutationError(kind string, err error) error {
	return &endpointMutationError{kind: kind, err: err}
}

type daemon struct {
	ctx       context.Context
	nonce     string
	statePath string
	onEvent   func(ServeEvent)
	deps      daemonDependencies

	enrollMu   sync.Mutex
	mu         sync.Mutex
	ready      bool
	state      State
	registry   *EndpointRegistry
	session    daemonSession
	generation uint64
	service    string
	control    string
	tunnel     string
	lastError  string
}

func newDaemon(ctx context.Context, nonce, statePath string, onEvent func(ServeEvent), deps daemonDependencies) *daemon {
	return &daemon{
		ctx:       ctx,
		nonce:     nonce,
		statePath: statePath,
		onEvent:   onEvent,
		deps:      deps,
		service:   "starting",
		control:   "inactive",
		tunnel:    "inactive",
	}
}

func defaultDaemonDependencies(dialExed dialExedFunc) daemonDependencies {
	return daemonDependencies{
		persist: writeState,
		enrollRemote: func(ctx context.Context, exedURL, token string) (enrollmentIdentity, error) {
			if dialExed == nil {
				return enrollmentIdentity{}, fmt.Errorf("nil exed dialer")
			}
			client, err := dialExed(exedURL)
			if err != nil {
				return enrollmentIdentity{}, err
			}
			if client == nil {
				return enrollmentIdentity{}, fmt.Errorf("exed dialer returned a nil client")
			}
			response, enrollErr := client.Enroll(ctx, &connectapi.EnrollRequest{EnrollmentToken: token})
			closeErr := client.Close()
			if enrollErr != nil {
				if closeErr != nil {
					closeErr = fmt.Errorf("close exed enrollment connection: %w", closeErr)
				}
				return enrollmentIdentity{}, errors.Join(fmt.Errorf("enroll External Connection: %w", enrollErr), closeErr)
			}
			identity := enrollmentIdentity{
				externalConnectionID: response.GetExternalConnectionID(),
				connectorSecret:      response.GetConnectorSecret(),
			}
			if err := (Credentials{ExternalConnectionID: identity.externalConnectionID, ConnectorSecret: identity.connectorSecret}).validate(); err != nil {
				return enrollmentIdentity{}, fmt.Errorf("invalid enrollment response: %w", err)
			}
			return identity, nil
		},
		startSession: func(ctx context.Context, state State, registry *EndpointRegistry, onEvent func(ServeEvent)) daemonSession {
			return startConnectorSession(ctx, state, registry, onEvent, dialExed)
		},
	}
}

// Serve runs the persistent local daemon until ctx is canceled or its local server fails.
func Serve(ctx context.Context, options ServeOptions) error {
	return serve(ctx, options, func(rawURL string) (connectorClient, error) {
		return DialExed(rawURL)
	})
}

func serve(ctx context.Context, options ServeOptions, dialExed dialExedFunc) (returnedErr error) {
	stateDir, err := resolveStateDir(options.StateDir)
	if err != nil {
		return err
	}
	socketPath, err := resolveSocketPath(options.StateDir, options.SocketPath)
	if err != nil {
		return err
	}
	socket, err := listenControlSocket(socketPath)
	if err != nil {
		return err
	}

	nonce, err := newInstanceNonce()
	if err != nil {
		_ = socket.close(false)
		return err
	}
	daemonContext, cancelDaemon := context.WithCancel(ctx)
	defer cancelDaemon()
	manager := newDaemon(daemonContext, nonce, filepath.Join(stateDir, "state.json"), options.OnEvent, defaultDaemonDependencies(dialExed))
	controlResult := make(chan error, 1)
	go func() {
		controlResult <- runControlServer(daemonContext, socket.listener, newLocalControlServer(manager))
	}()

	verifyContext, cancelVerify := context.WithTimeout(ctx, 3*time.Second)
	response, err := callControl(verifyContext, socketPath, "GET", "/status", nil)
	cancelVerify()
	if err != nil || response.Status == nil || response.Status.InstanceNonce != nonce {
		cancelDaemon()
		controlErr := <-controlResult
		closeErr := socket.close(false)
		if err == nil {
			err = fmt.Errorf("control socket self-verification reached another process")
		}
		return errors.Join(fmt.Errorf("verify control socket ownership: %w", err), controlErr, closeErr)
	}
	if err := manager.initialize(); err != nil {
		cancelDaemon()
		controlErr := <-controlResult
		return errors.Join(err, controlErr, socket.Close())
	}

	manager.mu.Lock()
	startedState := manager.state
	manager.mu.Unlock()
	reportServeEvent(options.OnEvent, ServeEvent{
		Kind:                 ServeStarted,
		ExternalConnectionID: startedState.ExternalConnectionID,
		EndpointCount:        len(startedState.Endpoints),
	})

	var runErr error
	controlFinished := false
	select {
	case <-ctx.Done():
		reportServeEvent(options.OnEvent, ServeEvent{Kind: ServeShuttingDown})
	case runErr = <-controlResult:
		controlFinished = true
		cancelDaemon()
	}
	shutdownErr := manager.shutdown()
	cancelDaemon()
	if !controlFinished {
		runErr = <-controlResult
	}
	closeErr := socket.Close()
	if ctx.Err() == nil && runErr == nil && shutdownErr == nil && closeErr == nil {
		reportServeEvent(options.OnEvent, ServeEvent{Kind: ServeStopped})
	}
	return errors.Join(runErr, shutdownErr, closeErr)
}

func newInstanceNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate daemon instance nonce: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}

func (daemon *daemon) initialize() error {
	state, err := loadOrCreateState(daemon.statePath)
	if err != nil {
		return err
	}
	snapshot, err := state.endpointSnapshot()
	if err != nil {
		return err
	}
	registry, err := NewEndpointRegistry(snapshot.Endpoints)
	if err != nil {
		return err
	}

	daemon.enrollMu.Lock()
	defer daemon.enrollMu.Unlock()
	daemon.mu.Lock()
	daemon.state = state
	daemon.registry = registry
	daemon.service = "running"
	if state.Enrolled {
		daemon.control = "connecting"
	} else {
		daemon.control = "inactive"
	}
	daemon.mu.Unlock()
	if state.Enrolled {
		daemon.launchSessionLocked()
	}
	daemon.mu.Lock()
	daemon.ready = true
	daemon.mu.Unlock()
	return nil
}

func (daemon *daemon) status() *controlStatus {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	enrollment := "unenrolled"
	if daemon.state.Enrolled {
		enrollment = "enrolled"
	}
	return &controlStatus{
		InstanceNonce:        daemon.nonce,
		Service:              daemon.service,
		Enrollment:           enrollment,
		ExternalConnectionID: daemon.state.ExternalConnectionID,
		ExedURL:              daemon.state.ExedURL,
		EndpointCount:        len(daemon.state.Endpoints),
		Control:              daemon.control,
		Tunnel:               daemon.tunnel,
		Error:                daemon.lastError,
	}
}

func (daemon *daemon) listEndpoints() (EndpointRegistrySnapshot, error) {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if !daemon.ready {
		return EndpointRegistrySnapshot{}, fmt.Errorf("daemon is still starting")
	}
	return daemon.state.endpointSnapshot()
}

func (daemon *daemon) mutateEndpoint(operation string, input controlEndpointRequest) (EndpointRegistrySnapshot, bool, error) {
	daemon.mu.Lock()
	if !daemon.ready {
		daemon.mu.Unlock()
		return EndpointRegistrySnapshot{}, false, mutationError("daemon_starting", fmt.Errorf("daemon is still starting"))
	}
	current, err := daemon.state.endpointSnapshot()
	if err != nil {
		daemon.mu.Unlock()
		return EndpointRegistrySnapshot{}, false, mutationError("internal", err)
	}
	next := EndpointRegistrySnapshot{}
	changed := true
	switch operation {
	case "add":
		next, changed, err = addEndpoint(current, input.Name, input.URL, input.TLSServerName)
	case "update":
		next, changed, err = updateEndpoint(current, input.Name, input.URL, input.TLSServerName)
	case "remove":
		next, err = removeEndpoint(current, input.Name)
	default:
		err = fmt.Errorf("unknown endpoint mutation %q", operation)
	}
	if err != nil || !changed {
		daemon.mu.Unlock()
		if err != nil {
			err = mutationError("endpoint_invalid", err)
		}
		return current, changed, err
	}
	nextState, err := daemon.state.withEndpointSnapshot(next)
	if err == nil {
		err = daemon.deps.persist(daemon.statePath, nextState)
	}
	if err != nil {
		daemon.mu.Unlock()
		return EndpointRegistrySnapshot{}, false, mutationError("state_write_failed", err)
	}
	replaceErr := daemon.registry.Replace(next.Endpoints)
	daemon.state = nextState
	if daemon.session != nil {
		daemon.session.Publish(next)
	}
	daemon.mu.Unlock()

	reportServeEvent(daemon.onEvent, ServeEvent{Kind: ServeEndpointRegistryUpdated, EndpointCount: len(next.Endpoints)})
	if replaceErr != nil {
		return next, true, mutationError("flow_revocation_failed", fmt.Errorf("endpoint change committed but flow revocation failed: %w", replaceErr))
	}
	return next, true, nil
}

func (daemon *daemon) enroll(ctx context.Context, request controlEnrollRequest) (State, error) {
	daemon.enrollMu.Lock()
	defer daemon.enrollMu.Unlock()

	exedURL, err := NormalizeExedURL(request.ExedURL)
	if err != nil {
		return State{}, err
	}
	request.Token = strings.TrimSpace(request.Token)
	if request.Token == "" {
		return State{}, fmt.Errorf("enrollment token is required")
	}
	daemon.mu.Lock()
	if !daemon.ready {
		daemon.mu.Unlock()
		return State{}, fmt.Errorf("daemon is still starting")
	}
	currentID := daemon.state.ExternalConnectionID
	if currentID != request.ExpectedOldID {
		daemon.mu.Unlock()
		return State{}, enrollmentConflict(request.ExpectedOldID, currentID)
	}
	if err := daemon.deps.persist(daemon.statePath, daemon.state); err != nil {
		daemon.mu.Unlock()
		return State{}, fmt.Errorf("preflight connector state persistence: %w", err)
	}
	daemon.mu.Unlock()

	identity, err := daemon.deps.enrollRemote(ctx, exedURL, request.Token)
	if err != nil {
		return State{}, err
	}

	daemon.mu.Lock()
	currentID = daemon.state.ExternalConnectionID
	if currentID != request.ExpectedOldID {
		daemon.mu.Unlock()
		return State{}, enrollmentConflict(request.ExpectedOldID, currentID)
	}
	newState := daemon.state
	newState.Enrolled = true
	newState.ExternalConnectionID = identity.externalConnectionID
	newState.ConnectorSecret = identity.connectorSecret
	newState.WireGuardPrivateKey = key.NewNode()
	newState.ExedURL = exedURL
	if err := daemon.deps.persist(daemon.statePath, newState); err != nil {
		daemon.mu.Unlock()
		return State{}, fmt.Errorf("persist connector state: %w", err)
	}
	oldSession := daemon.session
	daemon.session = nil
	daemon.generation++
	daemon.state = newState
	daemon.control = "connecting"
	daemon.tunnel = "inactive"
	daemon.lastError = ""
	daemon.mu.Unlock()

	var stopErr error
	if oldSession != nil {
		stopErr = oldSession.Stop()
	}
	daemon.launchSessionLocked()
	if stopErr != nil {
		return newState, fmt.Errorf("stop previous connector session: %w", stopErr)
	}
	return newState, nil
}

func enrollmentConflict(expected, current string) error {
	return &enrollmentConflictError{
		currentID: current,
		message:   fmt.Sprintf("enrollment changed concurrently: expected %q, current %q; retry with the current status", expected, current),
	}
}

func (daemon *daemon) launchSessionLocked() {
	daemon.mu.Lock()
	daemon.generation++
	generation := daemon.generation
	daemon.control = "connecting"
	daemon.tunnel = "inactive"
	daemon.lastError = ""
	state := daemon.state
	registry := daemon.registry
	session := daemon.deps.startSession(daemon.ctx, state, registry, func(event ServeEvent) {
		daemon.handleSessionEvent(generation, event)
	})
	daemon.session = session
	daemon.mu.Unlock()
	go daemon.watchSession(generation, session)
}

func (daemon *daemon) handleSessionEvent(generation uint64, event ServeEvent) {
	daemon.mu.Lock()
	if daemon.generation != generation {
		daemon.mu.Unlock()
		return
	}
	switch event.Kind {
	case ServeControlConnected:
		daemon.control = "connected"
		daemon.lastError = ""
	case ServeControlRetrying:
		daemon.control = "retrying"
		daemon.lastError = errorText(event.Err)
	case ServeControlDisconnected:
		if event.Terminal {
			daemon.control = "error"
			daemon.tunnel = "inactive"
		} else {
			daemon.control = "disconnected"
		}
		if event.Err != nil {
			daemon.lastError = errorText(event.Err)
		}
	case ServeTunnelConfigured:
		daemon.tunnel = "configured"
	}
	daemon.mu.Unlock()
	reportServeEvent(daemon.onEvent, event)
}

func (daemon *daemon) watchSession(generation uint64, session daemonSession) {
	<-session.Done()
	err := session.Err()
	daemon.mu.Lock()
	if daemon.generation == generation && daemon.session == session {
		daemon.session = nil
		daemon.control = "error"
		daemon.tunnel = "inactive"
		daemon.lastError = errorText(err)
	}
	daemon.mu.Unlock()
}

func (daemon *daemon) shutdown() error {
	daemon.enrollMu.Lock()
	defer daemon.enrollMu.Unlock()
	daemon.mu.Lock()
	daemon.ready = false
	daemon.service = "stopping"
	daemon.generation++
	session := daemon.session
	daemon.session = nil
	daemon.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Stop()
}

func errorText(err error) string {
	if err == nil {
		return "connector session stopped"
	}
	return err.Error()
}

type connectorSession struct {
	cancel  context.CancelFunc
	updates chan EndpointUpdate
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func startConnectorSession(ctx context.Context, state State, registry *EndpointRegistry, onEvent func(ServeEvent), dialExed dialExedFunc) *connectorSession {
	sessionContext, cancel := context.WithCancel(ctx)
	session := &connectorSession{
		cancel:  cancel,
		updates: make(chan EndpointUpdate, 1),
		done:    make(chan struct{}),
	}
	snapshot, _ := state.endpointSnapshot()
	publishEndpointUpdate(session.updates, snapshot)
	go func() {
		err := runConnectorSession(sessionContext, state, registry, session.updates, onEvent, dialExed)
		session.mu.Lock()
		session.err = err
		session.mu.Unlock()
		close(session.done)
	}()
	return session
}

func (session *connectorSession) Stop() error {
	session.cancel()
	<-session.done
	return session.Err()
}

func (session *connectorSession) Publish(snapshot EndpointRegistrySnapshot) {
	publishEndpointUpdate(session.updates, snapshot)
}

func (session *connectorSession) Done() <-chan struct{} { return session.done }
func (session *connectorSession) Err() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.err
}

func runConnectorSession(
	ctx context.Context,
	state State,
	registry *EndpointRegistry,
	endpointUpdates chan EndpointUpdate,
	onEvent func(ServeEvent),
	dialExed dialExedFunc,
) (returnedErr error) {
	publicKey, err := state.WireGuardPrivateKey.Public().MarshalText()
	if err != nil {
		return fmt.Errorf("encode WireGuard public key: %w", err)
	}
	wireGuardTunnel, err := NewWireGuardTunnel(state.WireGuardPrivateKey)
	if err != nil {
		return err
	}
	defer wireGuardTunnel.Close()
	flowServer, err := NewFlowServer(ctx, state.ExternalConnectionID, serveEndpointDialer(onEvent, registry.DialContext), func(err error) {
		reportServeEvent(onEvent, ServeEvent{Kind: ServeFlowError, Err: err})
	})
	if err != nil {
		return err
	}
	defer flowServer.Close()
	if dialExed == nil {
		return fmt.Errorf("nil exed dialer")
	}
	client, err := dialExed(state.ExedURL)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("exed dialer returned a nil client")
	}
	defer func() {
		if err := client.Close(); err != nil {
			returnedErr = errors.Join(returnedErr, fmt.Errorf("close exed connection: %w", err))
		}
	}()

	err = RunControlStream(ctx, client, state.Credentials(), string(publicKey), endpointUpdates, func(ctx context.Context, message *connectapi.ControlMessage) error {
		if err := wireGuardTunnel.HandleControlMessage(ctx, message); err != nil {
			return err
		}
		if err := flowServer.SetNetwork(wireGuardTunnel.Net()); err != nil {
			return err
		}
		reportServeEvent(onEvent, ServeEvent{Kind: ServeTunnelConfigured})
		return nil
	}, func(event ControlEvent) error {
		handleServeControlEvent(onEvent, event)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return &ServeControlError{Err: err}
	}
	return nil
}

func handleServeControlEvent(handler func(ServeEvent), event ControlEvent) {
	switch event.Kind {
	case ControlConnected:
		reportServeEvent(handler, ServeEvent{Kind: ServeControlConnected, Attempt: event.Attempt})
	case ControlDisconnected:
		reportServeEvent(handler, ServeEvent{Kind: ServeControlDisconnected, Err: event.Err, Terminal: event.Terminal})
	case ControlRetrying:
		reportServeEvent(handler, ServeEvent{Kind: ServeControlRetrying, Err: event.Err, Delay: event.Delay, Attempt: event.Attempt})
	case ControlSnapshotSent:
		reportServeEvent(handler, ServeEvent{Kind: ServeEndpointSnapshotSent})
	}
}

func serveEndpointDialer(handler func(ServeEvent), dial EndpointDialer) EndpointDialer {
	return func(ctx context.Context, flowContext FlowContext, endpoint string, targetPort int) (net.Conn, error) {
		connection, err := dial(ctx, flowContext, endpoint, targetPort)
		if err == nil && connection != nil {
			reportServeEvent(handler, ServeEvent{Kind: ServeOutboundConnected, Target: connection.RemoteAddr().String()})
		}
		return connection, err
	}
}

func publishEndpointUpdate(updates chan EndpointUpdate, snapshot EndpointRegistrySnapshot) {
	update := EndpointUpdate{Endpoints: append([]Endpoint(nil), snapshot.Endpoints...)}
	select {
	case updates <- update:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	updates <- update
}

func reportServeEvent(handler func(ServeEvent), event ServeEvent) {
	if handler != nil {
		handler(event)
	}
}
