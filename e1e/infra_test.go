// This file provides shared infrastructure for the e2e tests.

package e1e

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"github.com/Netflix/go-expect"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	ansiterm "github.com/veops/go-ansiterm"
)

var (
	flagVerbosePiperd = flag.Bool("vpiperd", false, "enable verbose logging from sshpiperd")
	flagVerboseExed   = flag.Bool("vexed", false, "enable verbose logging from exed")
	flagVerboseExelet = flag.Bool("vexelet", false, "enable verbose logging from exelet")
	flagVerbosePorts  = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerboseEmail  = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagVerbosePty    = flag.Bool("vpty", false, "enable verbose logging from pty connections")
	flagVerboseSlog   = flag.Bool("vslog", false, "enable verbose logging from slogs")
	flagVerboseAll    = flag.Bool("vv", false, "enable ALL verbose logging (shorthand for all -v* flags)")
	flagCinema        = flag.Bool("cinema", true, "enable ASCIIcinema recordings")
	flagCoverProfile  = flag.String("coverage-out", "e1e.cover", "path to write merged coverage profile")

	// testRunID is a random identifier for this test invocation.
	// A single container host is often shared across test and dev runs.
	// We use this ID to understand which boxes were created specifically by this test run.
	testRunID string
)

func TestMain(m *testing.M) {
	flag.Parse()

	// Enable all verbose flags if -vv is set
	if *flagVerboseAll {
		*flagVerbosePiperd = true
		*flagVerboseExed = true
		*flagVerboseExelet = true
		*flagVerbosePorts = true
		*flagVerboseEmail = true
		*flagVerbosePty = true
		*flagVerboseSlog = true
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

	if testing.Verbose() && !*flagVerboseAll && !*flagVerbosePiperd && !*flagVerboseExed && !*flagVerboseExelet && !*flagVerbosePorts && !*flagVerboseEmail && !*flagVerbosePty && !*flagVerboseSlog {
		fmt.Print(`
════════
-v requested, but the e1e tests generate lots of output, and they run in parallel.
Having "-v" enable extra logging is overwhelming.

For debug info, use -run to scope to a single test, and add some/all of these flags:

-vv       enable ALL verbose logging (shorthand for all flags below)
-vpiperd  print sshpiperd logs
-vexed    print exed logs
-vexelet  print exelet logs
-vports   print port mappings
-vemail   print email server logs
-vpty     print pty (ssh) logs
-vslog    print e1e test binary slogs

Flags must be added AFTER the paths, e.g., go test -v -count 1 -run TestHTTPProxyBasic ./e1e/... -vexed
════════

`)
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
		if err := env.Close(); err != nil {
			slog.Error("test cleanup failed", "error", err)
			fmt.Fprintf(os.Stderr, "\n\nERROR: %v\n\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	})

	Env = env
	slog.Info("running tests")

	exitCode = m.Run()

	testinfra.RunCleanups()

	os.Stderr.Sync()

	os.Exit(exitCode)
}

var logFiles = map[string]*os.File{
	"sshpiperd": nil,
	"exed":      nil,
	"exelet":    nil,
	"e1e":       nil,
}

var guidRegex = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

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
	*flagVerbosePorts = true
	*flagVerboseEmail = true
	return nil
}

var Env *testEnv

type testEnv struct {
	servers *testinfra.ServerEnv

	asciinemaMu      sync.Mutex // protects asciinemaWriters
	asciinemaWriters map[string]*expect.AsciinemaWriter

	canonicalizeMu sync.Mutex
	canonicalize   map[string]string // maps non-deterministic strings to deterministic ones
}

func (e *testEnv) sshPort() int {
	return e.servers.SSHProxy.Port()
}

// parseSSHHost extracts hostname from ssh:// URL
func parseSSHHost(ctrHost string) string {
	return strings.TrimPrefix(ctrHost, "ssh://")
}

func (t *testEnv) addCanonicalization(in any, canon string) {
	t.canonicalizeMu.Lock()
	defer t.canonicalizeMu.Unlock()
	key := fmt.Sprint(in)
	val, ok := t.canonicalize[key]
	if ok {
		if val != canon {
			panic(fmt.Sprintf("conflicting canonicalization for %q: %q vs %q", key, val, canon))
		}
		return
	}
	t.canonicalize[key] = canon
}

func (t *testEnv) canonicalizeString(s string) string {
	t.canonicalizeMu.Lock()
	defer t.canonicalizeMu.Unlock()
	// Build replacements and sort by key length descending to avoid
	// substring collisions. Replace longest keys first.
	pairs := make([][2]string, 0, len(t.canonicalize))
	for k, v := range t.canonicalize {
		pairs = append(pairs, [2]string{k, v})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		// primary: length of key (desc)
		if lenA, lenB := len(a[0]), len(b[0]); lenA != lenB {
			return cmp.Compare(lenB, lenA)
		}
		// secondary: key lexicographic (asc) for determinism
		return cmp.Compare(a[0], b[0])
	})
	kv := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		kv = append(kv, p[0], p[1])
	}
	s = strings.NewReplacer(kv...).Replace(s)
	// now canonicalize some other stuff using regexps :/
	s = regexp.MustCompile(`\(boldsoftware/exeuntu@sha256:[a-f0-9]{8}\)`).ReplaceAllString(s, `(boldsoftware/exeuntu@sha256:IMAGE_HASH)`)
	s = regexp.MustCompile(`Ready in [0-9.]+s!`).ReplaceAllString(s, `Ready in ELAPSED_TIME!`)
	s = regexp.MustCompile(`(?m)^.*?@localhost: Permission denied`).ReplaceAllString(s, `USER@localhost: Permission denied`)
	s = strings.ReplaceAll(s, "Press Enter to close this connection.\n", "Press Enter to close this connection.")
	// Canonicalize share tokens (26-character alphanumeric tokens that appear in share URLs or standalone)
	s = regexp.MustCompile(`(share=|\s)([A-Z0-9]{26})\b`).ReplaceAllString(s, `${1}SHARE_TOKEN`)
	// Canonicalize invitation timestamps
	s = regexp.MustCompile(`\(invited [^)]+\)`).ReplaceAllString(s, `(invited INVITE_AGE)`)
	// Canonicalize share link creation timestamps (e.g., "created now" or "created 1 second ago")
	s = regexp.MustCompile(`\(created [^,]+,`).ReplaceAllString(s, `(created SHARE_AGE,`)
	// Canonicalize random MOTD hints shown when SSHing to a box
	s = regexp.MustCompile(`(?s)(For support and documentation, "ssh exe\.dev" or visit https://exe\.dev/\n)\n(.+?)\n(exedev@)`).ReplaceAllString(s, "$1\nMOTD HINT\n\n$3")
	// Canonicalize shelley.backup timestamps (format: YYYYMMDD-HHMMSS)
	s = regexp.MustCompile(`shelley\.backup\.\d{8}-\d{6}`).ReplaceAllString(s, `shelley.backup.TIMESTAMP`)
	// Canonicalize REPL prompt (host varies by environment)
	s = regexp.MustCompile(`(?m)^[a-z0-9.-]+ ▶`).ReplaceAllString(s, `PROMPT ▶`)
	return s
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
	slog.Info("COVERAGE", "exed_dir", e.servers.Exed.CoverDir, "exelet_dir", coverDirs)

	if cd := e.servers.Exed.CoverDir; cd != "" {
		coverDirs = append(coverDirs, cd)
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
	env := &testEnv{
		asciinemaWriters: make(map[string]*expect.AsciinemaWriter),
		canonicalize:     make(map[string]string),
	}

	// We have a circular dependency:
	// exelet needs to know exed's HTTP port,
	// but exed needs to know exelet's address.
	// Start a TCP proxy for exed HTTP that
	// we can give to exelet immediately.
	// TODO: figure out why we're seeing connections
	// before setDestPort is called, and stop doing that.
	exedHTTPProxy, err := testinfra.NewTCPProxy("exedHTTPProxy")
	if err != nil {
		return env, fmt.Errorf("failed to create exed HTTP proxy: %w", err)
	}
	go exedHTTPProxy.Serve(context.Background())
	if *flagVerbosePorts {
		slog.Info("exed HTTP proxy listening", "port", exedHTTPProxy.Port())
	}

	exeletBinary, err := testinfra.BuildExeletBinary()
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
	exelet, err := testinfra.StartExelet(context.Background(), exeletBinary, ctrHost, exedHTTPProxy.Port(), testRunID, exeletLog, *flagVerbosePorts)
	if err != nil {
		return env, err
	}
	env.addCanonicalization(exelet.Address, "EXELET_ADDRESS")
	env.addCanonicalization(exelet.HTTPAddress, "EXELET_HTTP_ADDRESS")

	var exedLog io.Writer
	if *flagVerboseExed {
		exedLog = logFileFor("exed")
	}

	var piperLog io.Writer
	if *flagVerbosePiperd {
		piperLog = logFileFor("sshpiperd")
	}

	serverEnv, err := testinfra.StartServers(context.Background(),
		[]*testinfra.ExeletInstance{exelet},
		exedHTTPProxy,
		exedLog,
		piperLog,
		*flagVerbosePorts,
		*flagVerboseEmail,
	)
	env.servers = serverEnv
	if err != nil {
		return env, err
	}

	env.addCanonicalization(serverEnv.Exed.SSHPort, "EXED_SSH_PORT")
	env.addCanonicalization(serverEnv.Exed.HTTPPort, "EXED_HTTP_PORT")
	env.addCanonicalization(serverEnv.Exed.PiperPluginPort, "EXED_PIPER_PLUGIN_PORT")
	env.addCanonicalization(serverEnv.SSHPiperd.Port, "PIPERD_PORT")
	env.addCanonicalization(serverEnv.Email.Port, "EMAIL_SERVER_PORT")
	env.addCanonicalization(serverEnv.SSHProxy.Port(), "SSH_PORT")

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
	Env.addCanonicalization(publickey, "SSH_PUBKEY")
	return path, publickey
}

const (
	banner       = "~~~ EXE.DEV ~~~"
	exeDevPrompt = "\033[1;36mlocalhost\033[0m \033[37m▶\033[0m "
)

type expectPty struct {
	t        *testing.T
	prompt   string
	promptRe string
	console  *expect.Console
}

func (p *expectPty) want(s string) {
	p.t.Helper()
	out, err := p.console.ExpectString(s)
	if err != nil {
		p.t.Fatalf("want %q in output (%v). actual output:\n%s", s, err, out)
	}
}

func (p *expectPty) reject(s string) {
	p.t.Helper()
	p.console.RejectString(s)
}

func (p *expectPty) wantf(msg string, args ...any) {
	p.t.Helper()
	p.want(fmt.Sprintf(msg, args...))
}

func (p *expectPty) wantRe(re string) {
	p.t.Helper()
	out, err := p.console.Expect(
		expect.RegexpPattern(re),
	)
	if err != nil {
		p.t.Fatalf("want %q match in output (%v). actual output:\n%s", re, err, out)
	}
}

func (p *expectPty) wantPrompt() {
	p.t.Helper()
	if p.promptRe != "" {
		p.wantRe(p.promptRe)
		return
	}
	if p.prompt != "" {
		p.want(p.prompt)
		return
	}
	p.t.Fatalf("expectPty: no prompt or promptRe set")
}

func (p *expectPty) send(s string) {
	p.t.Helper()
	if _, err := p.console.Send(s); err != nil {
		p.t.Fatalf("failed to send %q: %v", s, err)
	}
}

func (p *expectPty) sendLine(s string) {
	p.t.Helper()
	p.send(s + "\n")
}

func (p *expectPty) disconnect() {
	p.t.Helper()
	p.sendLine("exit")
	p.wantEOF()
}

func (p *expectPty) deleteBox(boxName string) {
	p.t.Helper()
	p.sendLine("rm " + boxName)
	p.want("Deleting")
	p.reject("internal error")
	p.want("success")
	p.wantPrompt()
}

func (p *expectPty) wantEOF() {
	p.t.Helper()
	if out, err := p.console.ExpectEOF(); err != nil {
		p.t.Fatalf("want EOF in output (%v). output: %s", err, out)
	}
}

// attachAndStart attaches the pty to the given command and starts it.
func (p *expectPty) attachAndStart(cmd *exec.Cmd) {
	// Configure and attach.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.console.Tty(), p.console.Tty(), p.console.Tty()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}

	// Start the command.
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("failed to start %v: %v", cmd, err)
	}
	pty.Setsize(p.console.Tty(), &pty.Winsize{Rows: 120, Cols: 240})
	// sshCmd now owns the PTY; close our reference.
	// Without this, linux hangs on disconnect waiting for EOF.
	p.console.Tty().Close()
	p.t.Cleanup(func() { _ = cmd.Wait() })
}

func makePty(t *testing.T, name string) *expectPty {
	t.Helper()
	opts := []expect.ConsoleOpt{
		// TODO: reduce this timeout.
		// josh increased it on sep 15 because performance regressions in box startup made it necessary to avoid flakiness.
		expect.WithDefaultRefreshingTimeout(time.Minute),
	}
	if *flagVerbosePty {
		opts = append(opts, expect.WithStdout(os.Stdout))
	}

	// Add ASCIIcinema recording if -cinema flag is set
	var cinemaOpts []expect.ConsoleOpt
	if *flagCinema {
		cinemaOpts = cinemaOptsForTest(t)
	}
	opts = append(opts, cinemaOpts...)

	sshConsole, err := expect.NewConsole(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshConsole.Close() })

	// Write marker to asciinema recording when new PTY is created
	if *flagCinema && sshConsole.IsRecording() {
		box := fmt.Sprintf("\n\n●\r\n● %s\r\n●\r\n\n", name)
		sshConsole.WriteAsciinemaMarker(box)
	}

	return &expectPty{t: t, console: sshConsole}
}

