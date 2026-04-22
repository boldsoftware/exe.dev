package execore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/exemenu"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"github.com/tg123/sshpiper/libplugin"
	"google.golang.org/grpc"
)

// fakeLogsComputeServer extends fakeComputeServer with a canned log stream for
// GetInstanceLogs. GetInstance reports the VM as stopped (via the embedded
// fakeComputeServer's started flag, which starts false) so handleBoxAccess
// takes the not-running branch when we want it to.
type fakeLogsComputeServer struct {
	fakeComputeServer
	logs []string
}

func (f *fakeLogsComputeServer) GetInstanceLogs(_ *computeapi.GetInstanceLogsRequest, stream grpc.ServerStreamingServer[computeapi.GetInstanceLogsResponse]) error {
	for _, line := range f.logs {
		if err := stream.Send(&computeapi.GetInstanceLogsResponse{
			Log: &computeapi.Log{Message: line},
		}); err != nil {
			return err
		}
	}
	return nil
}

// startFakeExeletWithLogs starts a gRPC server with fakeLogsComputeServer and
// returns the client address plus the server (for toggling state).
func startFakeExeletWithLogs(t *testing.T, logs []string) (string, *exeletclient.Client, *fakeLogsComputeServer) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := &fakeLogsComputeServer{
		fakeComputeServer: fakeComputeServer{sshPort: 2222},
		logs:              logs,
	}

	gs := grpc.NewServer()
	computeapi.RegisterComputeServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	addr := fmt.Sprintf("tcp://%s", lis.Addr().String())
	client, err := exeletclient.NewClient(addr, exeletclient.WithInsecure())
	if err != nil {
		t.Fatalf("failed to create exelet client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return addr, client, srv
}

// setupStoppedBoxWithLogs creates a test server, a fake exelet returning the
// given log lines, and a stopped box owned by a fresh test user. Returns the
// server, the exelet address, and the owner user ID.
func setupStoppedBoxWithLogs(t *testing.T, boxName string, logs []string) (*Server, string, string) {
	t.Helper()
	server := newTestServer(t)
	ctx := context.Background()

	addr, client, _ := startFakeExeletWithLogs(t, logs)
	server.exeletClients[addr] = &exeletClient{addr: addr, client: client}
	server.exeletClients[addr].up.Store(true)

	userID := createTestUser(t, server, boxName+"@example.com")

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:              userID,
		ctrhost:             addr,
		name:                boxName,
		image:               "ubuntu:latest",
		noShard:             true,
		region:              "pdx",
		allocatedCPUs:       2,
		memoryCapacityBytes: 0,
		diskCapacityBytes:   0,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-" + boxName
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "stopped",
			SSHPort:     nil,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	return server, addr, userID
}

// TestSanitizeVMLogLine verifies that control characters are stripped from log
// lines before they are written to the REPL. The REPL is an interactive
// terminal, so a stopped VM that emits ANSI escape sequences (cursor moves,
// color resets, OSC clipboard writes, etc.) could otherwise scribble on the
// user's terminal when they run `vm-logs`.
func TestSanitizeVMLogLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ascii passes through", in: "ERROR: disk full", want: "ERROR: disk full"},
		{name: "tabs are preserved", in: "key:\tvalue", want: "key:\tvalue"},
		{name: "csi escape stripped", in: "\x1b[2Joops", want: "\ufffd[2Joops"},
		{name: "bell stripped", in: "ding\x07dong", want: "ding\ufffddong"},
		{name: "nul and del stripped", in: "a\x00b\x7fc", want: "a\ufffdb\ufffdc"},
		{name: "carriage return stripped", in: "line one\rline two", want: "line one\ufffdline two"},
		{name: "osc clipboard write stripped", in: "\x1b]52;c;aGVsbG8=\x07after", want: "\ufffd]52;c;aGVsbG8=\ufffdafter"},
		{name: "invalid utf8 becomes replacement", in: "ok\xffbad", want: "ok\ufffdbad"},
		{name: "unicode passes through", in: "héllo 世界", want: "héllo 世界"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeVMLogLine(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeVMLogLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTailVMLogLines covers the line-splitting and tail-truncation behavior
// of tailVMLogLines.
func TestTailVMLogLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		n    int
		want []string
	}{
		{name: "empty buffer", in: "", n: 100, want: nil},
		{name: "single line no newline", in: "hello", n: 100, want: []string{"hello"}},
		{name: "single line trailing newline", in: "hello\n", n: 100, want: []string{"hello"}},
		{name: "multiple lines", in: "one\ntwo\nthree\n", n: 100, want: []string{"one", "two", "three"}},
		{name: "multiple lines no trailing newline", in: "one\ntwo\nthree", n: 100, want: []string{"one", "two", "three"}},
		{name: "blank lines preserved internally", in: "one\n\nthree\n", n: 100, want: []string{"one", "", "three"}},
		{name: "tail truncation keeps last n", in: "a\nb\nc\nd\ne\n", n: 3, want: []string{"c", "d", "e"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tailVMLogLines([]byte(tt.in), tt.n)
			if len(got) != len(tt.want) {
				t.Fatalf("tailVMLogLines(%q, %d) returned %d lines, want %d: %q", tt.in, tt.n, len(got), len(tt.want), got)
			}
			for i, line := range got {
				if line != tt.want[i] {
					t.Errorf("tailVMLogLines(%q, %d)[%d] = %q, want %q", tt.in, tt.n, i, line, tt.want[i])
				}
			}
		})
	}
}

// TestStoppedVMBannerPointsAtVMLogs verifies that the banner shown when an
// owner hits a stopped VM points them at the authenticated `vm-logs <name>`
// REPL command, and does NOT attempt to embed raw container output. The
// banner is emitted as an SSH_MSG_USERAUTH_BANNER on a failing auth attempt,
// so we deliberately keep it free of VM-controlled content.
func TestStoppedVMBannerPointsAtVMLogs(t *testing.T) {
	t.Parallel()

	// Include some would-be-hostile content to prove the banner doesn't
	// reach for the container log stream at all.
	logLines := []string{
		"\x1b[2Jhijacked\x1b[H",
		"ERROR: disk full: cannot write rootfs",
	}
	server, _, userID := setupStoppedBoxWithLogs(t, "broken-vm", logLines)
	ctx := context.Background()

	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "broken-vm")
	if err != nil {
		t.Fatal(err)
	}

	piper := NewPiperPlugin(server, "127.0.0.1", 0)
	t.Cleanup(piper.Stop)

	piperCtx, _ := withPiperConnLog(ctx)
	upstream, err := piper.handleBoxAccess(piperCtx, &box, userID, "test-conn-stopped")
	if err == nil {
		t.Fatal("expected handleBoxAccess to fail for stopped VM, got nil error")
	}
	if upstream != nil {
		t.Errorf("expected nil upstream for stopped VM, got %+v", upstream)
	}

	var denial *libplugin.AuthDenialError
	if !errors.As(err, &denial) {
		t.Fatalf("expected *libplugin.AuthDenialError, got %T: %v", err, err)
	}
	if !strings.Contains(denial.Banner, `"broken-vm"`) {
		t.Errorf("denial banner should mention VM name, got: %q", denial.Banner)
	}
	if !strings.Contains(denial.Banner, "vm-logs broken-vm") {
		t.Errorf("denial banner should point at `vm-logs broken-vm`, got: %q", denial.Banner)
	}
	if !strings.Contains(denial.Banner, "rm broken-vm") {
		t.Errorf("denial banner should point at `rm broken-vm` as the delete escape hatch, got: %q", denial.Banner)
	}
	// No log content should leak into the banner. "ERROR" and the escape
	// sequence above both came from the container's (fake) log stream; if
	// either appears, the banner reached into exelet when it should not have.
	if strings.Contains(denial.Banner, "ERROR") {
		t.Errorf("denial banner should not contain container log content, got:\n%s", denial.Banner)
	}
	if strings.Contains(denial.Banner, "hijacked") {
		t.Errorf("denial banner should not contain container log content, got:\n%s", denial.Banner)
	}
}

