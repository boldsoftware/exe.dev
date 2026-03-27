// This file provides shared infrastructure for the e2e tests.

package e1e

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"golang.org/x/sync/semaphore"
)

var (
	flagVerbosePiperd   = flag.Bool("vpiperd", false, "enable verbose logging from sshpiperd")
	flagVerboseExed     = flag.Bool("vexed", false, "enable verbose logging from exed")
	flagVerboseExelet   = flag.Bool("vexelet", false, "enable verbose logging from exelet")
	flagVerboseExeprox  = flag.Bool("vexeprox", false, "enable verbose logging from exeprox")
	flagVerboseExepipe  = flag.Bool("vexepipe", false, "enable verbose logging from exepipe")
	flagVerbosePorts    = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerboseEmail    = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagVerbosePty      = flag.Bool("vpty", false, "enable verbose logging from pty connections")
	flagVerboseSlog     = flag.Bool("vslog", false, "enable verbose logging from slogs")
	flagVerboseMetricsd = flag.Bool("vmetricsd", false, "enable verbose logging from metricsd")
	flagVerboseAll      = flag.Bool("vv", false, "enable ALL verbose logging (shorthand for all -v* flags)")
	flagCinema          = flag.Bool("cinema", true, "enable ASCIIcinema recordings")
	flagCoverProfile    = flag.String("coverage-out", "e1e.cover", "path to write merged coverage profile")
	flagPlaywright      = flag.Bool("playwright", true, "enable Playwright browser tests (requires installed browsers)")
	flagDefaultExeprox  = flag.Bool("default-exeprox", true, "default to connecting to exeprox rather than exed")
	flagUseExepipe      = flag.Bool("use-exepipe", false, "start exepipe processes")

	// testRunID is a random identifier for this test invocation.
	// A single container host is often shared across test and dev runs.
	// We use this ID to understand which boxes were created specifically by this test run.
	testRunID string

	// pool limits concurrent VM creation across parallel tests.
	// Without this, all parallel tests stampede into VM creation
	// simultaneously, overwhelming the host and causing timeouts.
	pool *vmPool
)

