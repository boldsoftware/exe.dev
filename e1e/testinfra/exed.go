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
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ExedInstance describes the running exed process.
type ExedInstance struct {
	Exited          <-chan struct{} // closed when exed exits.
	Cause           func() error    // why context was canceled
	Cmd             *exec.Cmd       // exed command
	DBPath          string          // database location
	SSHPort         int             // exed ssh local host port
	HTTPPort        int             // exed HTTP local hostport
	PiperPluginPort int             // piper plugin gPRC server port
	ExtraPorts      []int           // additional proxy ports
	CoverDir        string          // where coverage data is written
	Errors          chan string     // exed errors are sent on this channel
	GUIDLog         chan string     // exed GUID logs sent on this channel

	exedLoggerDone chan bool // closed when logging goroutine done
}

// guidRegex picks out exed logs that are sent on the
// ExedInstance.GuidLog channel.
var guidRegex = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// StartExed starts the exed process.
//
// emailServerPort is a port on the local host,
// passed as the -fake-email-server exed option.
//
// piperPort is a port on the local host,
// passed as the -piperd-port exed option.
//
// extraProxyPorts is a list of ports passed to exed
// via the TEST_PROXY_PORTS environment variable.
// The ports can be zero, in which case exed will pick them.
// They are returned in the EXTRA_PORTS field of the
// returned ExedInstance.
//
// exeletAddrs is the addresses of the exelet(s),
// typically in the form tcp://HOST:PORT.
//
// logFile, if not nil, is a file to write logs to.
//
// logPorts is whether to log port numbers using slog.InfoContext.
func StartExed(ctx context.Context, emailServerPort, piperPort int, extraProxyPorts []int, exeletAddrs []string, logFile io.Writer, logPorts bool) (*ExedInstance, error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting exed")

	// If PREBUILT_EXED is set, use it.
	// Otherwise build a new binary.
	var binPath string
	if prebuilt := os.Getenv("PREBUILT_EXED"); prebuilt != "" {
		st, err := os.Stat(prebuilt)
		if err != nil {
			return nil, fmt.Errorf("PREBUILT_EXED not usable: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("PREBUILT_EXED points to a directory, need a file: %s", prebuilt)
		}
		binPath = prebuilt
	} else {
		bin, err := os.CreateTemp("", "exed-test")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		bin.Close()
		binPath = bin.Name()
		buildCmd := exec.Command("go", "build", "-race", "-cover", "-covermode=atomic", "-coverpkg=exe.dev/...", "-o", binPath, "../cmd/exed")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to build exed: %w\n%s", err, out)
		}
		AddCleanup(func() { os.Remove(binPath) })
	}

	shm := "/dev/shm"
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		shm = ""
	}
	dbPath, err := os.CreateTemp(shm, "exed-test-*.db")
	if err != nil {
		return nil, err
	}
	dbPath.Close()
	AddCleanup(func() { os.Remove(dbPath.Name()) })

	coverDir, err := os.MkdirTemp("", "exed-test_cov")
	if err != nil {
		return nil, fmt.Errorf("failed to create coverage dir: %w", err)
	}

	emailServerURL := fmt.Sprintf("http://localhost:%d", emailServerPort)

	whoamiPath := "../ghuser/whoami.sqlite3"
	if os.Getenv("CI") != "" {
		whoamiPath = "/root/whoami.sqlite3"
	}

	exedCmd := exec.Command(binPath,
		"-db="+dbPath.Name(),
		"-dev=test",
		"-http=:0",
		"-ssh=:0",
		"-piper-plugin=:0",
		"-piperd-port="+strconv.Itoa(piperPort),
		"-fake-email-server="+emailServerURL,
		"-gh-whoami="+whoamiPath,
		"-exelet-addresses="+strings.Join(exeletAddrs, ","),
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

	exedCmd.Env = append(
		exedCmd.Environ(),
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
		"GOCOVERDIR="+coverDir,
		"TEST_PROXY_PORTS="+extraPortsStr,
	)

	if os.Getenv("CI") != "" {
		exedCmd.Env = append(exedCmd.Env, "GITHUB_TOKEN=fake-but-not-empty")
	}

	// Ensure LLM gateway API keys are set for e1e tests.
	// If real keys aren't provided,
	// use fake keys so requests can reach the external APIs
	// (which will reject them with auth errors,
	// but that still proves the gateway path works end-to-end).
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		exedCmd.Env = append(exedCmd.Env, "ANTHROPIC_API_KEY=fake-key-for-e1e-test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		exedCmd.Env = append(exedCmd.Env, "OPENAI_API_KEY=fake-key-for-e1e-test")
	}
	if os.Getenv("FIREWORKS_API_KEY") == "" {
		exedCmd.Env = append(exedCmd.Env, "FIREWORKS_API_KEY=fake-key-for-e1e-test")
	}

	cmdOut, err := exedCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	exedCmd.Stderr = exedCmd.Stdout

	if err := exedCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exed: %w", err)
	}

	// Parse output to find ports
	var teeMu sync.Mutex
	tee := new(strings.Builder)
	type listen struct {
		typ  string
		port int
	}
	listeningC := make(chan listen, 3)
	proxyPortsC := make(chan []int, 1)
	startedC := make(chan bool)
	exedSlogErrC := make(chan string, 16)
	exedGUIDLogC := make(chan string, 128)
	exedLoggerDone := make(chan bool)

	go func() {
		defer close(exedLoggerDone)
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
				if guidRegex.Match(line) {
					select {
					case exedGUIDLogC <- string(line):
					default:
					}
				}
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				slog.ErrorContext(ctx, "failed to parse exed log file", "error", err, "line", string(line))
				continue
			}
			if level, ok := entry["level"].(string); ok && level == "ERROR" {
				select {
				case exedSlogErrC <- string(line):
				default:
				}
			}
			if guid, ok := entry["guid"].(string); ok && guid != "" {
				select {
				case exedGUIDLogC <- string(line):
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

	var sshPort, httpPort, piperPluginPort int
	var proxyPorts []int
	expectedProxyPorts := len(extraProxyPorts)
ProcessLogs:
	for {
		select {
		case ln := <-listeningC:
			switch ln.typ {
			case "ssh":
				sshPort = ln.port
			case "http":
				httpPort = ln.port
			case "plugin":
				piperPluginPort = ln.port
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
			return nil, fmt.Errorf("timeout waiting for exed to start. Output:\n%s", out)
		}
	}
	if sshPort == 0 || httpPort == 0 || piperPluginPort == 0 {
		return nil, fmt.Errorf("failed to start all required ports (ssh %d http %d piper %d)", sshPort, httpPort, piperPluginPort)
	}
	if len(proxyPorts) != expectedProxyPorts {
		return nil, fmt.Errorf("got %d proxy ports, expected %d", len(proxyPorts), expectedProxyPorts)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	go func() {
		exedCmd.Wait()
		cancel()
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(cmdCtx)
	})

	instance := &ExedInstance{
		Exited:          cmdCtx.Done(),
		Cause:           cause,
		Cmd:             exedCmd,
		DBPath:          dbPath.Name(),
		SSHPort:         sshPort,
		HTTPPort:        httpPort,
		PiperPluginPort: piperPluginPort,
		ExtraPorts:      proxyPorts,
		CoverDir:        coverDir,
		Errors:          exedSlogErrC,
		GUIDLog:         exedGUIDLogC,
		exedLoggerDone:  exedLoggerDone,
	}

	slog.InfoContext(ctx, "started exed", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// Stop stops the exed process.
// This does not return an error; errors are just logged.
func (ei *ExedInstance) Stop(ctx context.Context, testRunID string) {
	if err := ei.checkBoxesCleanedUp(ctx, testRunID); err != nil {
		slog.ErrorContext(ctx, "boxes not cleaned up", "error", err)
	}

	os.Remove(ei.DBPath)

	// Gracefully stop exed with SIGTERM so it writes coverage data.
	slog.InfoContext(ctx, "sending SIGTERM to exed")
	ei.Cmd.Process.Signal(syscall.SIGTERM)
	// Wait for graceful exit (up to 5 seconds).
	select {
	case <-ei.Exited:
		// Graceful exit.
	case <-time.After(5 * time.Second):
		// Forcefully kill if still running.
		slog.WarnContext(ctx, "exed did not exit gracefully, killing")
		ei.Cmd.Process.Kill()
		<-ei.Exited
	}

	// Close the Errors channel the caller may be using.
	select {
	case <-ei.exedLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)
	close(ei.GUIDLog)
}

// checkBoxesCleanedUp makes sure all the boxes that exed
// started are shut down.
func (ei *ExedInstance) checkBoxesCleanedUp(ctx context.Context, testRunID string) error {
	url := fmt.Sprintf("http://localhost:%d/debug/boxes?format=json", ei.HTTPPort)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to check boxes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code checking boxes: %d", resp.StatusCode)
	}

	var boxes []struct {
		Host   string `json:"host"`
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&boxes); err != nil {
		return fmt.Errorf("failed to decode boxes JSON: %w", err)
	}

	// Log all boxes for debugging.
	if len(boxes) > 0 {
		allBoxNames := make([]string, 0, len(boxes))
		for _, box := range boxes {
			allBoxNames = append(allBoxNames, box.Name)
		}
		slog.InfoContext(ctx, "boxes at cleanup", "boxes", allBoxNames)
	}

	// Only check boxes from this specific test run.
	boxPrefix := fmt.Sprintf("e1e-%s-", testRunID)
	var e1eBoxes []string
	for _, box := range boxes {
		if strings.HasPrefix(box.Name, boxPrefix) {
			e1eBoxes = append(e1eBoxes, box.Name)
		}
	}

	if len(e1eBoxes) > 0 {
		return fmt.Errorf("e1e boxes not cleaned up: %v", e1eBoxes)
	}

	return nil
}