func cinemaOptsForTest(t *testing.T) []expect.ConsoleOpt {
	testName := t.Name()
	Env.asciinemaMu.Lock()
	defer Env.asciinemaMu.Unlock()

	writer, ok := Env.asciinemaWriters[testName]
	if !ok {
		// TODO: snake case
		baseName := strings.ReplaceAll(testName, "/", "_")
		castFile := baseName + ".cast"

		const width = 120
		const height = 32
		var err error
		writer, err = expect.NewAsciinemaWriter(castFile, width, height)
		if err != nil {
			t.Fatalf("failed to create ASCIIcinema writer: %v", err)
		}

		Env.asciinemaWriters[testName] = writer
		t.Cleanup(func() {
			if t.Failed() {
				// Don't overwrite existing golden files on failure.
				// It's annoying to have to clean them up.
				return
			}
			writer.Close()
			_, skip := skipGolden.Load(t.Name())
			if !skip {
				if err := writeAsciinemaToText(castFile, baseName); err != nil {
					fmt.Fprintf(os.Stderr, "failed to write asciinema->text file: %v\n", err)
				}
			}
			Env.asciinemaMu.Lock()
			defer Env.asciinemaMu.Unlock()
			delete(Env.asciinemaWriters, testName)
		})
	}

	return []expect.ConsoleOpt{expect.WithAsciinemaWriter(writer)}
}

