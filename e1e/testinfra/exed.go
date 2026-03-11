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
	"path/filepath"
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
	dbPath          string          // database location (unexported to prevent e1e tests from accessing it)
	SSHPort         int             // exed ssh local host port
	HTTPPort        int             // exed HTTP local hostport
	PiperPluginPort int             // piper plugin gPRC server port
	ExeproxPort     int             // exeprox gPRC server port
	ExtraPorts      []int           // additional proxy ports
	CoverDir        string          // where coverage data is written
	Errors          chan string     // exed errors are sent on this channel
	GUIDLog         chan string     // exed GUID logs sent on this channel
	LMTPSocketPath  string          // path to LMTP Unix socket

	binPath        string    // exed binary we executed
	logFile        io.Writer // exed log file; may be nil for no logs
	piperPort      int       // port for ssh piper process
	emailServerURL string    // port for fake email server
	whoamiPath     string    // -gh-whoami exed parameter
	exedLoggerDone chan bool // closed when logging goroutine done

	onRestart func(*ExedInstance) // called after a successful restart
}

// guidRegex picks out exed logs that are sent on the
// ExedInstance.GuidLog channel.
var guidRegex = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// exedPorts holds the ports parsed from exed's log output.
type exedPorts struct {
	SSH         int
	HTTP        int
	PiperPlugin int
	Exeprox     int
	Extra       []int
}

func (p *exedPorts) setPort(typ string, port int) {
	switch typ {
	case "ssh":
		p.SSH = port
	case "http":
		p.HTTP = port
	case "plugin":
		p.PiperPlugin = port
	case "exeprox-service":
		p.Exeprox = port
	default:
		slog.Warn("unknown listener type in exed log", "type", typ, "port", port)
	}
}

// exedLogResult holds everything produced by watchExedLogs.
type exedLogResult struct {
	Ports      exedPorts
	Errors     chan string
	GUIDLog    chan string
	LoggerDone chan bool
}