// TestStoppedVMBannerNoContainerID verifies that an owner who reaches a box
// with no associated container (e.g. it was reaped, or was never created)
// still gets a denial banner pointing at the recovery commands.
func TestStoppedVMBannerNoContainerID(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx := context.Background()

	addr, client, _ := startFakeExeletWithLogs(t, nil)
	server.exeletClients[addr] = &exeletClient{addr: addr, client: client}
	server.exeletClients[addr].up.Store(true)

	userID := createTestUser(t, server, "ghost-vm@example.com")
	_, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:              userID,
		ctrhost:             addr,
		name:                "ghost-vm",
		image:               "ubuntu:latest",
		noShard:             true,
		region:              "pdx",
		allocatedCPUs:       2,
		memoryCapacityBytes: 0,
		diskCapacityBytes:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately *don't* call UpdateBoxContainerAndStatus, so ContainerID
	// stays nil — the situation handleBoxAccess's nil-container branch handles.

	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "ghost-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.ContainerID != nil {
		t.Fatalf("expected nil ContainerID, got %q", *box.ContainerID)
	}

	piper := NewPiperPlugin(server, "127.0.0.1", 0)
	t.Cleanup(piper.Stop)

	piperCtx, _ := withPiperConnLog(ctx)
	_, err = piper.handleBoxAccess(piperCtx, &box, userID, "test-conn-no-container")
	if err == nil {
		t.Fatal("expected handleBoxAccess to fail for VM with no container")
	}

	var denial *libplugin.AuthDenialError
	if !errors.As(err, &denial) {
		t.Fatalf("expected *libplugin.AuthDenialError, got %T: %v", err, err)
	}
	if !strings.Contains(denial.Banner, `"ghost-vm"`) {
		t.Errorf("denial banner should mention VM name, got: %q", denial.Banner)
	}
	if !strings.Contains(denial.Banner, "vm-logs ghost-vm") {
		t.Errorf("denial banner should point at `vm-logs ghost-vm`, got: %q", denial.Banner)
	}
	if !strings.Contains(denial.Banner, "rm ghost-vm") {
		t.Errorf("denial banner should point at `rm ghost-vm`, got: %q", denial.Banner)
	}
}