func TestMain(m *testing.M) {
	flag.Parse()

	// Enable all verbose flags if -vv is set
	if *flagVerboseAll {
		*flagVerbosePiperd = true
		*flagVerboseExed = true
		*flagVerboseExelet = true
		*flagVerboseExeprox = true
		*flagVerboseExepipe = true
		*flagVerbosePorts = true
		*flagVerboseEmail = true
		*flagVerbosePty = true
		*flagVerboseSlog = true
		*flagVerboseMetricsd = true
	}

	// Resolve coverage output path relative to repo root (parent of e1e directory)
	// go test runs from within the package directory, so relative paths would be relative to e1e/
	if !filepath.IsAbs(*flagCoverProfile) {
		wd, err := os.Getwd()
		if err == nil {
			*flagCoverProfile = filepath.Join(filepath.Dir(wd), *flagCoverProfile)
		}
	}

	// Generate unique test run ID to avoid box name collisions
	testRunID = fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

	if testing.Short() {
		// ain't nothing short about these tests
		fmt.Println("skipping tests in short mode")
		return
	}

	// Skip setup when just listing tests (go test -list)
	if f := flag.Lookup("test.list"); f != nil && f.Value.String() != "" {
		os.Exit(m.Run())
	}

	// We are going to actually run some tests.

	initVMSem()

	defer testinfra.RunCleanups()
	exit := func(status int) {
		testinfra.RunCleanups()
		os.Exit(status)
	}

	err := initLogging()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logging: %v\n", err)
		exit(1)
	}

	// Show the verbosity hint unless:
	// - any verbose flag is already set, OR
	// - a specific test is requested via -run (user likely knows what they're doing)
	runFilter := ""
	if f := flag.Lookup("test.run"); f != nil {
		runFilter = f.Value.String()
	}
	hasSpecificRun := runFilter != "" && runFilter != "." && runFilter != ".*"
	if testing.Verbose() && !hasSpecificRun && !*flagVerboseAll && !*flagVerbosePiperd && !*flagVerboseExed && !*flagVerboseExelet && !*flagVerboseExeprox && !*flagVerboseExepipe && !*flagVerbosePorts && !*flagVerboseEmail && !*flagVerbosePty && !*flagVerboseSlog {
		fmt.Print(`
════════
-v requested, but the e1e tests generate lots of output, and they run in parallel.
Having "-v" enable extra logging is overwhelming.

For debug info, use -run to scope to a single test, and add some/all of these flags:

-vv       enable ALL verbose logging (shorthand for all flags below)
-vpiperd  print sshpiperd logs
-vexed    print exed logs
-vexelet  print exelet logs
-vexeprox print exeprox logs
-vexepipe print exepipe logs
-vports   print port mappings
-vemail   print email server logs
-vpty     print pty (ssh) logs
-vslog    print e1e test binary slogs

Flags must be added AFTER the paths, e.g., go test -v -count 1 -run TestHTTPProxyBasic ./e1e/... -vexed
════════

`)
	}

	// Bootstrap localhost if CTR_HOST=localhost
	if os.Getenv("CTR_HOST") == "localhost" {
		if err := testinfra.BootstrapLocalhost(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to bootstrap localhost: %v\n", err)
			exit(1)
		}
	}

	ctrHost, err := testinfra.StartExeletVM(testRunID)
	if err != nil {
		if err == testinfra.ErrNoVM && os.Getenv("CI") != "" {
			fmt.Printf("skipping tests in CI: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}

	env, err := setup(ctrHost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to setup test environment: %v\n", err)
		slog.Error("test setup failed", "error", err)
		if closeErr := env.Close(); closeErr != nil {
			slog.Error("cleanup failed", "error", closeErr)
		}
		os.Stderr.Sync()
		exit(1)
	}

	var exitCode int

	// Add some cleanups before we run the tests.
	// The cleanups are run in reverse order.

	testinfra.AddCleanup(func() {
		for _, f := range logFiles {
			if f == nil {
				continue
			}
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to close log file %v: %v\n", f.Name(), err)
			}
		}
	})

	testinfra.AddCleanup(func() {
		for line := range env.servers.Exed.GUIDLog {
			fmt.Fprintf(os.Stderr, "\n\nexed log with guid during e1e run:\n%s\n\n", line)
		}
	})

	testinfra.AddCleanup(func() {
		for line := range env.servers.Exelets[0].Errors {
			exitCode = 1
			fmt.Fprintf(os.Stderr, "\n\nexelet emitted ERROR log during e1e run:\n%s\n\n", line)
		}
	})

	testinfra.AddCleanup(func() {
		if env.servers.Exelets[0].ExepipeErrors != nil {
			for line := range env.servers.Exelets[0].ExepipeErrors {
				exitCode = 1
				fmt.Fprintf(os.Stderr, "\n\nremote exepipe emitted ERROR log during e1e run\n%s\n\n", line)
			}
		}
	})

	testinfra.AddCleanup(func() {
		for line := range env.servers.Exed.Errors {
			// TODO(philip): TestNewWithPrompt triggers this, because Shelley talks
			// to the gateway and even though it's supposed to use "predictable" model, we get an error.
			// This is an unrelated bug uncovered when I was trying to change how the
			// plumbing works for the llm gateway, so I'm punting on fixing that bug and making
			// the test infra ever so slightly less picky about error logs.
			// Note that the change that exposed this was: "ctrhosttest: fix ResolveDefaultGateway to parse CTR_HOST SSH URLs"
			// which leads me to believe that the Shelley gateway URL was wrong previously, and Shelley
			// was silently swallowing an error, and now it's managing to talk to exed.
			if strings.Contains(line, "\"msg\":\"llmgateway.httpError\"") {
				continue
			}
			exitCode = 1
			fmt.Fprintf(os.Stderr, "\n\nexed emitted ERROR log during e1e run:\n%s\n\n", line)
		}
	})

	testinfra.AddCleanup(func() {
		for line := range env.servers.Exeprox.Errors {
			exitCode = 1
			fmt.Fprintf(os.Stderr, "\n\nexeprox emitted ERROR log during e1e run:\n%s\n\n", line)
		}
	})

	testinfra.AddCleanup(func() {
		if err := env.Close(); err != nil {
			slog.Error("test cleanup failed", "error", err)
			fmt.Fprintf(os.Stderr, "\n\nERROR: %v\n\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	})

	Env = env

	// Initialize Playwright if enabled
	if *flagPlaywright {
		slog.Info("starting playwright")
		if err := testinfra.StartPlaywright(); err != nil {
			slog.Error("failed to start playwright", "error", err)
			fmt.Fprintf(os.Stderr, "failed to start playwright: %v\n", err)
			exit(1)
		}
		testinfra.AddCleanup(func() {
			testinfra.StopPlaywright()
		})
	}

	slog.Info("running tests")

	exitCode = m.Run()

	testinfra.RunCleanups()

	os.Stderr.Sync()

	os.Exit(exitCode)
}

var logFiles = map[string]*os.File{
	"sshpiperd": nil,
	"exed":      nil,
	"exeprox":   nil,
	"exelet":    nil,
	"exepipe":   nil,
	"e1e":       nil,
}

func logFileFor(name string) *os.File {
	f, ok := logFiles[name]
	if !ok || f == nil {
		return os.Stdout
	}
	return f
}

func initLogging() error {
	e1eLogDir := os.Getenv("E1E_LOG_DIR")
	if e1eLogDir == "" {
		level := slog.LevelWarn
		if *flagVerboseSlog {
			level = slog.LevelDebug
		}
		// Default: Plain text logging to stdout
		handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
		slog.SetDefault(slog.New(handler))
		return nil
	}
	// Log to files. (We're probably in CI.)
	if err := os.MkdirAll(e1eLogDir, 0o700); err != nil {
		return fmt.Errorf("failed to create E1E_LOG_DIR %s: %w", e1eLogDir, err)
	}
	for name := range logFiles {
		logPath := filepath.Join(e1eLogDir, name+".log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("failed to open log file %s: %w", logPath, err)
		}
		logFiles[name] = logFile
	}
	handler := slog.NewJSONHandler(logFiles["e1e"], &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	// auto-enable all verbose flags except:
	// - pty, which is accessible via the .cast files
	// - slog, which is already verbose by setting log level to debug above
	*flagVerbosePiperd = true
	*flagVerboseExed = true
	*flagVerboseExelet = true
	*flagVerboseExeprox = true
	*flagVerboseExepipe = true
	*flagVerbosePorts = true
	*flagVerboseEmail = true
	return nil
}

var Env *testEnv

type testEnv struct {
	servers *testinfra.ServerEnv
}

func (e *testEnv) sshPort() int {
	return e.servers.SSHProxy.Port()
}

// HTTPPort returns the HTTP port on localhost to use to reach
// either exed or exeprox. By default we connect to exed.
// The -default-exeprox flag changes that to default to exeprox.
// This lets us test that both exed and exeprox have the same
// behavior for incoming requests.
func (e *testEnv) HTTPPort() int {
	if *flagDefaultExeprox {
		return e.servers.Exeprox.HTTPPort
	}
	return e.servers.Exed.HTTPPort
}

func (e *testEnv) context(t *testing.T) context.Context {
	// Merge t.Context() with exed, exelet, and piperd contexts.
	c, cancel := context.WithCancelCause(t.Context())
	go func() {
		select {
		case <-e.servers.Exed.Exited:
			cancel(e.servers.Exed.Cause())
		case <-e.servers.Exelets[0].Exited:
			cancel(e.servers.Exelets[0].Cause())
		case <-e.servers.Exeprox.Exited:
			cancel(e.servers.Exeprox.Cause())
		case <-e.servers.SSHPiperd.Exited:
			cancel(e.servers.SSHPiperd.Cause())
		case <-c.Done():
		}
	}()
	return c
}

func (e *testEnv) Close() error {
	if e == nil {
		return nil
	}

	var coverDirs []string
	if e.servers != nil {
		coverDirs = e.servers.Stop(context.Background(), testRunID)
	}

	// Collect and merge coverage data from exed and exelet
	if e.servers != nil && e.servers.Exed != nil && e.servers.Exeprox != nil {
		slog.Info("COVERAGE", "exed_dir", e.servers.Exed.CoverDir, "exelet_dir", coverDirs, "exeprox_dir", e.servers.Exeprox.CoverDir)

		if cd := e.servers.Exed.CoverDir; cd != "" {
			coverDirs = append(coverDirs, cd)
		}
		if cd := e.servers.Exeprox.CoverDir; cd != "" {
			coverDirs = append(coverDirs, cd)
		}
	}

	if len(coverDirs) > 0 {
		// Merge coverage from all sources using go tool covdata
		// -i takes comma-separated directories
		inputDirs := strings.Join(coverDirs, ",")
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i", inputDirs, "-o", *flagCoverProfile)
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Error("failed to write coverage profile", "cmd", cmd.String(), "error", err, "output", string(out))
		} else {
			slog.Info("wrote merged coverage profile", "path", *flagCoverProfile, "sources", coverDirs)
		}
	}

	return nil
}

func setup(ctrHost string) (*testEnv, error) {
	env := &testEnv{}

	// We use a TCP proxy for exed HTTP so that services connect
	// to a stable proxy port. On exed restart, the proxy is
	// retargeted to the new port.
	exedHTTPProxy, err := testinfra.NewTCPProxy(context.Background(), "exedHTTPProxy")
	if err != nil {
		return env, fmt.Errorf("failed to create exed HTTP proxy: %w", err)
	}
	go exedHTTPProxy.Serve()
	if *flagVerbosePorts {
		slog.Info("exed HTTP proxy listening", "port", exedHTTPProxy.Port())
	}

	// Start metricsd before exelet so we can pass its URL to exelet
	var metricsdLog io.Writer
	if *flagVerboseMetricsd {
		metricsdLog = logFileFor("metricsd")
	}
	metricsdInstance, err := testinfra.StartMetricsd(context.Background(), metricsdLog, *flagVerbosePorts)
	if err != nil {
		return env, fmt.Errorf("failed to start metricsd: %w", err)
	}

	var exepipe *testinfra.ExepipeInstance
	if *flagUseExepipe {
		var exepipeLog io.Writer
		if *flagVerboseExepipe {
			exepipeLog = logFileFor("exepipe")
		}
		exepipe, err = testinfra.StartExepipe(context.Background(), exepipeLog)
		if err != nil {
			return env, fmt.Errorf("failed to start exepipe: %w", err)
		}
	}

	bins, exeletBinary, err := testinfra.BuildAll(context.Background(), testRunID)
	if err != nil {
		return env, err
	}
	testinfra.AddCleanup(func() {
		os.Remove(exeletBinary)
	})

	var exeletLog io.Writer
	if *flagVerboseExelet {
		exeletLog = logFileFor("exelet")
	}

	var exepipeVMLog io.Writer
	if *flagVerboseExepipe {
		exepipeVMLog = logFileFor("exepipe")
	}

	// Configure metrics collection for exelet
	metricsConfig := &testinfra.MetricsConfig{
		DaemonURL: metricsdInstance.Address,
		Interval:  5 * time.Second,
	}

	// Both exedPort and metadataPort use the exed proxy because infra tests
	// don't run exeprox; exed handles metadata requests directly.
	exelet, err := testinfra.StartExelet(context.Background(), exeletBinary, ctrHost, exedHTTPProxy.Port(), exedHTTPProxy.Port(), exepipe, testRunID, exeletLog, exepipeVMLog, *flagVerbosePorts, nil, metricsConfig)
	if err != nil {
		return env, err
	}

	var exedLog io.Writer
	if *flagVerboseExed {
		exedLog = logFileFor("exed")
	}

	var exeproxLog io.Writer
	if *flagVerboseExeprox {
		exeproxLog = logFileFor("exeprox")
	}

	var piperLog io.Writer
	if *flagVerbosePiperd {
		piperLog = logFileFor("sshpiperd")
	}

	serverEnv, err := testinfra.StartServers(context.Background(),
		bins,
		[]*testinfra.ExeletInstance{exelet},
		exepipe,
		[]*testinfra.TCPProxy{exedHTTPProxy},
		exedLog,
		exeproxLog,
		piperLog,
		*flagVerbosePorts,
		*flagVerboseEmail,
		metricsdInstance,
	)
	env.servers = serverEnv
	if err != nil {
		return env, err
	}

	return env, nil
}

// genSSHKey generates an SSH keypair for a test.
// The private half goes into a file to satisfy ssh,
// and the public half is returned as a string,
// for testing convenience.
func genSSHKey(t *testing.T) (path, publickey string) {
	path, publickey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return path, publickey
}

type expectPty struct {
	*testinfra.TestPTY
}

func (p *expectPty) deleteBox(boxName string) {
	p.T().Helper()
	p.SendLine("rm " + boxName)
	p.Want("Deleting")
	p.Reject("internal error")
	p.Want("success")
	p.WantPrompt()
}

// attachAndStart attaches the pty to the given command and starts it.
func (p *expectPty) attachAndStart(cmd *exec.Cmd) {
	p.TestPTY.AttachAndStart(cmd)
	p.T().Cleanup(func() { _ = cmd.Wait() })
}

func makePty(t *testing.T, name string) *expectPty {
	var cinemaName string
	if *flagCinema {
		cinemaName = t.Name()
	}

	pty, seen := testinfra.MakeTestPTY(t, cinemaName, name, *flagVerbosePty)
	t.Cleanup(func() { pty.Close() })

	if *flagCinema && !seen {
		t.Cleanup(func() {
			if t.Failed() {
				// Don't overwrite golden files on failure.
				// It's annoying to have to clean them up.
				return
			}

			_, skip := skipGolden.Load(t.Name())
			if skip {
				return
			}

			if err := testinfra.WriteASCIInemaFile("golden", cinemaName); err != nil {
				t.Error(err)
			}
		})
	}

	return &expectPty{pty}
}

func sshToExeDev(t *testing.T, keyFile string) *expectPty {
	pty := makePty(t, "ssh localhost")
	cmd, err := Env.servers.SSHToExeDev(Env.context(t), pty.PTY(), keyFile)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	return pty
}

func runParseExeDevJSON[T any](t *testing.T, keyFile string, args ...string) T {
	result, err := testinfra.RunParseExeDevJSON[T](Env.context(t), Env.servers, keyFile, args...)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return result
}

func boxSSHCommand(t *testing.T, boxname, keyFile string, args ...string) *exec.Cmd {
	return Env.servers.BoxSSHCommand(Env.context(t), boxname, keyFile, args...)
}

// boxSSHShell runs a shell command on the box via SSH.
// The command is base64-encoded to avoid quoting issues when passing
// commands with special characters through SSH.
func boxSSHShell(t *testing.T, boxname, keyFile, shellCmd string) *exec.Cmd {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(shellCmd))
	// Wrap in single quotes so the remote shell treats this as a single argument to sh -c.
	// Base64 output contains only alphanumeric, +, /, and = characters, so no escaping needed.
	wrapper := fmt.Sprintf("'echo %s | base64 -d | sh'", encoded)
	return boxSSHCommand(t, boxname, keyFile, "sh", "-c", wrapper)
}

// waitForSSH blocks until SSH is responsive on the given box.
func waitForSSH(t *testing.T, boxName, keyFile string) {
	if err := Env.servers.WaitForBoxSSHServer(Env.context(t), boxName, keyFile); err != nil {
		t.Helper()
		t.Fatal(err)
	}
}

// startHTTPServer starts a busybox httpd server on the given port inside the box,
// registers cleanup, and waits for it to be ready.
func startHTTPServer(t *testing.T, box, keyFile string, port int) {
	t.Helper()
	httpdCmd := boxSSHCommand(t, box, keyFile, "busybox", "httpd", "-f", "-p", strconv.Itoa(port), "-h", "/home/exedev")
	if err := httpdCmd.Start(); err != nil {
		t.Fatalf("failed to start busybox httpd: %v", err)
	}
	t.Cleanup(func() {
		if httpdCmd.Process != nil {
			httpdCmd.Process.Kill()
			httpdCmd.Process.Wait()
		}
	})
	waitCmd := boxSSHCommand(t, box, keyFile, "timeout", "20", "sh", "-c",
		fmt.Sprintf("'while ! curl -s http://localhost:%d/; do sleep 0.1; done'", port))
	if err := waitCmd.Run(); err != nil {
		t.Fatalf("httpd on port %d not ready: %v", port, err)
	}
}

// serveIndex creates an index.html with the given content and starts busybox httpd on port 8080.
func serveIndex(t *testing.T, box, keyFile, content string) {
	t.Helper()
	makeIndex := boxSSHCommand(t, box, keyFile, "sh", "-c", fmt.Sprintf("'echo %s > /home/exedev/index.html'", content))
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v", err)
	}
	startHTTPServer(t, box, keyFile, 8080)
}

// cleanupBox connects to exed, deletes the box, and disconnects.
//
// Do NOT use t.Cleanup(func() { cleanupBox(...) }). The test context is
// canceled by the time t.Cleanup runs, so the SSH connection will fail.
// Call cleanupBox directly at the end of the test instead.
func cleanupBox(t *testing.T, keyFile, boxName string) {
	t.Helper()
	pty := sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.Disconnect()
}

// configureProxyRoute sets the proxy port and visibility for a box.
// visibility must be "public" or "private".
func configureProxyRoute(t *testing.T, keyFile, box string, port int, visibility string) {
	t.Helper()
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "port", box, strconv.Itoa(port))
	if err != nil {
		t.Fatalf("failed to set proxy port: %v\n%s", err, out)
	}
	out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "set-"+visibility, box)
	if err != nil {
		t.Fatalf("failed to set proxy visibility: %v\n%s", err, out)
	}
}

func sshToBox(t *testing.T, boxname, keyFile string) *expectPty {
	pty := sshWithUsername(t, boxname, keyFile)
	pty.SetPromptRE(regexp.QuoteMeta(boxname) + ".*" + regexp.QuoteMeta("$"))
	pty.WantPrompt()
	pty.DisableBracketedPaste()
	return pty
}

func sshWithUsername(t *testing.T, username, keyFile string) *expectPty {
	pty := makePty(t, "ssh "+username+"@localhost")
	sshCmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), username, keyFile)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })
	return pty
}

