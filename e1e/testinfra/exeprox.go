package testinfra

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ExeproxInstance describes a single running exeprox.
type ExeproxInstance struct {
	Exited     <-chan struct{} // closed when exeprox exits
	Cause      func() error    // why context was canceled
	Cmd        *exec.Cmd       // exeprox command
	HTTPPort   int             // exeprox HTTP local hostport
	ExtraPorts []int           // additional proxy ports
	CoverDir   string          // where coverage data is written
	Errors     chan string     // exeprox error sent on this channel

	exeproxLoggerDone chan bool // closed when logging goroutine done
}

// StartExeprox starts an exeprox process.
//
// exedHTTPPort is the port on which exed handles HTTP.
//
// exedGRPCPort is the port of the exed process's proxy gRPC server.
//
// extraProxyPorts is a list of ports passed to exed
// via the TEST_PROXY_PORTS environment variable.
// The ports can be zero, in which case exed will pick them.
// They are returned in the EXTRA_PORTS field of the
// returned ExedInstance.
//
// logFile, if not nil, is a file to write logs to.
//
// logPorts is whether to log port numbers using slog.InfoContext.
func StartExeprox(ctx context.Context, exedHTTPPort, exedGRPCPort int, extraProxyPorts []int, testRunID string, logFile io.Writer, logPorts bool) (*ExeproxInstance, error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting exeprox")

	// If PREBUILT_EXEPROX is set, use it.
	// Otherwise build a new binary.
	var binPath string
	if prebuilt := os.Getenv("PREBUILT_EXEPROX"); prebuilt != "" {
		st, err := os.Stat(prebuilt)
		if err != nil {
			return nil, fmt.Errorf("PREBUILT_EXEPROX not usable: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("PREBUILT_EXEPROX points to a directory, need a file: %s", prebuilt)
		}
		binPath = prebuilt
	} else {
		bin, err := os.CreateTemp("", "exeprox-test")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		bin.Close()
		binPath = bin.Name()
		rootDir, err := exeRootDir()
		if err != nil {
			return nil, err
		}
		buildCmd := exec.Command("go", "build", "-race", "-cover", "-covermode=atomic", "-coverpkg=exe.dev/...", "-o", binPath, "./cmd/exeprox")
		buildCmd.Dir = rootDir
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to build exeprox: %v\n%s", err, out)
		}
		AddCleanup(func() { os.Remove(binPath) })
	}

	coverDir, err := os.MkdirTemp("", "exeprox-test_cov")
	if err != nil {
		return nil, fmt.Errorf("failed to create coverage dir: %w", err)
	}

	exeproxCmd := exec.Command(binPath,
		"-stage=test",
		"-http=:0",
		"-exed-http-port="+strconv.Itoa(exedHTTPPort),
		"-exed-https-port=0",
		"-exed-grpc-addr=tcp://localhost:"+strconv.Itoa(exedGRPCPort),
	)
	// Convert extra proxy ports to comma-delimited string
	extraPortsStr := ""
	if len(extraProxyPorts) > 0 {
		portStrs := make([]string, len(extraProxyPorts))
		for i, port := range extraProxyPorts {
			portStrs[i] = strconv.Itoa(port)
		}
		extraPortsStr = strings.Join(portStrs, ",")
	}

	exeproxCmd.Env = append(
		exeproxCmd.Environ(),
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
		"GOCOVERDIR="+coverDir,
		"TEST_PROXY_PORTS="+extraPortsStr,
	)

	cmdOut, err := exeproxCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	exeproxCmd.Stderr = exeproxCmd.Stdout

	if err := exeproxCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exeprox: %w", err)
	}

	// Parse output to find ports.
	var teeMu sync.Mutex
	tee := new(strings.Builder)
	type listen struct {
		typ  string
		port int
	}
	listeningC := make(chan listen, 3)
	proxyPortsC := make(chan []int, 1)
	startedC := make(chan bool)
	exeproxSlogErrC := make(chan string, 16)
	exeproxLoggerDone := make(chan bool)

	go func() {
		defer close(exeproxLoggerDone)
		started := false
		seenPanic := false
		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.WriteString("\n")
			teeMu.Unlock()

			if logFile != nil {
				fmt.Fprintf(logFile, "%s\n", line)
			}
			if seenPanic {
				fmt.Printf("%s\n", line)
			}

			// Parse JSON log line.
			if !json.Valid(line) {
				// Invalid JSON could be a stray fmt.Printf...
				// or a panic.
				// If it's a panic, dup all output to stdout.
				if bytes.Contains(line, []byte("panic:")) {
					seenPanic = true
					// Dump what we have so far.
					// From here on out,
					// we'll print as we go.
					teeMu.Lock()
					fmt.Print(tee.String())
					teeMu.Unlock()
				}
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				slog.ErrorContext(ctx, "failed to parse exeprox log file", "error", err, "line", string(line))
				continue
			}
			if level, ok := entry["level"].(string); ok && level == "ERROR" {
				select {
				case exeproxSlogErrC <- string(line):
				default:
				}
			}

			switch entry["msg"] {
			case "listening":
				listeningC <- listen{typ: entry["type"].(string), port: int(entry["port"].(float64))}
				if logPorts {
					slog.InfoContext(ctx, "exed listening", "type", entry["type"], "port", entry["port"])
				}

			case "proxy listeners set up":
				// Parse proxy ports from the "ports" array
				// in the log entry
				if portsVal, ok := entry["ports"].([]any); ok {
					ports := make([]int, len(portsVal))
					for i, p := range portsVal {
						ports[i] = int(p.(float64))
					}
					select {
					case proxyPortsC <- ports:
					default:
					}
					if logPorts {
						slog.InfoContext(ctx, "exed proxy ports", "ports", ports)
					}
				}

			case "server started":
				if !started {
					close(startedC)
					started = true
				}
			}
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.ErrorContext(ctx, "error scanning exed output", "error", err)
		}
	}()

	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var httpPort int
	var proxyPorts []int
	expectedProxyPorts := len(extraProxyPorts)
