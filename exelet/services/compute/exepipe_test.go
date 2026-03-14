package compute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	ei, err := startExepipe(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	defer func() {
		ch := make(chan bool)
		go func() {
			ei.Cmd.Wait()
			close(ch)
		}()
		ei.Cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-ch:
		case <-time.After(1 * time.Second):
			ei.Cmd.Process.Kill()
			<-ch
		}
		<-ei.LoggerDone
		if ei.RmPath != "" {
			os.Remove(ei.RmPath)
		}
	}()

	exepipe = ei

	m.Run()
}

type exepipeInstance struct {
	RmPath     string
	Cmd        *exec.Cmd
	UnixAddr   string
	LoggerDone chan bool
}

var exepipe *exepipeInstance

func startExepipe(ctx context.Context) (*exepipeInstance, error) {
	// If PREBUILT_EXEPIPE is set, use it.
	// Otherwise build a new binary.
	var binPath, rmPath string
	if prebuilt := os.Getenv("PREBUILT_EXEPIPE"); prebuilt != "" {
		st, err := os.Stat(prebuilt)
		if err != nil {
			return nil, fmt.Errorf("PREBUILT_EXEPIPE not usable: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("PREBUILT_EXEPIPE points to a directory, need a file: %s", prebuilt)
		}
		binPath = prebuilt
	} else {
		bin, err := os.CreateTemp("", "exepipe-test")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		bin.Close()
		binPath = bin.Name()
		rootDir, err := exeRootDir()
		if err != nil {
			return nil, err
		}
		buildCmd := exec.Command("go", "build", "-race", "-o", binPath, "./cmd/exepipe")
		buildCmd.Dir = rootDir
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to build exepipe: %v\n%s", err, out)
		}
		rmPath = binPath
	}

	unixAddr := fmt.Sprintf("@exepipe%08x", rand.Uint32())
	exepipeCmd := exec.Command(binPath,
		"-stage=test",
		"-addr="+unixAddr,
		"-http-port=", // no metrics server
	)

	exepipeCmd.Env = append(
		exepipeCmd.Environ(),
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
	)

	cmdOut, err := exepipeCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	exepipeCmd.Stderr = exepipeCmd.Stdout

	if err := exepipeCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exepipe: %v", err)
	}

	started := make(chan bool)
	loggerDone := make(chan bool)
	var teeMu sync.Mutex
	var tee strings.Builder
	go func() {
		defer close(loggerDone)

		seenPanic := false

		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()

			teeMu.Lock()
			tee.Write(line)
			tee.WriteString("\n")
			teeMu.Unlock()

			if seenPanic {
				fmt.Printf("%s\n", line)
			}

			if !json.Valid(line) {
				if bytes.Contains(line, []byte("panic:")) {
					seenPanic = true
					// Dump what we have so far,
					// and then print as we go.
					teeMu.Lock()
					fmt.Print(tee.String())
					teeMu.Unlock()
				}
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				slog.ErrorContext(ctx, "failed to parse exepipe log file", "error", err, "line", string(line))
				continue
			}

			if entry["msg"] == "server started" {
				close(started)
			}
		}

		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.ErrorContext(ctx, "error scanning exepipe output", "error", err)
		}
	}()

	select {
	case <-started:
	case <-time.After(time.Minute):
		teeMu.Lock()
		out := tee.String()
		teeMu.Unlock()
		return nil, fmt.Errorf("timeout waiting for exepipe to start. Output:\n%s", out)
	}

	instance := &exepipeInstance{
		RmPath:     rmPath,
		Cmd:        exepipeCmd,
		UnixAddr:   unixAddr,
		LoggerDone: loggerDone,
	}
	return instance, nil
}

// exeRootDir returns the root of the source directory.
// We find it by walking up the directory tree until we find "go.mod".
func exeRootDir() (string, error) {
	srcdir := "."
	for range 32 {
		if _, err := os.Stat(filepath.Join(srcdir, "go.mod")); err == nil {
			return srcdir, nil
		}
		srcdir = filepath.Join(srcdir, "..")
	}
	return "", errors.New("could not find go.mod; directory too deep or in wrong directory")
}
