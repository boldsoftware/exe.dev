package execonnect

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"tailscale.com/types/key"
)

type fakeDaemonSession struct {
	mu        sync.Mutex
	done      chan struct{}
	err       error
	stopped   bool
	onStop    func()
	published []EndpointRegistrySnapshot
}

func newFakeDaemonSession() *fakeDaemonSession {
	return &fakeDaemonSession{done: make(chan struct{})}
}

func (session *fakeDaemonSession) Stop() error {
	session.mu.Lock()
	if !session.stopped {
		session.stopped = true
		if session.onStop != nil {
			session.onStop()
		}
		close(session.done)
	}
	err := session.err
	session.mu.Unlock()
	return err
}

func (session *fakeDaemonSession) Publish(snapshot EndpointRegistrySnapshot) {
	session.mu.Lock()
	session.published = append(session.published, copyEndpointRegistrySnapshot(snapshot))
	session.mu.Unlock()
}

func (session *fakeDaemonSession) Done() <-chan struct{} { return session.done }

func (session *fakeDaemonSession) Err() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.err
}

func (session *fakeDaemonSession) finish(err error) {
	session.mu.Lock()
	if !session.stopped {
		session.err = err
		session.stopped = true
		close(session.done)
	}
	session.mu.Unlock()
}

type fakeSessionStarter struct {
	mu       sync.Mutex
	states   []State
	sessions []*fakeDaemonSession
	onStart  func(State, *fakeDaemonSession)
}

func (starter *fakeSessionStarter) start(_ context.Context, state State, _ *EndpointRegistry, _ func(ServeEvent)) daemonSession {
	session := newFakeDaemonSession()
	snapshot, err := state.endpointSnapshot()
	if err != nil {
		panic(err)
	}
	session.Publish(snapshot)
	starter.mu.Lock()
	starter.states = append(starter.states, state)
	starter.sessions = append(starter.sessions, session)
	if starter.onStart != nil {
		starter.onStart(state, session)
	}
	starter.mu.Unlock()
	return session
}