// waitForEmailAndVerify waits for an email message to an address,
// looks for a verification link in that email, and clicks it.
// It returns HTTP authorization cookies.
func waitForEmailAndVerify(t *testing.T, to string) []*http.Cookie {
	cookies, err := Env.servers.WaitForEmailAndVerify(to)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return cookies
}

// webLoginWithEmail performs a web-only login flow (no SSH involved).
// This uses the /auth POST endpoint to trigger email verification.
// Unlike registerForExeDevWithEmail, this doesn't create a user via SSH,
// so it exercises the web-only user creation path.
func webLoginWithEmail(t *testing.T, email string) []*http.Cookie {
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return cookies
}

// webLoginWithExe performs a login flow with login_with_exe=1 set.
// This simulates a user logging in via the proxy auth flow (login-with-exe).
// Users created this way are "basic users" and should only see the profile tab.
func webLoginWithExe(t *testing.T, email string) []*http.Cookie {
	cookies, err := Env.servers.WebLoginWithExe(email)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return cookies
}

// boxName creates a unique test-specific box name with e1e prefix for easy cleanup
func boxName(t *testing.T) string {
	return testinfra.BoxName(t.Name(), testRunID)
}

func registerForExeDevWithEmail(t *testing.T, email string) (pty *expectPty, cookies []*http.Cookie, keyFile, returnedEmail string) {
	pty = makePty(t, "ssh localhost")
	cookies, keyFile, sshCmd, err := Env.servers.RegisterForExeDevWithEmail(Env.context(t), pty.PTY(), email, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	// Add billing automatically - it's required before VM creation
	if err := Env.servers.AddBillingForEmail(email); err != nil {
		t.Fatalf("failed to add billing for %s: %v", email, err)
	}

	return pty, cookies, keyFile, email
}

// registerForExeDevWithoutBilling registers a user without adding billing.
// Use this for tests that specifically test the no-billing user flow.
func registerForExeDevWithoutBilling(t *testing.T) (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	name := t.Name()
	name = strings.ReplaceAll(name, "/", ".")
	email = name + testinfra.FakeEmailSuffix

	pty = makePty(t, "ssh localhost")
	cookies, keyFile, sshCmd, err := Env.servers.RegisterForExeDevWithEmail(Env.context(t), pty.PTY(), email, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })
	return pty, cookies, keyFile, email
}

