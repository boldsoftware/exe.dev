package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const cliHelperEnvironment = "EXECONNECT_CLI_HELPER"

type fakeExed struct {
	connectapi.UnimplementedExternalConnectionServiceServer
	enrollCalls  atomic.Int64
	connectCalls atomic.Int64
	connectCode  codes.Code
}

func (server *fakeExed) Enroll(_ context.Context, request *connectapi.EnrollRequest) (*connectapi.EnrollResponse, error) {
	server.enrollCalls.Add(1)
	if request.GetEnrollmentToken() != "valid-token" {
		return nil, status.Error(codes.Unauthenticated, "invalid or used enrollment token")
	}
	return &connectapi.EnrollResponse{
		ExternalConnectionID: "ec_cli_test",
		ConnectorSecret:      "ecs_cli_test",
	}, nil
}

func (server *fakeExed) Connect(grpc.BidiStreamingServer[connectapi.ConnectorMessage, connectapi.ControlMessage]) error {
	server.connectCalls.Add(1)
	return status.Error(server.connectCode, "connector credential rejected")
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv(cliHelperEnvironment) == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(os.Getenv("EXECONNECT_CLI_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"execonnect"}, args...)
	main()
}

func TestRootHelpAndTypoExitContracts(t *testing.T) {
	help := runCLI(t, "", "")
	if help.exitCode != 0 || help.stderr != "" || !strings.Contains(help.stdout, "USAGE:") {
		t.Fatalf("bare execonnect = %#v", help)
	}

	typo := runCLI(t, "", "", "endroll")
	if typo.exitCode != 1 || typo.stdout != "" || !strings.Contains(typo.stderr, `did you mean "enroll"`) {
		t.Fatalf("typo execonnect = %#v", typo)
	}
}

func TestDaemonFirstCLIAndTerminalAuthFailure(t *testing.T) {
	server, caFile := startFakeExed(t)
	stateDir := t.TempDir()
	daemon := startDaemonCLI(t, caFile, stateDir)

	add := runCLI(t, caFile, "", "--state-dir", stateDir, "endpoint", "add", "database", "tcp://db.internal:5432")
	if add.exitCode != 0 || !strings.Contains(add.stdout, "Added endpoint database") {
		t.Fatalf("unenrolled add = %#v", add)
	}
	list := runCLI(t, caFile, "", "--state-dir", stateDir, "endpoint", "list")
	if list.exitCode != 0 || !strings.Contains(list.stdout, "database") {
		t.Fatalf("unenrolled list = %#v", list)
	}

	enroll := runCLI(t, caFile, "valid-token\n", "--state-dir", stateDir, "enroll", "--force", "--exed-url", server.URL)
	if enroll.exitCode != 0 || !strings.Contains(enroll.stdout, "Enrolled external connection ec_cli_test") {
		t.Fatalf("enroll = %#v", enroll)
	}
	daemon.waitForLine(t, "daemon remains available for enrollment")

	statusResult := runCLI(t, caFile, "", "--state-dir", stateDir, "status")
	if statusResult.exitCode != 0 {
		t.Fatalf("status = %#v", statusResult)
	}
	for _, want := range []string{
		"Service: running", "Enrollment: enrolled", "External connection ID: ec_cli_test",
		"Endpoint count: 1", "Control: error", "Tunnel: inactive", "Unauthenticated",
	} {
		if !strings.Contains(statusResult.stdout, want) {
			t.Fatalf("status output missing %q: %s", want, statusResult.stdout)
		}
	}
	if server.enrollCalls.Load() != 1 || server.connectCalls.Load() != 1 {
		t.Fatalf("remote calls enroll=%d connect=%d", server.enrollCalls.Load(), server.connectCalls.Load())
	}
}

func TestEnrollmentRejectionLeavesDaemonUnenrolled(t *testing.T) {
	server, caFile := startFakeExed(t)
	stateDir := t.TempDir()
	startDaemonCLI(t, caFile, stateDir)
	result := runCLI(t, caFile, "invalid-token\n", "--state-dir", stateDir, "enroll", "--force", "--exed-url", server.URL)
	if result.exitCode != 1 || !strings.Contains(result.stderr, "enrollment token was rejected or already used (Unauthenticated)") {
		t.Fatalf("rejected enrollment = %#v", result)
	}
	statusResult := runCLI(t, caFile, "", "--state-dir", stateDir, "status")
	if statusResult.exitCode != 0 || !strings.Contains(statusResult.stdout, "Enrollment: unenrolled") {
		t.Fatalf("status after rejection = %#v", statusResult)
	}
}

type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCLI(t *testing.T, caFile, stdin string, args ...string) cliResult {
	t.Helper()
	command := helperCommand(t, caFile, args...)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("run CLI: %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

type runningDaemon struct {
	command *exec.Cmd
	lines   chan string
}

func startDaemonCLI(t *testing.T, caFile, stateDir string) *runningDaemon {
	t.Helper()
	command := helperCommand(t, caFile, "--state-dir", stateDir, "serve")
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	daemon := &runningDaemon{command: command, lines: make(chan string, 32)}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			daemon.lines <- scanner.Text()
		}
		close(daemon.lines)
	}()
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Signal(os.Interrupt)
		}
		if err := command.Wait(); err != nil {
			t.Errorf("daemon exit: %v", err)
		}
	})
	daemon.waitForLine(t, "daemon started unenrolled")
	return daemon
}

func (daemon *runningDaemon) waitForLine(t *testing.T, want string) {
	t.Helper()
	for line := range daemon.lines {
		if strings.Contains(line, want) {
			return
		}
	}
	t.Fatalf("daemon exited before logging %q", want)
}

func helperCommand(t *testing.T, caFile string, args ...string) *exec.Cmd {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIHelperProcess$")
	command.Env = append(
		os.Environ(),
		cliHelperEnvironment+"=1",
		"EXECONNECT_CLI_ARGS="+base64.StdEncoding.EncodeToString(data),
		"SSL_CERT_FILE="+caFile,
	)
	return command
}

type fakeExedServer struct {
	*httptest.Server
	*fakeExed
}

func startFakeExed(t *testing.T) (*fakeExedServer, string) {
	t.Helper()
	service := &fakeExed{connectCode: codes.Unauthenticated}
	grpcServer := grpc.NewServer()
	connectapi.RegisterExternalConnectionServiceServer(grpcServer, service)
	httpServer := httptest.NewUnstartedServer(grpcServer)
	httpServer.EnableHTTP2 = true
	httpServer.StartTLS()
	t.Cleanup(func() {
		httpServer.Close()
		grpcServer.Stop()
	})

	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: httpServer.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	return &fakeExedServer{Server: httpServer, fakeExed: service}, caFile
}