// TestVMLogsCommandStreamsLogsForOwner verifies that the authenticated
// `vm-logs` command streams container logs to the owner, splits exelet's
// single-chunk log payload into lines, and sanitizes embedded escape
// sequences.
func TestVMLogsCommandStreamsLogsForOwner(t *testing.T) {
	t.Parallel()

	// Exelet ships raw log-file chunks, not one-message-per-line, so the
	// production wire shape is a few large messages with embedded newlines.
	// Include an ANSI escape to prove sanitization runs before we print.
	logLines := []string{
		"starting exelet instance\npulling ubuntu:latest\nERROR: \x1b[31mdisk full\x1b[0m\nexiting with status 1\n",
	}
	server, _, userID := setupStoppedBoxWithLogs(t, "broken-vm", logLines)
	ctx := context.Background()

	ss := &SSHServer{server: server}
	output := &MockOutput{}
	cc := &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: userID, Email: "broken-vm@example.com"},
		Args:   []string{"broken-vm"},
		Output: output,
	}
	if err := ss.handleVMLogsCommand(ctx, cc); err != nil {
		t.Fatalf("handleVMLogsCommand failed: %v", err)
	}

	out := output.String()
	for _, want := range []string{
		"starting exelet instance",
		"pulling ubuntu:latest",
		"disk full",
		"exiting with status 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("vm-logs output missing line %q; got:\n%s", want, out)
		}
	}
	// Sanitization must strip the ANSI color escape from the log line
	// itself. The handler emits its own ANSI sequences for the header and
	// footer, so we can't just scan for `\x1b`. Instead, check for the
	// specific sanitized form we expect to see on the ERROR line: the ESC
	// bytes from the container's logs should have been replaced with
	// U+FFFD, leaving `�[31mdisk full�[0m` in the output.
	if !strings.Contains(out, "\ufffd[31mdisk full\ufffd[0m") {
		t.Errorf("vm-logs output did not sanitize container-provided ANSI escapes; got:\n%s", out)
	}
}

// TestVMLogsCommandJSON verifies the --json output shape.
func TestVMLogsCommandJSON(t *testing.T) {
	t.Parallel()

	logLines := []string{"line one\nline two\n"}
	server, _, userID := setupStoppedBoxWithLogs(t, "json-vm", logLines)
	ctx := context.Background()

	ss := &SSHServer{server: server}
	output := &MockOutput{}
	cc := &exemenu.CommandContext{
		User:      &exemenu.UserInfo{ID: userID, Email: "json-vm@example.com"},
		Args:      []string{"json-vm"},
		Output:    output,
		ForceJSON: true,
	}
	if err := ss.handleVMLogsCommand(ctx, cc); err != nil {
		t.Fatalf("handleVMLogsCommand failed: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, `"vm_name":"json-vm"`) && !strings.Contains(out, `"vm_name": "json-vm"`) {
		t.Errorf("vm-logs JSON output missing vm_name; got: %s", out)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Errorf("vm-logs JSON output missing log lines; got: %s", out)
	}
}

// TestVMLogsCommandDeniesNonOwner verifies that a random user who does not
// own the VM and has no team relationship gets a "not found" error, not the
// logs. The error message must not disclose whether the VM exists.
func TestVMLogsCommandDeniesNonOwner(t *testing.T) {
	t.Parallel()

	server, _, _ := setupStoppedBoxWithLogs(t, "private-vm", []string{"secret log line"})
	ctx := context.Background()

	intruderID := createTestUser(t, server, "intruder@example.com")

	ss := &SSHServer{server: server}
	output := &MockOutput{}
	cc := &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: intruderID, Email: "intruder@example.com"},
		Args:   []string{"private-vm"},
		Output: output,
	}
	err := ss.handleVMLogsCommand(ctx, cc)
	if err == nil {
		t.Fatal("expected handleVMLogsCommand to fail for non-owner, got nil error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
	if strings.Contains(output.String(), "secret log line") {
		t.Errorf("intruder should not see log contents; got output:\n%s", output.String())
	}
}

// TestVMLogsCommandWrongArgs verifies argument validation.
func TestVMLogsCommandWrongArgs(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	userID := createTestUser(t, server, "argcheck@example.com")

	ss := &SSHServer{server: server}

	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		output := &MockOutput{}
		cc := &exemenu.CommandContext{
			User:   &exemenu.UserInfo{ID: userID, Email: "argcheck@example.com"},
			Args:   args,
			Output: output,
		}
		if err := ss.handleVMLogsCommand(context.Background(), cc); err == nil {
			t.Errorf("handleVMLogsCommand(%v): expected error, got nil", args)
		}
	}
}
