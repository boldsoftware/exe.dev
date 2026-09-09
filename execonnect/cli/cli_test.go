package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/boldsoftware/exe.dev/execonnect"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunHelpAndCommandErrors(t *testing.T) {
	for _, testCase := range []struct {
		args []string
		want []string
	}{
		{args: nil, want: []string{"COMMANDS:", "Quick start:", "https://exe.dev/integrations#external-connections", "execonnect endpoint add NAME URL", "Linux or macOS", "systemd"}},
		{args: []string{"--help"}, want: []string{"COMMANDS:", "/run/execonnect/control.sock", "$XDG_RUNTIME_DIR", "execonnect serve"}},
		{args: []string{"enroll", "--help"}, want: []string{"Connect this machine", "[command options] [token]", "https://exe.dev/integrations#external-connections", "another terminal", "execonnect endpoint add NAME URL"}},
		{args: []string{"endpoint"}, want: []string{"COMMANDS:", "lowercase DNS label", "execonnect endpoint add NAME URL"}},
		{args: []string{"endpoint", "--help"}, want: []string{"lowercase DNS label", "http, https", "Start serve", "execonnect endpoint add NAME URL"}},
		{args: []string{"endpoint", "add", "--help"}, want: []string{"At most 100", "no user info", "--tls-server-name"}},
		{args: []string{"endpoint", "update", "--help"}, want: []string{"must already exist", "rename operation"}},
		{args: []string{"endpoint", "remove", "--help"}, want: []string{"established and pending flows", "endpoint remove database"}},
		{args: []string{"endpoint", "list", "--help"}, want: []string{"deterministic name order", "effective HTTPS TLS server name"}},
		{args: []string{"serve", "--help"}, want: []string{"foreground", "Linux", "macOS", "systemd", "remains available", "--verbose"}},
		{args: []string{"status", "--help"}, want: []string{"running local daemon", "tunnel state", "never reads daemon state", "https://exe.dev/integrations#external-connections"}},
	} {
		t.Run(strings.Join(testCase.args, "_"), func(t *testing.T) {
			stdout, stderr, err := runForTest(t, strings.NewReader(""), testCase.args...)
			if err != nil {
				t.Fatalf("Run(%v): %v", testCase.args, err)
			}
			if stderr != "" {
				t.Fatalf("help stderr = %q", stderr)
			}
			if !strings.Contains(stdout, "USAGE:") {
				t.Fatalf("help output = %q", stdout)
			}
			for _, want := range testCase.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("help output missing %q: %s", want, stdout)
				}
			}
		})
	}

	reader := &readTrackingReader{}
	if _, _, err := runForTest(t, reader, "endroll"); err == nil || !strings.Contains(err.Error(), `did you mean "enroll"`) {
		t.Fatalf("endroll error = %v", err)
	}
	if reader.read {
		t.Fatal("unknown command consumed enrollment input")
	}
	if _, _, err := runForTest(t, strings.NewReader(""), "doctor"); err == nil || !strings.Contains(err.Error(), "execonnect --help") || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("doctor error = %v", err)
	}
	if _, _, err := runForTest(t, strings.NewReader(""), "endpoint", "udpate"); err == nil || !strings.Contains(err.Error(), `did you mean "update"`) || !strings.Contains(err.Error(), "execonnect endpoint --help") {
		t.Fatalf("endpoint typo error = %v", err)
	}
	if _, _, err := runForTest(t, strings.NewReader(""), "--socket=", "status"); err == nil || !strings.Contains(err.Error(), "option --socket requires a value") {
		t.Fatalf("empty socket error = %v", err)
	}
	if _, _, err := runForTest(t, strings.NewReader(""), "enroll", "--enrollment-token", "secret"); err == nil || !strings.Contains(err.Error(), "execonnect enroll --help") {
		t.Fatalf("legacy token option error = %v", err)
	}
	if _, _, err := runForTest(t, strings.NewReader(""), "serve", "--config", "endpoints.yaml"); err == nil || !strings.Contains(err.Error(), "execonnect serve --help") {
		t.Fatalf("legacy config option error = %v", err)
	}
}