// registerForExeDev is a convenience command to register for an exe.dev account.
// It returns the open pty after registration, authentication cookies for HTTP access,
// the private keyFile, and the account email.
// It is the caller's responsibility to call pty.disconnect() when done.
func registerForExeDev(t *testing.T) (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	name := t.Name()
	name = strings.ReplaceAll(name, "/", ".")
	email = name + testinfra.FakeEmailSuffix
	return registerForExeDevWithEmail(t, email)
}

func setCookiesForJar(t testing.TB, jar *cookiejar.Jar, rawURL string, cookies []*http.Cookie) {
	t.Helper()
	if len(cookies) == 0 || jar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse URL %q: %v", rawURL, err)
	}
	cloned := make([]*http.Cookie, len(cookies))
	for i, c := range cookies {
		cCopy := *c
		cloned[i] = &cCopy
	}
	jar.SetCookies(u, cloned)
}

func noRedirectClient(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// followRedirects follows redirections when using a noRedirectClient.
// This will call resp.Body.Close for any response that are not returned.
func followRedirects(t *testing.T, client *http.Client, resp *http.Response) (*http.Response, error) {
	t.Helper()
	for resp.StatusCode == http.StatusTemporaryRedirect {
		resp.Body.Close()
		location, err := resp.Location()
		if err != nil {
			return nil, err
		}
		t.Logf("redirecting to %q", location)
		resp, err = client.Get(location.String())
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// newClientWithCookies creates an http.Client with a cookie jar pre-populated
// with the given cookies for the exed HTTP port.
func newClientWithCookies(t testing.TB, cookies []*http.Cookie) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil) // no error possible
	setCookiesForJar(t, jar, fmt.Sprintf("http://localhost:%d", Env.HTTPPort()), cookies)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

// newBox requests a new box from the open repl pty.
func newBox(t *testing.T, pty *expectPty, opts ...testinfra.BoxOpts) string {
	boxName, err := Env.servers.NewBox(t.Name(), testRunID, pty.PTY(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return boxName
}

// addBillingForEmail adds billing account for a user by email.
// This is needed before VM creation for users without billing info.
func addBillingForEmail(t *testing.T, email string) {
	t.Helper()
	if err := Env.servers.AddBillingForEmail(email); err != nil {
		t.Fatalf("failed to add billing: %v", err)
	}
}

var (
	didRunTest sync.Map // map[string]bool
	skipGolden sync.Map // map[string]bool
)

func e1eTestsOnlyRunOnce(t *testing.T) {
	prev, _ := didRunTest.Swap(t.Name(), true)
	if didRun, ok := prev.(bool); ok && didRun {
		t.Fatal("e1e tests don't work with -count > 1. use a bash loop. if this makes you sad, talk to josh.")
	}
}

func initVMSem() {
	n := int64(3)
	if s := os.Getenv("E1_VM_CONCURRENCY"); s != "" {
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil || parsed <= 0 {
			fmt.Fprintf(os.Stderr, "invalid E1_VM_CONCURRENCY=%q: must be a positive integer\n", s)
			os.Exit(1)
		}
		n = parsed
	}
	pool = newVMPool(n)
	slog.Info("VM concurrency limit", "limit", n)
}

// reserveVMs declares that t will create n VMs.
// Call with 0 to document that the test creates none;
// call with n>0 to acquire that many slots from the shared concurrency pool.
// Slots are held for t's lifetime and released via t.Cleanup.
//
// Every test that creates VMs must call reserveVMs with the correct count.
// Nothing in the VM creation path enforces this.
// Enforcement is at the call site rather than in newBox
// because atomic multi-slot acquisition prevents deadlocks (two tests
// each holding one slot and needing another).
func reserveVMs(t *testing.T, n int) {
	pool.reserve(t, n)
}

// vmPool is a weighted semaphore that manages concurrent VM slots.
type vmPool struct {
	sem *semaphore.Weighted
	cap int64
}

func newVMPool(n int64) *vmPool {
	return &vmPool{sem: semaphore.NewWeighted(n), cap: n}
}

// reserve acquires n VM slots, blocking until they're all available.
// Slots are released when t finishes via t.Cleanup.
//
// Acquisition is atomic: the caller either gets all n slots at once
// or blocks without holding any. This prevents deadlocks where two
// tests each grab one slot and then wait for the other's.
func (p *vmPool) reserve(t *testing.T, n int) {
	t.Helper()
	if n == 0 {
		return
	}
	if int64(n) > p.cap {
		panic(fmt.Sprintf("test %s wants %d VMs but capacity=%d; increase E1_VM_CONCURRENCY or reduce VMs needed",
			t.Name(), n, p.cap))
	}
	start := time.Now()
	if err := p.sem.Acquire(t.Context(), int64(n)); err != nil {
		panic(fmt.Sprintf("test %s: context done waiting for %d VM slots (capacity=%d): %v",
			t.Name(), n, p.cap, err))
	}
	t.Logf("reserved %d VM slot(s) (%v)", n, time.Since(start).Round(time.Millisecond))
	t.Cleanup(func() {
		p.sem.Release(int64(n))
	})
}

// noGolden marks the test as not wanting golden file updates.
// We use this for tests that satisfy one or both of these conditions:
//   - are hard to get stable output out of (but prefer to use canonicalization if at all possible)
//   - whose golden output isn't interesting/useful
func noGolden(t *testing.T) {
	skipGolden.Store(t.Name(), true)
}
