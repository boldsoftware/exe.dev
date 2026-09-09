//go:build linux

package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestEnrollmentInterruptRestoresTerminal(t *testing.T) {
	_, caFile := startFakeExed(t)
	stateDir := t.TempDir()
	startDaemonCLI(t, caFile, stateDir)

	command := helperCommand(t, caFile, "--state-dir", stateDir, "enroll", "--force")
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	output := readPTYUntil(t, terminal, "Enrollment token: ")
	var state *unix.Termios
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err = unix.IoctlGetTermios(int(terminal.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		if state.Lflag&unix.ECHO == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal echo remained enabled during hidden input")
		}
		runtime.Gosched()
	}

	if _, err := terminal.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
		t.Fatalf("interrupt error = %v", err)
	}
	state, err = unix.IoctlGetTermios(int(terminal.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if state.Lflag&unix.ECHO == 0 {
		t.Fatal("terminal echo was not restored after interrupt")
	}

	remaining := make([]byte, 1024)
	terminal.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		count, readErr := terminal.Read(remaining)
		output = append(output, remaining[:count]...)
		if readErr != nil {
			break
		}
	}
	text := string(output)
	for _, want := range []string{
		"https://exe.dev/integrations#external-connections",
		"Press Ctrl+C to cancel.",
		"Enrollment token: ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("terminal output missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "read enrollment token") || strings.Contains(text, "execonnect: context canceled") {
		t.Fatalf("terminal output contains misleading cancellation error: %q", text)
	}
	if !bytes.HasSuffix(bytes.ReplaceAll(output, []byte("\r"), nil), []byte("\n")) {
		t.Fatalf("terminal output does not end in a clean newline: %q", text)
	}
}

func TestReplacementConfirmationInterruptIsClean(t *testing.T) {
	server, caFile := startFakeExed(t)
	stateDir := t.TempDir()
	startDaemonCLI(t, caFile, stateDir)
	initial := runCLI(t, caFile, "valid-token\n", "--state-dir", stateDir, "enroll", "--force", "--exed-url", server.URL)
	if initial.exitCode != 0 {
		t.Fatalf("initial enrollment = %#v", initial)
	}

	command := helperCommand(t, caFile, "--state-dir", stateDir, "enroll", "--exed-url", server.URL)
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	output := readPTYUntil(t, terminal, "[y/N]: ")
	if _, err := terminal.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
		t.Fatalf("interrupt error = %v", err)
	}
	remaining := make([]byte, 1024)
	terminal.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		count, readErr := terminal.Read(remaining)
		output = append(output, remaining[:count]...)
		if readErr != nil {
			break
		}
	}
	text := string(output)
	if strings.Contains(text, "read replacement confirmation") || strings.Contains(text, "execonnect: context canceled") {
		t.Fatalf("terminal output contains misleading cancellation error: %q", text)
	}
	if !bytes.HasSuffix(bytes.ReplaceAll(output, []byte("\r"), nil), []byte("\n")) {
		t.Fatalf("terminal output does not end in a clean newline: %q", text)
	}
}

func readPTYUntil(t *testing.T, terminal interface {
	Read([]byte) (int, error)
	SetReadDeadline(time.Time) error
}, want string,
) []byte {
	t.Helper()
	if err := terminal.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var output []byte
	buffer := make([]byte, 256)
	for !bytes.Contains(output, []byte(want)) {
		count, err := terminal.Read(buffer)
		output = append(output, buffer[:count]...)
		if err != nil {
			t.Fatalf("read terminal waiting for %q: %v; output %q", want, err, output)
		}
	}
	return output
}

func TestLocalCommandInterruptCancelsRPC(t *testing.T) {
	for _, args := range [][]string{
		{"status"},
		{"endpoint", "list"},
		{"endpoint", "add", "database", "tcp://db.internal:5432"},
		{"endpoint", "update", "database", "tcp://db.internal:6432"},
		{"endpoint", "remove", "database"},
		{"enroll", "--force", "token"},
	} {
		t.Run(strings.Join(args[:min(len(args), 2)], "-"), func(t *testing.T) {
			socketPath := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			requested := make(chan struct{})
			server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				close(requested)
				<-request.Context().Done()
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			command := helperCommand(t, "", append([]string{"--socket", socketPath}, args...)...)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = command.Wait()
				close(done)
			}()
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					_ = command.Process.Kill()
					<-done
				}
			})
			select {
			case <-requested:
			case <-done:
				t.Fatalf("command exited before its RPC: %v; %s", waitErr, stderr.String())
			}
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Ctrl+C did not cancel the local RPC")
			}
			var exitError *exec.ExitError
			if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 130 {
				t.Fatalf("interrupt error = %v; %s", waitErr, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("interrupt printed an error: %s", stderr.String())
			}
		})
	}
}