func writeAsciinemaToText(castFile, baseName string) error {
	// Convert asciinema -> text
	castData, err := os.ReadFile(castFile)
	if err != nil {
		return fmt.Errorf("failed to read cast file %s: %w", castFile, err)
	}

	text, err := asciinemaToText(castData)
	if err != nil {
		return fmt.Errorf("failed to convert %s to text: %v\n", castFile, err)
	}
	text = Env.canonicalizeString(text)

	textFile := filepath.Join("golden", baseName+".txt")
	if err := os.WriteFile(textFile, []byte(text), 0o600); err != nil {
		return fmt.Errorf("failed to write text file %s: %w", textFile, err)
	}

	return nil
}

func sshToExeDev(t *testing.T, keyFile string) *expectPty {
	pty := sshWithUsername(t, "", keyFile)
	pty.prompt = exeDevPrompt
	return pty
}

func runExeDevSSHCommand(t *testing.T, keyFile string, args ...string) ([]byte, error) {
	sshArgs := baseSSHArgs("", keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	out, err := sshCmd.CombinedOutput()
	if strings.Contains(string(out), "\r") {
		t.Errorf("ssh output contains \\r, did REPL formatting sneak through? raw output:\n%q", string(out))
	}
	if ansi.Strip(string(out)) != string(out) {
		t.Errorf("ssh output contains ANSI escape codes, did REPL formatting sneak through? raw output:\n%q", string(out))
	}
	return out, err
}

func runParseExeDevJSON[T any](t *testing.T, keyFile string, args ...string) T {
	t.Helper()
	var result T
	out, err := runExeDevSSHCommand(t, keyFile, args...)
	if err != nil {
		t.Fatalf("failed to run command: %v\n%s", err, out)
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse command output as JSON: %v\n%s", err, out)
	}
	return result
}

func boxSSHCommand(t *testing.T, boxname, keyFile string, args ...string) *exec.Cmd {
	return boxSSHCommandContext(Env.context(t), boxname, keyFile, args...)
}

func boxSSHCommandContext(ctx context.Context, boxname, keyFile string, args ...string) *exec.Cmd {
	sshArgs := baseSSHArgs(boxname, keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	return sshCmd
}

// waitForSSH blocks until SSH is responsive on the given box.
func waitForSSH(t *testing.T, boxName, keyFile string) {
	// Wait for SSH to be responsive (systemd may take time to initialize).
	var err error
	for range 300 {
		err = boxSSHCommand(t, boxName, keyFile, "true").Run()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("box ssh did not come up, last error: %v", err)
}

func sshToBox(t *testing.T, boxname, keyFile string) *expectPty {
	pty := sshWithUsername(t, boxname, keyFile)
	pty.promptRe = regexp.QuoteMeta(boxname) + ".*" + regexp.QuoteMeta("$")
	return pty
}

func usernameAt(username string) string {
	if username == "" {
		return ""
	}
	return username + "@"
}

func baseSSHArgs(username, keyFile string) []string {
	args := sshOpts()
	args = append(args,
		"-p", fmt.Sprint(Env.sshPort()),
		"-o", "IdentityFile="+keyFile,
		usernameAt(username)+"localhost",
	)
	return args
}

func sshOpts() []string {
	return []string{
		"-F", "/dev/null",
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		"-o", "PreferredAuthentications=publickey",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ChallengeResponseAuthentication=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=5", // 5 second connection timeout
		"-o", "ServerAliveInterval=5", // send keepalive every 5 seconds
		"-o", "ServerAliveCountMax=2", // disconnect after 2 failed keepalives (10s total)
	}
}

func sshWithUsername(t *testing.T, username, keyFile string) *expectPty {
	pty := makePty(t, "ssh "+usernameAt(username)+"localhost")
	sshArgs := baseSSHArgs(username, keyFile)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	// fmt.Println("RUNNING", sshCmd)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	pty.attachAndStart(sshCmd)
	return pty
}

// waitForEmailAndVerify waits for an email message to an address,
// looks for a verification link in that email, and clicks it.
// It returns HTTP authorization cookies.
func waitForEmailAndVerify(t *testing.T, to string) []*http.Cookie {
	msg, err := Env.servers.Email.WaitForEmail(to)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return clickVerifyLinkInEmail(t, msg)
}

func clickVerifyLinkInEmail(t *testing.T, emailMsg *testinfra.EmailMessage) []*http.Cookie {
	verifyURL, err := testinfra.ExtractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	parsedVerifyURL, err := url.Parse(verifyURL)
	if err != nil {
		t.Fatalf("failed to parse verification URL %q: %v", verifyURL, err)
	}

	// Step 1: GET the verification page (shows confirmation form)
	getResp, err := http.Get(verifyURL)
	if err != nil {
		t.Fatalf("failed to access verification page: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("verification page request failed with status: %d", getResp.StatusCode)
	}

	htmlBody, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("failed to read verification page body: %v", err)
	}
	getResp.Body.Close()

	bodyStr := string(htmlBody)

	// // Extract the pairing code from the verification page
	// codeRe := regexp.MustCompile(`tracking-widest[^>]*>([0-9]{6})<`)
	// if codeMatches := codeRe.FindStringSubmatch(bodyStr); len(codeMatches) >= 2 {
	// 	Env.addCanonicalization(codeMatches[1], "EMAIL_VERIFICATION_CODE")
	// }

	// Extract hidden inputs so we can POST the same form fields back
	hiddenRe := regexp.MustCompile(`<input[^>]+name="([^"]+)"[^>]+value="([^"]*)"[^>]*>`)
	formData := url.Values{}
	for _, match := range hiddenRe.FindAllStringSubmatch(bodyStr, -1) {
		name := match[1]
		value := html.UnescapeString(match[2])
		formData.Set(name, value)
	}

	token := formData.Get("token")
	if token == "" {
		t.Fatalf("failed to extract token from HTML form: %s", bodyStr)
	}
	Env.addCanonicalization(token, "EMAIL_VERIFICATION_TOKEN")

	// Determine form action (defaults to /verify-email if not found)
	actionRe := regexp.MustCompile(`<form[^>]+action="([^"]+)"`)
	actionMatch := actionRe.FindStringSubmatch(bodyStr)
	actionPath := "/verify-email"
	if len(actionMatch) >= 2 {
		actionPath = actionMatch[1]
	}
	if !strings.HasPrefix(actionPath, "/") {
		actionPath = "/" + actionPath
	}

	// Create HTTP client with cookie jar to capture authentication cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postURL := fmt.Sprintf("http://localhost:%d%s", Env.servers.Exed.HTTPPort, actionPath)
	postResp, err := client.PostForm(postURL, formData)
	if err != nil {
		t.Fatalf("failed to submit verification form: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(postResp.Body)
		t.Errorf("verification form submission returned status: %d, body: %s", postResp.StatusCode, string(body))
	}
	postResp.Body.Close()

	// Extract cookies from the response
	cookies := jar.Cookies(parsedVerifyURL)
	if len(cookies) == 0 {
		parsedPostURL, _ := url.Parse(postURL)
		cookies = jar.Cookies(parsedPostURL)
	}

	return cookies
}

// webLoginWithEmail performs a web-only login flow (no SSH involved).
// This uses the /auth POST endpoint to trigger email verification.
// Unlike registerForExeDevWithEmail, this doesn't create a user via SSH,
// so it exercises the web-only user creation path.
func webLoginWithEmail(t *testing.T, email string) []*http.Cookie {
	t.Helper()

	// POST to /auth with email to trigger the web login flow
	authURL := fmt.Sprintf("http://localhost:%d/auth", Env.servers.Exed.HTTPPort)
	resp, err := http.PostForm(authURL, url.Values{"email": {email}})
	if err != nil {
		t.Fatalf("failed to POST to /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /auth failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Wait for verification email and click verification link
	// (same as SSH flow).
	return waitForEmailAndVerify(t, email)
}

// webLoginWithExe performs a login flow with login_with_exe=1 set.
// This simulates a user logging in via the proxy auth flow (login-with-exe).
// Users created this way are "basic users" and should only see the profile tab.
func webLoginWithExe(t *testing.T, email string) []*http.Cookie {
	t.Helper()

	// POST to /auth with email AND login_with_exe=1 to trigger login-with-exe flow
	authURL := fmt.Sprintf("http://localhost:%d/auth", Env.servers.Exed.HTTPPort)
	resp, err := http.PostForm(authURL, url.Values{
		"email":          {email},
		"login_with_exe": {"1"},
	})
	if err != nil {
		t.Fatalf("failed to POST to /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /auth failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Wait for verification email and verify
	// (same as SSH flow).
	return waitForEmailAndVerify(t, email)
}

var boxCounter atomic.Int32

// boxName creates a unique test-specific box name with e1e prefix for easy cleanup
func boxName(t *testing.T) string {
	t.Helper()
	// Create a unique test-specific box name: "e1e-{runid}-{counter}-{testname}"
	// runid provides cross-process uniqueness.
	// counter covers within-process uniqueness.
	// e1e prefix and testname are for debuggability.
	counter := fmt.Sprintf("%04x", boxCounter.Add(1))
	testName := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	// Sanitize to allowed charset [a-z0-9-] to satisfy isValidBoxName
	testName = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(testName, "-")
	// Collapse multiple hyphens and trim
	testName = regexp.MustCompile(`-+`).ReplaceAllString(testName, "-")
	testName = strings.Trim(testName, "-")
	boxName := fmt.Sprintf("e1e-%s-%s-%s", testRunID, counter, testName)
	Env.addCanonicalization(boxName, "BOX_NAME")
	return boxName
}

func registerForExeDevWithEmail(t *testing.T, email string) (pty *expectPty, cookies []*http.Cookie, keyFile, returnedEmail string) {
	keyFile, publicKey := genSSHKey(t)
	pty = sshToExeDev(t, keyFile)
	pty.want(banner)

	pty.want("Please enter your email")
	pty.sendLine(email)
	returnedEmail = email
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")

	cookies = waitForEmailAndVerify(t, email)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.wantPrompt()

	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	return pty, cookies, keyFile, returnedEmail
}

// registerForExeDev is a convenience command to register for an exe.dev account.
// It returns the open pty after registration, authentication cookies for HTTP access,
// the private keyFile, and the account email.
// It is the caller's responsibility to call pty.disconnect() when done.
func registerForExeDev(t *testing.T) (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	name := t.Name()
	name = strings.ReplaceAll(name, "/", ".")
	email = name + "@example.com"
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

// BoxOpts holds optional parameters for newBox.
type BoxOpts struct {
	Image   string
	Command string
}

// newBox requests a new box from the open repl pty.
func newBox(t *testing.T, pty *expectPty, opts ...BoxOpts) string {
	boxName := boxName(t)
	boxNameRe := regexp.QuoteMeta(boxName)

	// Use first opts if provided, otherwise default
	var opt BoxOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Build the command line
	cmdLine := "new --name=" + boxName
	if opt.Image != "" {
		cmdLine += " --image=" + strconv.Quote(opt.Image)
	}
	if opt.Command != "" {
		cmdLine += " --command=" + strconv.Quote(opt.Command)
	}

	pty.sendLine(cmdLine)
	pty.reject("Sorry")
	pty.wantRe("Creating .*" + boxNameRe)
	// Calls to action
	pty.want("App")
	pty.want("http://")
	pty.want("SSH")
	pty.wantf("ssh -p %v %v@exe.cloud", Env.sshPort(), boxName)

	// Confirm it is there.
	pty.sendLine("ls")
	pty.want("VMs")
	pty.wantRe(boxNameRe + ".*running.*\n")
	return boxName
}

func asciinemaToText(castData []byte) (string, error) {
	// asciinema has a size header, but we ignore it.
	// this isn't safe in general, but it makes sense for us, in our context.
	// width and height should both be generous for consistency and to avoid losing scrollback.
	screen := ansiterm.NewScreen(1024, 16384)
	stream := ansiterm.InitByteStream(screen, false)
	stream.Attach(screen)

	// discard header
	_, castLines, ok := bytes.Cut(castData, []byte("\n"))
	if !ok {
		return "", fmt.Errorf("failed to cut header from cast data")
	}
	dec := json.NewDecoder(bytes.NewReader(castLines))
NextLine:
	for {
		var ev []any
		err := dec.Decode(&ev)
		switch {
		case errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF):
			break NextLine
		case err != nil:
			return "", fmt.Errorf("failed to decode event: %w", err)
		}
		if len(ev) != 3 {
			continue
		}
		if typ, _ := ev[1].(string); typ == "o" {
			if data, _ := ev[2].(string); data != "" {
				stream.Feed([]byte(data))
			}
		}
	}

	lines := screen.Display()
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Some ptys like to use a bunch of trailing spaces followed by a series of \b,
	// in order to "clear" the line.
	// This varies by OS, because of course it does.
	// Canonicalize by trimming all trailing spaces.
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	outText := strings.Join(lines, "\n") + "\n"
	return outText, nil
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

// noGolden marks the test as not wanting golden file updates.
// We use this for tests that satisfy one or both of these conditions:
//   - are hard to get stable output out of (but prefer to use canonicalization if at all possible)
//   - whose golden output isn't interesting/useful
func noGolden(t *testing.T) {
	skipGolden.Store(t.Name(), true)
}
