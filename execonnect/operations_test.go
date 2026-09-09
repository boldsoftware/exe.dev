package execonnect

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		uid         int
		home        string
		environment map[string]string
		wantState   string
		wantSocket  string
	}{
		{
			name:        "Linux root",
			goos:        "linux",
			uid:         0,
			home:        "/root",
			environment: map[string]string{"XDG_STATE_HOME": "/root/state", "XDG_RUNTIME_DIR": "/root/run"},
			wantState:   "/var/lib/execonnect",
			wantSocket:  "/run/execonnect/control.sock",
		},
		{
			name:        "Linux non-root XDG",
			goos:        "linux",
			uid:         1000,
			home:        "/home/tester",
			environment: map[string]string{"XDG_STATE_HOME": "/state", "XDG_RUNTIME_DIR": "/runtime"},
			wantState:   "/state/execonnect",
			wantSocket:  "/runtime/execonnect/control.sock",
		},
		{
			name:       "Linux non-root fallback",
			goos:       "linux",
			uid:        1000,
			home:       "/home/tester",
			wantState:  "/home/tester/.local/state/execonnect",
			wantSocket: "/home/tester/.local/state/execonnect/control.sock",
		},
		{
			name:        "Linux ignores relative XDG values",
			goos:        "linux",
			uid:         1000,
			home:        "/home/tester",
			environment: map[string]string{"XDG_STATE_HOME": "relative-state", "XDG_RUNTIME_DIR": "relative-runtime"},
			wantState:   "/home/tester/.local/state/execonnect",
			wantSocket:  "/home/tester/.local/state/execonnect/control.sock",
		},
		{
			name:        "Darwin",
			goos:        "darwin",
			uid:         0,
			home:        "/Users/tester",
			environment: map[string]string{"XDG_STATE_HOME": "/state", "XDG_RUNTIME_DIR": "/runtime"},
			wantState:   "/Users/tester/Library/Application Support/execonnect",
			wantSocket:  "/Users/tester/Library/Application Support/execonnect/control.sock",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir, socketPath, err := defaultPaths(
				test.goos,
				func() int { return test.uid },
				func() (string, error) { return test.home, nil },
				func(name string) (string, bool) {
					value, ok := test.environment[name]
					return value, ok
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if stateDir != test.wantState || socketPath != test.wantSocket {
				t.Fatalf("defaults = %q, %q; want %q, %q", stateDir, socketPath, test.wantState, test.wantSocket)
			}
		})
	}
}

func TestEnrollPreflightsDaemonBeforeReadingToken(t *testing.T) {
	read := false
	_, err := Enroll(t.Context(), EnrollOptions{
		SocketPath:    filepath.Join(t.TempDir(), "missing.sock"),
		ExpectedOldID: "",
		ReadToken: func() (string, error) {
			read = true
			return "must-not-be-read", nil
		},
	})
	if !errors.Is(err, ErrServeNotRunning) {
		t.Fatalf("Enroll error = %v", err)
	}
	if read {
		t.Fatal("token reader called while daemon was down")
	}
}

func TestRoutineClientsRequireDaemon(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := InspectLocalStatus(t.Context(), StatusOptions{StateDir: stateDir}); !errors.Is(err, ErrServeNotRunning) {
		t.Fatalf("status error = %v", err)
	}
	if _, err := ListEndpoints(t.Context(), EndpointListOptions{StateDir: stateDir}); !errors.Is(err, ErrServeNotRunning) {
		t.Fatalf("list error = %v", err)
	}
	if _, _, err := AddEndpoint(t.Context(), EndpointOptions{StateDir: stateDir, Name: "db", URL: "tcp://db:5432"}); !errors.Is(err, ErrServeNotRunning) {
		t.Fatalf("add error = %v", err)
	}
}

func TestExplicitPathOverrides(t *testing.T) {
	stateDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "control.sock")

	resolvedStateDir, err := resolveStateDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedStateDir != stateDir {
		t.Fatalf("state directory = %q, want %q", resolvedStateDir, stateDir)
	}
	resolvedSocketPath, err := resolveSocketPath(stateDir, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSocketPath != socketPath {
		t.Fatalf("socket path = %q, want %q", resolvedSocketPath, socketPath)
	}
}