func TestReadEnrollmentTokenFromPipeAndHiddenTerminal(t *testing.T) {
	token, err := readEnrollmentToken(t.Context(), strings.NewReader("pipe-token\nignored\n"), io.Discard)
	if err != nil || token != "pipe-token" {
		t.Fatalf("piped token = %q, err=%v", token, err)
	}
	if _, err := readEnrollmentToken(t.Context(), strings.NewReader("\n"), io.Discard); err == nil {
		t.Fatal("empty piped token succeeded")
	}

	input, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	oldIsTerminal := isTerminal
	oldReadTerminalLineInput := readTerminalLineInput
	isTerminal = func(int) bool { return true }
	readTerminalLineInput = func(_ context.Context, fd int, hidden bool) ([]byte, error) {
		if fd != int(input.Fd()) {
			t.Fatalf("terminal fd = %d, want %d", fd, input.Fd())
		}
		if !hidden {
			t.Fatal("enrollment token read did not hide input")
		}
		return []byte("hidden-token"), nil
	}
	t.Cleanup(func() {
		isTerminal = oldIsTerminal
		readTerminalLineInput = oldReadTerminalLineInput
	})
	var prompt bytes.Buffer
	token, err = readEnrollmentToken(t.Context(), input, &prompt)
	if err != nil || token != "hidden-token" {
		t.Fatalf("terminal token = %q, err=%v", token, err)
	}
	if prompt.String() != "Enrollment token: \n" || strings.Contains(prompt.String(), token) {
		t.Fatalf("terminal output = %q", prompt.String())
	}
}

type fakeControlAPI struct {
	mu          sync.Mutex
	currentID   string
	enrollCalls int
	tokens      []string
}