// watchExedLogs launches a goroutine that scans r for exed's JSON log
// output, parses port announcements, and forwards errors/GUIDs to
// channels. It blocks until "server started" is seen or timeout expires.
//
// logFile, if non-nil, receives a copy of every line.
// logPorts controls whether port announcements are logged via slog.
// expectedExtraPorts is the expected len of the "proxy listeners set up" ports array.
func watchExedLogs(ctx context.Context, r io.Reader, logFile io.Writer, logPorts bool, expectedExtraPorts int, timeout time.Duration) (*exedLogResult, error) {
	var teeMu sync.Mutex
	tee := new(strings.Builder)
	type listen struct {
		typ  string
		port int
	}
	listeningC := make(chan listen, 4)
	proxyPortsC := make(chan []int, 1)
	startedC := make(chan bool)
	exedSlogErrC := make(chan string, 16)
	exedGUIDLogC := make(chan string, 128)
	exedLoggerDone := make(chan bool)

	go func() {
		defer close(exedLoggerDone)
		started := false
		seenPanic := false
		scan := bufio.NewScanner(r)
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

	var ports exedPorts
processLogs:
	for {
		select {
		case ln := <-listeningC:
			ports.setPort(ln.typ, ln.port)
		case ports.Extra = <-proxyPortsC:
			// Received proxy ports from "proxy listeners set up"
			// log message
		case <-startedC:
			break processLogs
		case <-time.After(timeout):
			teeMu.Lock()
			out := tee.String()
			teeMu.Unlock()
			return nil, fmt.Errorf("timeout waiting for exed to start. Output:\n%s", out)
		}
	}

	// Drain any listening/proxy events buffered before "server started".
drainListening:
	for {
		select {
		case ln := <-listeningC:
			ports.setPort(ln.typ, ln.port)
		case ports.Extra = <-proxyPortsC:
		default:
			break drainListening
		}
	}

	if ports.SSH == 0 || ports.HTTP == 0 || ports.PiperPlugin == 0 || ports.Exeprox == 0 {
		return nil, fmt.Errorf("failed to get all required ports (ssh %d http %d piper %d exeprox %d)", ports.SSH, ports.HTTP, ports.PiperPlugin, ports.Exeprox)
	}
	if len(ports.Extra) != expectedExtraPorts {
		return nil, fmt.Errorf("got %d proxy ports, expected %d", len(ports.Extra), expectedExtraPorts)
	}

	return &exedLogResult{
		Ports:      ports,
		Errors:     exedSlogErrC,
		GUIDLog:    exedGUIDLogC,
		LoggerDone: exedLoggerDone,
	}, nil
}

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
		rootDir, err := exeRootDir()
		if err != nil {
			return nil, err
		}
		buildCmd := exec.Command("go", "build", "-race", "-cover", "-covermode=atomic", "-coverpkg=exe.dev/...", "-o", binPath, "./cmd/exed")
		buildCmd.Dir = rootDir
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

	// Create a temp directory for the LMTP socket (socket paths have length limits)
	lmtpDir, err := os.MkdirTemp("", "lmtp")
	if err != nil {
		return nil, fmt.Errorf("failed to create LMTP socket dir: %w", err)
	}
	lmtpSocketPath := filepath.Join(lmtpDir, "lmtp.sock")
	AddCleanup(func() { os.RemoveAll(lmtpDir) })

	emailServerURL := fmt.Sprintf("http://localhost:%d", emailServerPort)

	whoamiPath := "../ghuser/whoami.sqlite3"
	if os.Getenv("CI") != "" {
		whoamiPath = "/root/whoami.sqlite3"
	}

	exedCmd := exec.Command(binPath,
		"-db="+dbPath.Name(),
		"-stage=test",
		"-http=:0",
		"-ssh=localhost:0",
		"-piper-plugin=localhost:0",
		"-piperd-port="+strconv.Itoa(piperPort),
		"-exeprox-service-port=0",
		"-fake-email-server="+emailServerURL,
		"-gh-whoami="+whoamiPath,
		"-exelet-addresses="+strings.Join(exeletAddrs, ","),
		"-lmtp-socket="+lmtpSocketPath,
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

	exedCmd.Env = addExedEnvKeys(exedCmd.Env)

	cmdOut, err := exedCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	exedCmd.Stderr = exedCmd.Stdout

	if err := exedCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exed: %w", err)
	}

	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	result, err := watchExedLogs(ctx, cmdOut, logFile, logPorts, len(extraProxyPorts), timeout)
	if err != nil {
		return nil, err
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
		dbPath:          dbPath.Name(),
		SSHPort:         result.Ports.SSH,
		HTTPPort:        result.Ports.HTTP,
		PiperPluginPort: result.Ports.PiperPlugin,
		ExeproxPort:     result.Ports.Exeprox,
		ExtraPorts:      result.Ports.Extra,
		CoverDir:        coverDir,
		Errors:          result.Errors,
		GUIDLog:         result.GUIDLog,
		LMTPSocketPath:  lmtpSocketPath,
		binPath:         binPath,
		logFile:         logFile,
		piperPort:       piperPort,
		emailServerURL:  emailServerURL,
		whoamiPath:      whoamiPath,
		exedLoggerDone:  result.LoggerDone,
	}

	AddCanonicalization(instance.SSHPort, "EXED_SSH_PORT")
	AddCanonicalization(instance.HTTPPort, "EXED_HTTP_PORT")
	AddCanonicalization(instance.PiperPluginPort, "EXED_PIPER_PLUGIN_PORT")
	AddCanonicalization(instance.ExeproxPort, "EXED_EXEPROX_SERVICE_PORT")

	slog.InfoContext(ctx, "started exed", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// addExedEnvKeys adds some keys to the exed environment.
func addExedEnvKeys(env []string) []string {
	if os.Getenv("CI") != "" {
		env = append(env, "GITHUB_TOKEN=fake-but-not-empty")
	}

	// Disable IPQS email quality checks in tests.
	// Test emails like "TestFoo@example.com" get flagged as disposable,
	// which disables VM creation for the test user.
	env = append(env, "IPQS_API_KEY=")

	// Ensure LLM gateway API keys are set for e1e tests.
	// If real keys aren't provided,
	// use fake keys so requests can reach the external APIs
	// (which will reject them with auth errors,
	// but that still proves the gateway path works end-to-end).
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		env = append(env, "ANTHROPIC_API_KEY=fake-key-for-e1e-test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		env = append(env, "OPENAI_API_KEY=fake-key-for-e1e-test")
	}
	if os.Getenv("FIREWORKS_API_KEY") == "" {
		env = append(env, "FIREWORKS_API_KEY=fake-key-for-e1e-test")
	}

	// Pass through GitHub mock server URLs for GitHub App installation tests.
	for _, key := range []string{
		"EXE_GITHUB_APP_CLIENT_ID",
		"EXE_GITHUB_APP_CLIENT_SECRET",
		"EXE_GITHUB_APP_SLUG",
		"TEST_GITHUB_TOKEN_URL",
		"TEST_GITHUB_API_URL",
		"EXE_GITHUB_APP_ID",
		"EXE_GITHUB_APP_PRIVATE_KEY",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}

	return env
}

// Stop stops the exed process.
// This does not return an error; errors are just logged.
// If midTest is true we are in the middle of a test
// and should not remove the database.
func (ei *ExedInstance) Stop(ctx context.Context, testRunID string, midTest bool) {
	if !midTest {
		if err := ei.checkBoxesCleanedUp(ctx, testRunID); err != nil {
			slog.ErrorContext(ctx, "boxes not cleaned up", "error", err)
			// Send to Errors channel so the test fails.
			// The channel is buffered and drained by a cleanup in infra_test.go.
			select {
			case ei.Errors <- fmt.Sprintf("boxes not cleaned up: %v", err):
			default:
			}
		}

		os.Remove(ei.dbPath)
	}

	// Gracefully stop exed with SIGTERM so it writes coverage data.
	slog.InfoContext(ctx, "sending SIGTERM to exed")
	ei.Cmd.Process.Signal(syscall.SIGTERM)
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
	case <-ei.exedLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)
	close(ei.GUIDLog)
}

// checkBoxesCleanedUp makes sure all the boxes that exed
// started are shut down.
func (ei *ExedInstance) checkBoxesCleanedUp(ctx context.Context, testRunID string) error {
	url := fmt.Sprintf("http://localhost:%d/debug/vms?format=json", ei.HTTPPort)
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

// Restart restarts the exed process with a possibly different
// set of exelets. If midTest is true, we are in the middle
// of a test and should not remove the database.
func (ei *ExedInstance) Restart(ctx context.Context, exeletAddrs []string, testRunID string, midTest bool) error {
	// We don't want canceling the context passed here to stop exed.
	ctx = context.WithoutCancel(ctx)

	start := time.Now()
	slog.InfoContext(ctx, "restarting exed", "exelets", exeletAddrs)
	var logger *slog.Logger
	if ei.logFile != nil {
		handler := slog.NewJSONHandler(ei.logFile, nil)
		logger = slog.New(handler)
		logger.InfoContext(ctx, "restarting exed", "exelets", exeletAddrs)
	}

	ei.Stop(ctx, testRunID, midTest)

	exedCmd := exec.Command(ei.binPath,
		"-db="+ei.dbPath,
		"-stage=test",
		"-http=:0",
		"-ssh=localhost:0",
		"-piper-plugin=localhost:0",
		"-piperd-port="+strconv.Itoa(ei.piperPort),
		"-exeprox-service-port=0",
		"-fake-email-server="+ei.emailServerURL,
		"-gh-whoami="+ei.whoamiPath,
		"-exelet-addresses="+strings.Join(exeletAddrs, ","),
		"-lmtp-socket="+ei.LMTPSocketPath,
	)

	exedCmd.Env = append(
		exedCmd.Environ(),
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
		"GOCOVERDIR="+ei.CoverDir,
	)

	// Pass 0s to let the OS assign fresh ports, avoiding port-reuse races.
	if n := len(ei.ExtraPorts); n > 0 {
		portStrs := make([]string, n)
		for i := range portStrs {
			portStrs[i] = "0"
		}
		exedCmd.Env = append(exedCmd.Env, "TEST_PROXY_PORTS="+strings.Join(portStrs, ","))
	}

	exedCmd.Env = addExedEnvKeys(exedCmd.Env)

	cmdOut, err := exedCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	exedCmd.Stderr = exedCmd.Stdout

	if err := exedCmd.Start(); err != nil {
		return fmt.Errorf("failed to restart exed: %v", err)
	}

	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	result, err := watchExedLogs(ctx, cmdOut, ei.logFile, false, len(ei.ExtraPorts), timeout)
	if err != nil {
		return err
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	go func() {
		exedCmd.Wait()
		cancel()
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(cmdCtx)
	})

	ei.Exited = cmdCtx.Done()
	ei.Cause = cause
	ei.Cmd = exedCmd
	ei.SSHPort = result.Ports.SSH
	ei.HTTPPort = result.Ports.HTTP
	ei.PiperPluginPort = result.Ports.PiperPlugin
	ei.ExeproxPort = result.Ports.Exeprox
	ei.ExtraPorts = result.Ports.Extra
	ei.Errors = result.Errors
	ei.GUIDLog = result.GUIDLog
	ei.exedLoggerDone = result.LoggerDone

	if ei.onRestart != nil {
		ei.onRestart(ei)
	}

	elapsed := time.Since(start.Truncate(100 * time.Millisecond))
	slog.InfoContext(ctx, "restarted exed", "elapsed", elapsed)
	if logger != nil {
		logger.InfoContext(ctx, "restarted exed", "elapsed", elapsed)
	}

	return nil
}
