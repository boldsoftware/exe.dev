package testinfra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SSHPiperdInstance describes the running sshpiperd process.
type SSHPiperdInstance struct {
	Exited <-chan struct{} // closed when sshpiperd exits
	Cause  func() error    // why context was canceled
	Cmd    *exec.Cmd       // sshpiperd command
	Port   int             // port that sshpiperd listens on
}

// StartSSHPiperd starts the SSH piperd process.
//
// sshPiperPluginPort is the port on the local host where exed is running
// a grpc server that acts as an sshpiperd plugin.
//
// logFile, is not nil, is a file to write logs to.
func StartSSHPiperd(ctx context.Context, sshPiperPluginPort int, logFile io.Writer) (*SSHPiperdInstance, error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting piperd")

	tmpFile, err := os.CreateTemp("", "sshpiperd_test_key_*.pem")
	if err != nil {
		return nil, err
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Start piperd process and capture its output
	piperdCmd := exec.Command("go", "run", "-race", "./cmd/sshpiperd",
		"--log-format", "json",
		"--log-level", "debug",
		"--port", "0",
		"--drop-hostkeys-message",
		"--address=0.0.0.0",
		"--server-key-generate-mode", "always",
		"--server-key", tmpFile.Name(),
		"grpc",
		"--endpoint=localhost:"+strconv.Itoa(sshPiperPluginPort),
		"--insecure",
	)

	srcdir, err := exeRootDir()
	if err != nil {
		return nil, err
	}
	piperdCmd.Dir = filepath.Join(srcdir, "deps", "sshpiper") // run from sshpiper dir so it finds its go.mod

	// Start piperd process and capture its output
	cmdOut, err := piperdCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	piperdCmd.Stderr = piperdCmd.Stdout

	if err := piperdCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start sshpiperd: %w", err)
	}

	// Parse output to find ports
	var teeMu sync.Mutex
	tee := new(strings.Builder)
	sshPortC := make(chan int, 1)
	sshErrorC := make(chan error, 1)
	go func() {
		scan := bufio.NewScanner(cmdOut)
		found := false
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.WriteString("\n")
			teeMu.Unlock()

			if logFile != nil {
				fmt.Fprintf(logFile, "%s\n", line)
			}

			// Parse JSON log line
			if !json.Valid(line) {
				// TODO: log when non-JSON lines are seen?
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				slog.ErrorContext(ctx, "failed to parse sshpiper log line", "error", err, "line", string(line))
				continue
			}
			switch entry["msg"] {
			case "sshpiperd is listening":
				port, ok := entry["port"].(float64)
				if ok {
					sshPortC <- int(port)
				} else {
					sshErrorC <- fmt.Errorf("failed to get SSH port from sshpiperd log entry: %v", entry)
				}
				found = true

				// TODO: Collect any other sshpiperd output
				break
			}
		}
		if !found {
			teeMu.Lock()
			out := tee.String()
			teeMu.Unlock()
			sshErrorC <- fmt.Errorf("sshpiperd never reported listening, output:\n%s", out)
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			fmt.Fprintf(os.Stderr, "sshpiperd scan error: %v\n", err)
		}
	}()

	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var sshPort int
	select {
	case sshPort = <-sshPortC:
	case err := <-sshErrorC:
		return nil, err
	case <-time.After(timeout):
		teeMu.Lock()
		out := tee.String()
		teeMu.Unlock()
		return nil, fmt.Errorf("timeout waiting for piperd to start. output:\n%s", out)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	go func() {
		piperdCmd.Wait()
		cancel()
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(cmdCtx)
	})

	instance := &SSHPiperdInstance{
		Exited: cmdCtx.Done(),
		Cause:  cause,
		Cmd:    piperdCmd,
		Port:   sshPort,
	}

	slog.InfoContext(ctx, "started piperd", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// Stop stops the piperd process.
func (si *SSHPiperdInstance) Stop(ctx context.Context) {
	si.Cmd.Process.Kill()
	<-si.Exited
}