ProcessLogs:
	for {
		select {
		case ln := <-listeningC:
			switch ln.typ {
			case "http":
				httpPort = ln.port
			}
		case proxyPorts = <-proxyPortsC:
			// Received proxy ports from "proxy listeners set up"
			// log message
		case <-startedC:
			break ProcessLogs
		case <-time.After(timeout):
			teeMu.Lock()
			out := tee.String()
			teeMu.Unlock()
			return nil, fmt.Errorf("timeout waiting for exeprox to start. Output:\n%s", out)
		}
	}
	if httpPort == 0 {
		return nil, fmt.Errorf("failed to start all required ports (http %d)", httpPort)
	}
	if len(proxyPorts) != expectedProxyPorts {
		return nil, fmt.Errorf("got %d proxy ports, expected %d", len(proxyPorts), expectedProxyPorts)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	go func() {
		exeproxCmd.Wait()
		cancel()
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(cmdCtx)
	})

	instance := &ExeproxInstance{
		Exited:            cmdCtx.Done(),
		Cause:             cause,
		Cmd:               exeproxCmd,
		HTTPPort:          httpPort,
		ExtraPorts:        proxyPorts,
		CoverDir:          coverDir,
		Errors:            exeproxSlogErrC,
		exeproxLoggerDone: exeproxLoggerDone,
	}

	AddCanonicalization(instance.HTTPPort, "EXEPROX_HTTP_PORT")

	slog.InfoContext(ctx, "started exeprox", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// Stop stops the exeprox process.
// This does not return an error; errors rae just logged.
func (ei *ExeproxInstance) Stop(ctx context.Context, testRunID string) {
	// Gracefully stop exeprox with SIGTERM so it writes coverage dta.
	if ei.Cmd != nil {
		slog.InfoContext(ctx, "sending SIGTERM to exeprox")
		ei.Cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait briefly for graceful exit so coverage data gets written,
	// then SIGKILL. Deploy does not do graceful shutdown either.
	select {
	case <-ei.Exited:
	case <-time.After(1 * time.Second):
		ei.Cmd.Process.Kill()
		<-ei.Exited
	}

	// Close the Errors channel the caller may be using.
	select {
	case <-ei.exeproxLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)
}