func (api *fakeControlAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/status":
		enrollment := "unenrolled"
		if api.currentID != "" {
			enrollment = "enrolled"
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": map[string]any{
			"instance_nonce": "test", "service": "running", "enrollment": enrollment,
			"external_connection_id": api.currentID, "endpoint_count": 0,
			"control": "inactive", "tunnel": "inactive",
		}})
	case "/endpoints":
		_ = json.NewEncoder(writer).Encode(map[string]any{"endpoints": []any{}})
	case "/enroll":
		var input struct {
			ExpectedOldID string `json:"expected_old_id"`
			Token         string `json:"token"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			testingErrorResponse(writer, err.Error())
			return
		}
		if input.ExpectedOldID != api.currentID {
			writer.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"code": "enrollment_conflict", "message": "conflict", "current_external_connection_id": api.currentID,
			}})
			return
		}
		api.enrollCalls++
		api.tokens = append(api.tokens, input.Token)
		api.currentID = "ec_enrolled_" + input.Token
		_ = json.NewEncoder(writer).Encode(map[string]any{"enrollment": map[string]any{"external_connection_id": api.currentID}})
	default:
		testingErrorResponse(writer, "unexpected path")
	}
}

func testingErrorResponse(writer http.ResponseWriter, message string) {
	writer.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": "test", "message": message}})
}

func startFakeControlAPI(t *testing.T, currentID string) (string, *fakeControlAPI) {
	t.Helper()
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeControlAPI{currentID: currentID}
	server := &http.Server{Handler: api}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})
	return stateDir, api
}

func TestGlobalPathOptionsWorkAnywhereUntilDoubleDash(t *testing.T) {
	stateDir, _ := startFakeControlAPI(t, "ec_paths")
	socketPath := filepath.Join(stateDir, "control.sock")
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "state before root command split", args: []string{"--state-dir", stateDir, "status"}, want: "External connection ID: ec_paths"},
		{name: "state after root command equal", args: []string{"status", "--state-dir=" + stateDir}, want: "External connection ID: ec_paths"},
		{name: "state between endpoint commands", args: []string{"endpoint", "--state-dir", stateDir, "list"}, want: "No endpoints configured"},
		{name: "socket after nested command split", args: []string{"endpoint", "list", "--socket", socketPath}, want: "No endpoints configured"},
		{name: "socket after nested command equal", args: []string{"endpoint", "list", "--socket=" + socketPath}, want: "No endpoints configured"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stdout, stderr, err := runForTest(t, strings.NewReader(""), testCase.args...)
			if err != nil || stderr != "" || !strings.Contains(stdout, testCase.want) {
				t.Fatalf("Run(%v) stdout=%q stderr=%q err=%v", testCase.args, stdout, stderr, err)
			}
		})
	}

	firstStateDir, _ := startFakeControlAPI(t, "ec_first")
	lastStateDir, _ := startFakeControlAPI(t, "ec_last")
	stdout, _, err := runForTest(t, strings.NewReader(""), "--state-dir", firstStateDir, "status", "--state-dir="+lastStateDir)
	if err != nil || !strings.Contains(stdout, "External connection ID: ec_last") {
		t.Fatalf("last state-dir did not win: stdout=%q err=%v", stdout, err)
	}

	_, _, err = runForTest(t, strings.NewReader(""), "status", "--", "--state-dir", stateDir)
	if err == nil || !strings.Contains(err.Error(), "status does not accept positional arguments") {
		t.Fatalf("option after -- was normalized: %v", err)
	}
}

func TestGlobalPathOptionsConsumeFlagLookingValues(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--state-dir", "-state"},
		{"status", "--socket", "-socket"},
	} {
		normalized, err := normalizeGlobalPathOptions(args)
		if err != nil {
			t.Fatalf("normalizeGlobalPathOptions(%v): %v", args, err)
		}
		want := []string{args[1], args[2], args[0]}
		if !slicesEqual(normalized, want) {
			t.Fatalf("normalizeGlobalPathOptions(%v) = %v, want %v", args, normalized, want)
		}
	}
}

func TestGlobalPathOptionsReportMissingValues(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--socket"},
		{"endpoint", "list", "--state-dir", "--"},
	} {
		_, _, err := runForTest(t, strings.NewReader(""), args...)
		if err == nil || !strings.Contains(err.Error(), "requires a value") || !strings.Contains(err.Error(), "execonnect --help") {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
	}
}

func TestVersionLabelsBuildRevision(t *testing.T) {
	stdout, stderr, err := runForTest(t, strings.NewReader(""), "--version")
	want := "execonnect version " + execonnect.BuildRevision() + "\n"
	if err != nil || stderr != "" || stdout != want {
		t.Fatalf("version stdout=%q stderr=%q err=%v, want %q", stdout, stderr, err, want)
	}
}

func TestVersionAndExplicitSocketDoNotRequireHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, _, err := runForTest(t, strings.NewReader(""), "--version"); err != nil {
		t.Fatalf("version: %v", err)
	}
	_, _, err := runForTest(t, strings.NewReader(""), "--socket", filepath.Join(t.TempDir(), "missing.sock"), "status")
	if !errors.Is(err, execonnect.ErrServeNotRunning) {
		t.Fatalf("explicit socket status error = %v", err)
	}
	_, _, err = runForTest(t, strings.NewReader(""), "--state-dir", t.TempDir(), "status")
	if !errors.Is(err, execonnect.ErrServeNotRunning) {
		t.Fatalf("explicit state status error = %v", err)
	}
}

func TestEnrollAcceptsPositionalTokenWithoutReadingOrPrompting(t *testing.T) {
	stateDir, api := startFakeControlAPI(t, "")
	input := &readTrackingReader{}
	stdout, stderr, err := runForTest(t, input, "--state-dir", stateDir, "enroll", "ect_positional")
	if err != nil || !strings.Contains(stdout, "ec_enrolled_ect_positional") || stderr != "" {
		t.Fatalf("positional enroll stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if input.read {
		t.Fatal("positional enroll read stdin")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.enrollCalls != 1 || !slicesEqual(api.tokens, []string{"ect_positional"}) {
		t.Fatalf("enroll calls=%d tokens=%v", api.enrollCalls, api.tokens)
	}
}

func TestBareEnrollRetainsHiddenInteractivePrompt(t *testing.T) {
	stateDir, api := startFakeControlAPI(t, "")
	input, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	oldIsTerminal := isTerminal
	oldReadTerminalLineInput := readTerminalLineInput
	isTerminal = func(int) bool { return true }
	hidden := false
	readTerminalLineInput = func(_ context.Context, fd int, hideInput bool) ([]byte, error) {
		if fd != int(input.Fd()) {
			t.Fatalf("terminal fd = %d, want %d", fd, input.Fd())
		}
		hidden = hideInput
		return []byte("ect_interactive"), nil
	}
	t.Cleanup(func() {
		isTerminal = oldIsTerminal
		readTerminalLineInput = oldReadTerminalLineInput
	})

	stdout, stderr, err := runForTest(t, input, "--state-dir", stateDir, "enroll")
	if err != nil || !strings.Contains(stdout, "ec_enrolled_ect_interactive") {
		t.Fatalf("interactive enroll stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !hidden || !strings.Contains(stderr, "Enrollment token:") || strings.Contains(stderr, "ect_interactive") {
		t.Fatalf("interactive prompt hidden=%t stderr=%q", hidden, stderr)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.enrollCalls != 1 || !slicesEqual(api.tokens, []string{"ect_interactive"}) {
		t.Fatalf("enroll calls=%d tokens=%v", api.enrollCalls, api.tokens)
	}
}

func TestEnrollRejectsExtraArgsWithoutExposingTokens(t *testing.T) {
	input := &readTrackingReader{}
	_, stderr, err := runForTest(t, input, "enroll", "ect_first_secret", "ect_second_secret")
	if err == nil || !strings.Contains(err.Error(), "execonnect enroll --help") {
		t.Fatalf("extra-argument enroll stderr=%q err=%v", stderr, err)
	}
	if input.read {
		t.Fatal("extra-argument enroll read stdin")
	}
	for _, secret := range []string{"ect_first_secret", "ect_second_secret"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(stderr, secret) {
			t.Fatalf("extra-argument error exposed token %q: stderr=%q err=%v", secret, stderr, err)
		}
	}
}

func TestEnrollForceWorksForFirstAndReplacement(t *testing.T) {
	stateDir, api := startFakeControlAPI(t, "")
	stdout, stderr, err := runForTest(t, strings.NewReader("first\n"), "--state-dir", stateDir, "enroll", "--force")
	if err != nil || !strings.Contains(stdout, "ec_enrolled_first") || stderr != "" {
		t.Fatalf("first force stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = runForTest(t, strings.NewReader("replacement\n"), "--state-dir", stateDir, "enroll", "--force")
	if err != nil || !strings.Contains(stdout, "ec_enrolled_replacement") || stderr != "" {
		t.Fatalf("replacement force stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.enrollCalls != 2 || !slicesEqual(api.tokens, []string{"first", "replacement"}) {
		t.Fatalf("enroll calls=%d tokens=%v", api.enrollCalls, api.tokens)
	}
}

func TestPlainReplacementDefaultsNoAndNonTTYFailsClosed(t *testing.T) {
	stateDir, api := startFakeControlAPI(t, "ec_old")
	input, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	oldIsTerminal := isTerminal
	isTerminal = func(int) bool { return true }
	t.Cleanup(func() { isTerminal = oldIsTerminal })
	stdout, stderr, err := runForTest(t, input, "--state-dir", stateDir, "enroll")
	if err != nil || stdout != "Enrollment unchanged.\n" || !strings.Contains(stderr, "[y/N]") {
		t.Fatalf("default-no stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	api.mu.Lock()
	calls := api.enrollCalls
	api.mu.Unlock()
	if calls != 0 {
		t.Fatalf("default-no enrollment calls = %d", calls)
	}

	isTerminal = func(int) bool { return false }
	_, _, err = runForTest(t, strings.NewReader("must-not-be-read\n"), "--state-dir", stateDir, "enroll")
	if err == nil || !strings.Contains(err.Error(), "non-interactive replacement requires --force") {
		t.Fatalf("non-TTY replacement error = %v", err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.enrollCalls != 0 {
		t.Fatalf("non-TTY plain enrollment calls = %d", api.enrollCalls)
	}
}

type readTrackingReader struct{ read bool }

func (reader *readTrackingReader) Read([]byte) (int, error) {
	reader.read = true
	return 0, io.EOF
}

func TestCLIEnrollmentPreflightsSocketBeforeTokenRead(t *testing.T) {
	reader := &readTrackingReader{}
	_, _, err := runForTest(t, reader, "--state-dir", t.TempDir(), "enroll", "--force")
	if !errors.Is(err, execonnect.ErrServeNotRunning) {
		t.Fatalf("enroll error = %v", err)
	}
	if reader.read {
		t.Fatal("CLI read token before daemon socket preflight")
	}
}

func TestServeUnenrolledGuidance(t *testing.T) {
	var output bytes.Buffer
	writeServeEvent(log.New(&output, "", 0), execonnect.ServeEvent{Kind: execonnect.ServeStarted, EndpointCount: 2}, false)
	for _, want := range []string{
		"daemon started unenrolled with 2 endpoints",
		"https://exe.dev/integrations#external-connections",
		"run `execonnect enroll` in another terminal",
		"execonnect endpoint add NAME URL",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("serve output missing %q: %q", want, output.String())
		}
	}
}

func TestEmptyEndpointListShowsNextStep(t *testing.T) {
	var output bytes.Buffer
	if err := writeEndpointList(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "No endpoints configured.\nNext: execonnect endpoint add NAME URL\n"; got != want {
		t.Fatalf("empty endpoint output = %q, want %q", got, want)
	}
}

func TestStatusFormattingShowsFirstRunNextSteps(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status execonnect.LocalStatus
		want   string
	}{
		{
			name: "unenrolled",
			status: execonnect.LocalStatus{
				Service: "running", Enrollment: "unenrolled", Control: "inactive", Tunnel: "inactive",
			},
			want: "Next: open https://exe.dev/integrations#external-connections and run execonnect enroll.",
		},
		{
			name: "enrolled without endpoints",
			status: execonnect.LocalStatus{
				Service: "running", Enrollment: "enrolled", Enrolled: true, ExternalConnectionID: "ec_status", ExedURL: execonnect.DefaultExedURL, Control: "connected", Tunnel: "active",
			},
			want: "Next: execonnect endpoint add NAME URL",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeStatus(&output, testCase.status); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), testCase.want) {
				t.Fatalf("status output missing %q: %s", testCase.want, output.String())
			}
		})
	}
}

func TestStatusFormattingCoversStructuredFields(t *testing.T) {
	var output bytes.Buffer
	err := writeStatus(&output, execonnect.LocalStatus{
		Service:              "running",
		Enrollment:           "enrolled",
		Enrolled:             true,
		ExternalConnectionID: "ec_status",
		ExedURL:              "https://exe.dev",
		EndpointCount:        1,
		Control:              "error",
		Tunnel:               "inactive",
		Error:                "authentication rejected",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Service: running", "Enrollment: enrolled", "External connection ID: ec_status",
		"Endpoint count: 1", "Control: error", "Tunnel: inactive", "Error: authentication rejected",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status output missing %q: %s", want, output.String())
		}
	}
}

func TestGRPCErrorsAreConciseUnlessServeIsVerbose(t *testing.T) {
	enrollmentErr := status.Error(codes.Unauthenticated, "secret enrollment detail")
	if got := conciseEnrollmentError(enrollmentErr).Error(); got != "enrollment token was rejected or already used (Unauthenticated)" {
		t.Fatalf("enrollment error = %q", got)
	}
	err := status.Error(codes.Unauthenticated, "secret server detail")
	if got := controlErrorText(err, false); got != "authentication rejected (Unauthenticated)" {
		t.Fatalf("concise error = %q", got)
	}
	if got := controlErrorText(err, true); !strings.Contains(got, "secret server detail") {
		t.Fatalf("verbose error = %q", got)
	}
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

func runForTest(t *testing.T, stdin io.Reader, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), args, stdin, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}