func TestSocketResolutionUsesExplicitStateDirectoryForIsolation(t *testing.T) {
	stateDir := t.TempDir()
	path, err := resolveSocketPath(stateDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(stateDir, controlSocketName) {
		t.Fatalf("socket path = %q", path)
	}
}

func TestLinuxClientCandidatesBridgeUserAndSystemDaemons(t *testing.T) {
	environment := map[string]string{
		"XDG_STATE_HOME":  "/state",
		"XDG_RUNTIME_DIR": "/runtime",
	}
	lookupEnv := func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	}
	homeDir := func() (string, error) { return "/home/operator", nil }

	_, serveSocket, err := defaultPaths("linux", func() int { return 1000 }, homeDir, lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if serveSocket != "/runtime/execonnect/control.sock" {
		t.Fatalf("foreground serve socket = %q", serveSocket)
	}
	candidates, err := clientSocketCandidates("", "", "linux", func() int { return 1000 }, homeDir, lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/runtime/execonnect/control.sock", linuxDefaultSocketPath}
	if !slicesEqual(candidates, want) {
		t.Fatalf("client sockets = %v, want %v", candidates, want)
	}
}

func TestClientSocketCandidatesHonorExplicitPathsAndMissingHome(t *testing.T) {
	homeError := func() (string, error) { return "", errors.New("HOME unavailable") }
	noEnvironment := func(string) (string, bool) { return "", false }
	explicitSocket := filepath.Join(t.TempDir(), "explicit.sock")
	candidates, err := clientSocketCandidates("", explicitSocket, "linux", func() int { return 1000 }, homeError, noEnvironment)
	if err != nil || !slicesEqual(candidates, []string{explicitSocket}) {
		t.Fatalf("explicit socket candidates = %v, err=%v", candidates, err)
	}
	explicitState := t.TempDir()
	candidates, err = clientSocketCandidates(explicitState, "", "linux", func() int { return 1000 }, homeError, noEnvironment)
	if err != nil || !slicesEqual(candidates, []string{filepath.Join(explicitState, controlSocketName)}) {
		t.Fatalf("explicit state candidates = %v, err=%v", candidates, err)
	}
	candidates, err = clientSocketCandidates("", "", "linux", func() int { return 1000 }, homeError, noEnvironment)
	if err != nil || !slicesEqual(candidates, []string{linuxDefaultSocketPath}) {
		t.Fatalf("missing HOME candidates = %v, err=%v", candidates, err)
	}
}

func TestClientControlPrefersUserSocketAndFallsBackToSystemSocket(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "user.sock")
	systemPath := filepath.Join(t.TempDir(), "system.sock")
	userCalls := startStatusControlServer(t, userPath, "ec_user")
	systemCalls := startStatusControlServer(t, systemPath, "ec_system")

	response, selected, err := callClientControl(t.Context(), []string{userPath, systemPath}, "GET", "/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected != userPath || response.Status == nil || response.Status.ExternalConnectionID != "ec_user" {
		t.Fatalf("selected=%q response=%+v", selected, response.Status)
	}
	if userCalls.Load() != 1 || systemCalls.Load() != 0 {
		t.Fatalf("calls: user=%d system=%d", userCalls.Load(), systemCalls.Load())
	}

	missingUser := filepath.Join(t.TempDir(), "missing.sock")
	response, selected, err = callClientControl(t.Context(), []string{missingUser, systemPath}, "GET", "/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected != systemPath || response.Status == nil || response.Status.ExternalConnectionID != "ec_system" {
		t.Fatalf("fallback selected=%q response=%+v", selected, response.Status)
	}
	if systemCalls.Load() != 1 {
		t.Fatalf("system calls = %d, want 1", systemCalls.Load())
	}
}

func TestClientSocketCandidatesUseXDGWithoutHomeAndDeduplicateSystemSocket(t *testing.T) {
	homeError := func() (string, error) { return "", errors.New("HOME unavailable") }
	xdgEnvironment := func(name string) (string, bool) {
		if name == "XDG_RUNTIME_DIR" {
			return "/runtime", true
		}
		return "", false
	}
	candidates, err := clientSocketCandidates("", "", "linux", func() int { return 1000 }, homeError, xdgEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/runtime/execonnect/control.sock", linuxDefaultSocketPath}
	if !slicesEqual(candidates, want) {
		t.Fatalf("XDG client sockets = %v, want %v", candidates, want)
	}

	systemEnvironment := func(name string) (string, bool) {
		if name == "XDG_RUNTIME_DIR" {
			return "/run", true
		}
		return "", false
	}
	candidates, err = clientSocketCandidates("", "", "linux", func() int { return 1000 }, homeError, systemEnvironment)
	if err != nil || !slicesEqual(candidates, []string{linuxDefaultSocketPath}) {
		t.Fatalf("deduplicated client sockets = %v, err=%v", candidates, err)
	}
}

func TestClientControlDoesNotFallbackAfterDaemonResponse(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "user.sock")
	systemPath := filepath.Join(t.TempDir(), "system.sock")
	listener, err := net.Listen("unix", userPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("{}"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	systemCalls := startStatusControlServer(t, systemPath, "ec_system")

	_, _, err = callClientControl(t.Context(), []string{userPath, systemPath}, "POST", "/endpoints/add", controlEndpointRequest{Name: "db"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %v", err)
	}
	if systemCalls.Load() != 0 {
		t.Fatalf("system calls = %d, want 0", systemCalls.Load())
	}
}

func TestClientControlDiagnosticsNameCandidates(t *testing.T) {
	first := filepath.Join(t.TempDir(), "user.sock")
	second := filepath.Join(t.TempDir(), "system.sock")
	_, _, err := callClientControl(t.Context(), []string{first, second}, "GET", "/status", nil)
	if !errors.Is(err, ErrServeNotRunning) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{first, second, "execonnect serve", "systemd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func startStatusControlServer(t *testing.T, path, externalConnectionID string) *atomic.Int32 {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": map[string]any{
			"instance_nonce": "test", "service": "running", "enrollment": "enrolled",
			"external_connection_id": externalConnectionID, "endpoint_count": 0,
			"control": "inactive", "tunnel": "inactive",
		}})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return &calls
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
