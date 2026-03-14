package testinfra

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ExepipeInstance describes a single running exepipe.
type ExepipeInstance struct {
	BinPath  string          // exepipe binary
	Exited   <-chan struct{} // closed when exepipe exits
	Cause    func() error    // why context was canceled
	Cmd      *exec.Cmd       // exepipe command
	UnixAddr string          // exepipe server address for commands
	HTTPPort int             // exepipe HTTP local port
}

// StartExepipe starts an exepipe process.
//
// logFile, if not nil, is a file to write logs to.
func StartExepipe(ctx context.Context, logFile io.Writer) (*ExepipeInstance, error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting exepipe")

	// If PREBUILT_EXEPIPE is set, use it.
	// Otherwise build a new binary.
	var binPath string
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
		AddCleanup(func() { os.Remove(binPath) })
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

	exepipeCmd.Stdout = logFile
	exepipeCmd.Stderr = logFile

	if err := exepipeCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exepipe: %w", err)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	go func() {
		exepipeCmd.Wait()
		cancel()
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(cmdCtx)
	})

	instance := &ExepipeInstance{
		BinPath:  binPath,
		Exited:   cmdCtx.Done(),
		Cause:    cause,
		Cmd:      exepipeCmd,
		UnixAddr: unixAddr,
	}

	AddCanonicalization(instance.UnixAddr, "EXEPIPE_UNIX_ADDRESS")

	slog.InfoContext(ctx, "started exepipe", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// Stop stops the exepipe process.
// This does not return an error; errors are just logged.
func (ep *ExepipeInstance) Stop(ctx context.Context) {
	if ep.Cmd != nil {
		slog.InfoContext(ctx, "sending SIGTERM to exepipe")
		ep.Cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait briefly before using SIGKILL.
	select {
	case <-ep.Exited:
	case <-time.After(1 * time.Second):
		if ep.Cmd != nil {
			ep.Cmd.Process.Kill()
			<-ep.Exited
		}
	}
}