func TestControlSocketActiveContentionAndStaleRecovery(t *testing.T) {
	runtimeDir := t.TempDir()
	path := filepath.Join(runtimeDir, controlSocketName)
	first, err := listenControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o660 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control socket mode = %v", info.Mode())
	}
	directoryInfo, err := os.Stat(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o770 {
		t.Fatalf("runtime directory mode = %o", directoryInfo.Mode().Perm())
	}
	if _, err := listenControlSocket(path); !errors.Is(err, ErrServeRunning) {
		t.Fatalf("duplicate listen error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := listenControlSocket(path)
	if err != nil {
		t.Fatalf("replace stale socket: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControlSocketNeverRemovesNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), controlSocketName)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenControlSocket(path); err == nil || !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("listen error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("control path content = %q, err=%v", data, err)
	}
}

func TestServeContentionDoesNotTouchStateOrLaunchSession(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(t.TempDir(), controlSocketName)
	owner, err := listenControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var dials atomic.Int64
	err = serve(t.Context(), ServeOptions{StateDir: stateDir, SocketPath: path}, func(string) (connectorClient, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	})
	if !errors.Is(err, ErrServeRunning) {
		t.Fatalf("serve error = %v", err)
	}
	if dials.Load() != 0 {
		t.Fatalf("remote dials = %d", dials.Load())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state touched before socket ownership: %v", err)
	}
}

func TestDaemonEnrollmentReplacementCASRetentionIdentityAndOrdering(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	endpoint, err := NormalizeEndpoint("database", "tcp://db.internal:5432", "")
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := newUnenrolledState().withEndpointSnapshot(EndpointRegistrySnapshot{Endpoints: []Endpoint{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	oldState.Enrolled = true
	oldState.ExternalConnectionID = "ec_old"
	oldState.ConnectorSecret = "ecs_old"
	oldState.WireGuardPrivateKey = key.NewNode()
	oldState.ExedURL = "https://old.example"
	if err := writeState(statePath, oldState); err != nil {
		t.Fatal(err)
	}

	var orderMu sync.Mutex
	var order []string
	starter := &fakeSessionStarter{}
	starter.onStart = func(state State, session *fakeDaemonSession) {
		orderMu.Lock()
		order = append(order, "start:"+state.ExternalConnectionID)
		orderMu.Unlock()
		if state.ExternalConnectionID == "ec_old" {
			session.onStop = func() {
				committed, err := LoadState(statePath)
				if err != nil || committed.ExternalConnectionID != "ec_new" {
					t.Errorf("old session stopped before durable replacement: state=%#v err=%v", committed, err)
				}
				orderMu.Lock()
				order = append(order, "stop:ec_old")
				orderMu.Unlock()
			}
		}
	}
	deps := daemonDependencies{
		persist: func(path string, state State) error {
			if err := writeState(path, state); err != nil {
				return err
			}
			orderMu.Lock()
			order = append(order, "commit:"+state.ExternalConnectionID)
			orderMu.Unlock()
			return nil
		},
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			return enrollmentIdentity{externalConnectionID: "ec_new", connectorSecret: "ecs_new"}, nil
		},
		startSession: starter.start,
	}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, deps)
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	orderMu.Lock()
	order = nil
	orderMu.Unlock()

	newState, err := daemon.enroll(t.Context(), controlEnrollRequest{
		ExpectedOldID: "ec_old",
		ExedURL:       "https://new.example",
		Token:         "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(newState.Endpoints) != 1 || newState.Endpoints[0].Name != "database" {
		t.Fatalf("replacement endpoints = %#v", newState.Endpoints)
	}
	if newState.ExternalConnectionID != "ec_new" || newState.ConnectorSecret != "ecs_new" || newState.ExedURL != "https://new.example" {
		t.Fatalf("replacement identity = %#v", newState)
	}
	newKey, err := newState.WireGuardPrivateKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := oldState.WireGuardPrivateKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(newKey, oldKey) {
		t.Fatal("replacement transferred old WireGuard identity")
	}
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{"commit:ec_old", "commit:ec_new", "stop:ec_old", "start:ec_new"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("replacement order = %v, want %v", gotOrder, wantOrder)
	}

	_, err = daemon.enroll(t.Context(), controlEnrollRequest{ExpectedOldID: "ec_old", ExedURL: DefaultExedURL, Token: "loser"})
	if !errors.Is(err, ErrEnrollmentConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
}

func TestEnrollmentPersistencePreflightSuppressesRemoteCall(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var persistCalls atomic.Int64
	var remoteCalls atomic.Int64
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, daemonDependencies{
		persist: func(string, State) error {
			persistCalls.Add(1)
			return errors.New("disk full")
		},
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			remoteCalls.Add(1)
			return enrollmentIdentity{externalConnectionID: "ec_remote", connectorSecret: "ecs_remote"}, nil
		},
		startSession: (&fakeSessionStarter{}).start,
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}

	_, err := daemon.enroll(t.Context(), controlEnrollRequest{ExedURL: DefaultExedURL, Token: "one-time-token"})
	if err == nil || !strings.Contains(err.Error(), "preflight connector state persistence") {
		t.Fatalf("enroll error = %v", err)
	}
	if persistCalls.Load() != 1 {
		t.Fatalf("persistence calls = %d, want 1", persistCalls.Load())
	}
	if remoteCalls.Load() != 0 {
		t.Fatalf("remote enrollment calls = %d, want 0", remoteCalls.Load())
	}
}

func TestConcurrentEnrollmentCASHasOneWinner(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	entered := make(chan struct{})
	release := make(chan struct{})
	var remoteCalls atomic.Int64
	starter := &fakeSessionStarter{}
	deps := daemonDependencies{
		persist: writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			if remoteCalls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return enrollmentIdentity{externalConnectionID: "ec_winner", connectorSecret: "ecs_winner"}, nil
		},
		startSession: starter.start,
	}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, deps)
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, err := daemon.enroll(t.Context(), controlEnrollRequest{ExedURL: DefaultExedURL, Token: "one"})
		results <- err
	}()
	<-entered
	go func() {
		_, err := daemon.enroll(t.Context(), controlEnrollRequest{ExedURL: DefaultExedURL, Token: "two"})
		results <- err
	}()
	close(release)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("concurrent results = %v, %v", first, second)
	}
	loser := first
	if loser == nil {
		loser = second
	}
	if !errors.Is(loser, ErrEnrollmentConflict) {
		t.Fatalf("loser error = %v", loser)
	}
	if remoteCalls.Load() != 1 {
		t.Fatalf("remote enrollment calls = %d, want 1", remoteCalls.Load())
	}
}

func TestEndpointPublicationsCannotReorderConcurrentMutations(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, daemonDependencies{
		persist: writeState,
		startSession: func(context.Context, State, *EndpointRegistry, func(ServeEvent)) daemonSession {
			panic("unexpected session launch")
		},
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	firstPublish := make(chan struct{})
	releaseFirst := make(chan struct{})
	session := &orderedPublishSession{
		fakeDaemonSession: newFakeDaemonSession(),
		firstPublish:      firstPublish,
		releaseFirst:      releaseFirst,
	}
	daemon.mu.Lock()
	daemon.session = session
	daemon.mu.Unlock()

	results := make(chan error, 2)
	go func() {
		_, _, err := daemon.mutateEndpoint("add", controlEndpointRequest{Name: "alpha", URL: "tcp://alpha.internal:1"})
		results <- err
	}()
	<-firstPublish
	go func() {
		_, _, err := daemon.mutateEndpoint("add", controlEndpointRequest{Name: "beta", URL: "tcp://beta.internal:2"})
		results <- err
	}()
	close(releaseFirst)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	session.publishMu.Lock()
	defer session.publishMu.Unlock()
	if len(session.snapshots) != 2 || len(session.snapshots[0].Endpoints) != 1 || len(session.snapshots[1].Endpoints) != 2 {
		t.Fatalf("publication order = %#v", session.snapshots)
	}
}

type orderedPublishSession struct {
	*fakeDaemonSession
	firstPublish chan struct{}
	releaseFirst chan struct{}
	publishMu    sync.Mutex
	snapshots    []EndpointRegistrySnapshot
}

func (session *orderedPublishSession) Publish(snapshot EndpointRegistrySnapshot) {
	session.publishMu.Lock()
	first := len(session.snapshots) == 0
	if first {
		close(session.firstPublish)
		session.publishMu.Unlock()
		<-session.releaseFirst
		session.publishMu.Lock()
	}
	session.snapshots = append(session.snapshots, copyEndpointRegistrySnapshot(snapshot))
	session.publishMu.Unlock()
}

func TestReplacementLaunchIncludesEndpointMutationDuringOldSessionShutdown(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := newUnenrolledState()
	state.Enrolled = true
	state.ExternalConnectionID = "ec_old"
	state.ConnectorSecret = "ecs_old"
	state.WireGuardPrivateKey = key.NewNode()
	state.ExedURL = DefaultExedURL
	if err := writeState(statePath, state); err != nil {
		t.Fatal(err)
	}
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	starter := &fakeSessionStarter{}
	starter.onStart = func(state State, session *fakeDaemonSession) {
		if state.ExternalConnectionID == "ec_old" {
			session.onStop = func() {
				close(stopEntered)
				<-releaseStop
			}
		}
	}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, daemonDependencies{
		persist: writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			return enrollmentIdentity{externalConnectionID: "ec_new", connectorSecret: "ecs_new"}, nil
		},
		startSession: starter.start,
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	enrollResult := make(chan error, 1)
	go func() {
		_, err := daemon.enroll(t.Context(), controlEnrollRequest{ExpectedOldID: "ec_old", ExedURL: DefaultExedURL, Token: "token"})
		enrollResult <- err
	}()
	<-stopEntered
	if _, changed, err := daemon.mutateEndpoint("add", controlEndpointRequest{Name: "database", URL: "tcp://db.internal:5432"}); err != nil || !changed {
		t.Fatalf("mutation during replacement changed=%v err=%v", changed, err)
	}
	close(releaseStop)
	if err := <-enrollResult; err != nil {
		t.Fatal(err)
	}
	starter.mu.Lock()
	defer starter.mu.Unlock()
	if len(starter.states) != 2 || len(starter.states[1].Endpoints) != 1 || starter.states[1].Endpoints[0].Name != "database" {
		t.Fatalf("replacement session state = %#v", starter.states)
	}
}

func TestControlEnrollmentConflictIsStructured(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := newUnenrolledState()
	state.Enrolled = true
	state.ExternalConnectionID = "ec_current"
	state.ConnectorSecret = "ecs_current"
	state.WireGuardPrivateKey = key.NewNode()
	state.ExedURL = DefaultExedURL
	if err := writeState(statePath, state); err != nil {
		t.Fatal(err)
	}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, daemonDependencies{
		persist: writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			t.Fatal("conflicting request reached remote enrollment")
			return enrollmentIdentity{}, nil
		},
		startSession: (&fakeSessionStarter{}).start,
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"expected_old_id":"ec_stale","exed_url":"https://exe.dev","token":"token"}`)
	request := httptest.NewRequest(http.MethodPost, "/enroll", body)
	response := httptest.NewRecorder()
	newLocalControlServer(daemon).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status code = %d, body=%s", response.Code, response.Body.String())
	}
	var decoded controlResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != "enrollment_conflict" || decoded.Error.CurrentExternalConnectionID != "ec_current" {
		t.Fatalf("conflict response = %#v", decoded)
	}
}

func TestUnenrolledEndpointCRUDPublishesAfterEnrollment(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	starter := &fakeSessionStarter{}
	deps := daemonDependencies{
		persist: writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			return enrollmentIdentity{externalConnectionID: "ec_new", connectorSecret: "ecs_new"}, nil
		},
		startSession: starter.start,
	}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, deps)
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := daemon.mutateEndpoint("add", controlEndpointRequest{Name: "database", URL: "tcp://db.internal:5432"}); err != nil || !changed {
		t.Fatalf("unenrolled add changed=%v err=%v", changed, err)
	}
	if _, changed, err := daemon.mutateEndpoint("update", controlEndpointRequest{Name: "database", URL: "tcp://db-new.internal:5432"}); err != nil || !changed {
		t.Fatalf("unenrolled update changed=%v err=%v", changed, err)
	}
	if _, changed, err := daemon.mutateEndpoint("add", controlEndpointRequest{Name: "temporary", URL: "tcp://temp.internal:1234"}); err != nil || !changed {
		t.Fatalf("unenrolled second add changed=%v err=%v", changed, err)
	}
	listed, err := daemon.listEndpoints()
	if err != nil || len(listed.Endpoints) != 2 {
		t.Fatalf("unenrolled list = %#v, err=%v", listed, err)
	}
	if _, changed, err := daemon.mutateEndpoint("remove", controlEndpointRequest{Name: "temporary"}); err != nil || !changed {
		t.Fatalf("unenrolled remove changed=%v err=%v", changed, err)
	}
	if len(starter.sessions) != 0 {
		t.Fatal("unenrolled endpoint mutation launched a session")
	}
	if _, err := daemon.enroll(t.Context(), controlEnrollRequest{ExedURL: DefaultExedURL, Token: "token"}); err != nil {
		t.Fatal(err)
	}
	if len(starter.sessions) != 1 {
		t.Fatalf("started sessions = %d", len(starter.sessions))
	}
	session := starter.sessions[0]
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.published) != 1 || len(session.published[0].Endpoints) != 1 || session.published[0].Endpoints[0].Target != "db-new.internal:5432" {
		t.Fatalf("initial publication = %#v", session.published)
	}
}

func TestTerminalRemoteAuthLeavesDaemonServingErrorStatus(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := newUnenrolledState()
	state.Enrolled = true
	state.ExternalConnectionID = "ec_auth"
	state.ConnectorSecret = "ecs_auth"
	state.WireGuardPrivateKey = key.NewNode()
	state.ExedURL = DefaultExedURL
	if err := writeState(statePath, state); err != nil {
		t.Fatal(err)
	}
	starter := &fakeSessionStarter{}
	daemon := newDaemon(t.Context(), "nonce", statePath, nil, daemonDependencies{
		persist:      writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) { panic("unexpected enroll") },
		startSession: starter.start,
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	session := starter.sessions[0]
	session.finish(&ServeControlError{Err: status.Error(codes.Unauthenticated, "credential rejected")})
	daemon.mu.Lock()
	generation := daemon.generation
	daemon.mu.Unlock()
	daemon.watchSession(generation, session)
	got := daemon.status()
	if got.Service != "running" || got.Enrollment != "enrolled" || got.Control != "error" || got.Tunnel != "inactive" || !strings.Contains(got.Error, "Unauthenticated") {
		t.Fatalf("status = %#v", got)
	}
}

func TestStatusFieldCoverage(t *testing.T) {
	daemon := newDaemon(t.Context(), "nonce-value", filepath.Join(t.TempDir(), "state.json"), nil, daemonDependencies{
		persist: writeState,
		enrollRemote: func(context.Context, string, string) (enrollmentIdentity, error) {
			return enrollmentIdentity{}, errors.New("unused")
		},
		startSession: (&fakeSessionStarter{}).start,
	})
	if err := daemon.initialize(); err != nil {
		t.Fatal(err)
	}
	status := daemon.status()
	if status.InstanceNonce != "nonce-value" || status.Service != "running" || status.Enrollment != "unenrolled" || status.EndpointCount != 0 || status.Control != "inactive" || status.Tunnel != "inactive" || status.Error != "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestControlJSONIsStrictAndBounded(t *testing.T) {
	var request controlEndpointRequest
	if err := decodeBoundedJSON(strings.NewReader(`{"name":"db","unknown":true}`), maxControlRequestSize, &request); err == nil {
		t.Fatal("accepted unknown request field")
	}
	if err := decodeBoundedJSON(strings.NewReader(strings.Repeat("x", maxControlRequestSize+1)), maxControlRequestSize, &request); err == nil {
		t.Fatal("accepted oversized request")
	}
}

type publishingDaemonSession struct {
	*fakeDaemonSession
	updates chan EndpointUpdate
}

func (session *publishingDaemonSession) Publish(snapshot EndpointRegistrySnapshot) {
	session.fakeDaemonSession.Publish(snapshot)
	publishEndpointUpdate(session.updates, snapshot)
}

func startLocalControl(t *testing.T, stateDir string, initial EndpointRegistrySnapshot, updates chan EndpointUpdate) *EndpointRegistry {
	t.Helper()
	state, err := newUnenrolledState().withEndpointSnapshot(initial)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "state.json")
	if err := writeState(statePath, state); err != nil {
		t.Fatal(err)
	}
	registry, err := NewEndpointRegistry(initial.Endpoints)
	if err != nil {
		t.Fatal(err)
	}
	daemon := newDaemon(t.Context(), "test-nonce", statePath, nil, daemonDependencies{persist: writeState})
	daemon.state = state
	daemon.registry = registry
	daemon.ready = true
	daemon.service = "running"
	daemon.control = "inactive"
	daemon.tunnel = "inactive"
	daemon.session = &publishingDaemonSession{fakeDaemonSession: newFakeDaemonSession(), updates: updates}

	path := filepath.Join(stateDir, controlSocketName)
	socket, err := listenControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- runControlServer(ctx, socket.listener, newLocalControlServer(daemon))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("control server: %v", err)
		}
		if err := socket.Close(); err != nil {
			t.Errorf("close control socket: %v", err)
		}
	})
	return registry
}
