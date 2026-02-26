package testinfra

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	expect "github.com/Netflix/go-expect"
	"github.com/creack/pty"
	ansiterm "github.com/veops/go-ansiterm"
)

// PTY is a pseudo-terminal that supports searching for strings
// that appear on the terminal and supports keeping a ASCIInema recording
// of I/O on the terminal.
type PTY struct {
	prompt   string
	promptRE string
	console  *expect.Console
}

// MakePTY makes a new PTY.
//
// cinemaName is the name to use for the ASCIIenema file.
// If this is the empty string, no file is recorded.
// This is normally the name of the test.
//
// name is the name of the PTY for ASCIInema output.
//
// verbose is whether to display pty I/O on stdout.
//
// This returns the PTY and reports whether it's seen the cinemaName before.
func MakePTY(cinemaName, name string, verbose bool) (*PTY, bool, error) {
	opts := []expect.ConsoleOpt{
		// TODO: reduce this timeout.
		// josh increased it on sep 15 because performance
		// regressions in box startup made it necessary to
		// avoid flakiness.
		expect.WithDefaultRefreshingTimeout(time.Minute),
	}

	if verbose {
		opts = append(opts, expect.WithStdout(os.Stdout))
	}

	// Add ASCIIcinema recording if requested.
	seen := false
	if cinemaName != "" {
		var copts []expect.ConsoleOpt
		var err error
		copts, seen, err = cinemaOpts(cinemaName)
		if err != nil {
			return nil, false, err
		}
		opts = append(opts, copts...)
	}

	console, err := expect.NewConsole(opts...)
	if err != nil {
		return nil, false, err
	}

	// Write marker to asciinema recording when new PTY is created
	if cinemaName != "" && console.IsRecording() {
		box := fmt.Sprintf("\n\n●\r\n● %s\r\n●\r\n\n", name)
		console.WriteAsciinemaMarker(box)
	}

	return &PTY{console: console}, seen, nil
}

// Close closes the PTY.
func (p *PTY) Close() error {
	return p.console.Close()
}

// TTY returns the terminal associated with the PTY.
func (p *PTY) TTY() *os.File {
	return p.console.Tty()
}

// Want looks for a string in the PTY output,
// and returns an error if not found.
func (p *PTY) Want(s string) error {
	out, err := p.console.ExpectString(s)
	if err != nil {
		return fmt.Errorf("want %q in output (%v), actual output:\n%s", s, err, out)
	}
	return nil
}

// Wantf is like Want, but with formatting.
func (p *PTY) Wantf(msg string, args ...any) error {
	return p.Want(fmt.Sprintf(msg, args...))
}

// WantRE is like Want, but with a regular expression.
func (p *PTY) WantRE(re string) error {
	out, err := p.console.Expect(
		expect.RegexpPattern(re),
	)
	if err != nil {
		return fmt.Errorf("want %q match in output (%v), actual output\n%s", re, err, out)
	}
	return nil
}

// SetPrompt sets a prompt for WantPrompt.
func (p *PTY) SetPrompt(prompt string) {
	if p.promptRE != "" {
		panic(fmt.Sprintf("saw both prompt %q and promptRE %q", prompt, p.promptRE))
	}
	p.prompt = prompt
}

// SetPromptRE sets a prompt regular expression for WantPrompt.
func (p *PTY) SetPromptRE(promptRE string) {
	if p.prompt != "" {
		panic(fmt.Sprintf("saw both prompt %q and promptRE %q", p.prompt, promptRE))
	}
	p.promptRE = promptRE
}

// WantPrompt expects to see a prompt in the PTY output.
// Either SetPrompt or SetPromptRE must be called first.
func (p *PTY) WantPrompt() error {
	if p.promptRE != "" {
		return p.WantRE(p.promptRE)
	}
	if p.prompt != "" {
		return p.Want(p.prompt)
	}
	return errors.New("WantPrompt called before SetPrompt or SetPromptRE")
}

// Send writes a string to the PTY.
func (p *PTY) Send(s string) error {
	_, err := p.console.Send(s)
	return err
}

// SendLine writes a string followed by a newline to the PTY.
func (p *PTY) SendLine(s string) error {
	return p.Send(s + "\n")
}

// Disconnect disconnects the PTY.
func (p *PTY) Disconnect() error {
	if err := p.SendLine("exit"); err != nil {
		return err
	}
	return p.WantEOF()
}

// WantEOF expects EOF from the PTY.
func (p *PTY) WantEOF() error {
	if out, err := p.console.ExpectEOF(); err != nil {
		return fmt.Errorf("want EOF in output (%v), output:\n%s", err, out)
	}
	return nil
}

// Reject adds a reject string to the PTY.
// If the string is seen, other PTY operations will return an error.
func (p *PTY) Reject(s string) {
	p.console.RejectString(s)
}

// AttachAndStart attaches the PTY to the given command and starts it.
func (p *PTY) AttachAndStart(cmd *exec.Cmd) error {
	tty := p.TTY()
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %v: %v", cmd, err)
	}

	pty.Setsize(tty, &pty.Winsize{Rows: 120, Cols: 240})

	// The command now owns the PTY; close our reference.
	// Without this, Linux hangs on disconnect waiting for EOF.
	tty.Close()

	return nil
}

var (
	// asciinemaMu protects asciinemaWriters.
	asciinemaMu sync.Mutex

	// asciinemaWriters maps from cinema names to asciinema writers.
	asciinemaWriters = make(map[string]*expect.AsciinemaWriter)
)

// cinemaOpts returns the expect options for cinema recording.
// It also reports whether we've seen cinemaName before.
func cinemaOpts(cinemaName string) ([]expect.ConsoleOpt, bool, error) {
	asciinemaMu.Lock()
	defer asciinemaMu.Unlock()

	writer, ok := asciinemaWriters[cinemaName]
	if !ok {
		baseName := cinemaNameToBaseName(cinemaName)
		castFile := baseName + ".cast"

		const width = 120
		const height = 32
		var err error
		writer, err = expect.NewAsciinemaWriter(castFile, width, height)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create ASCIInema writer: %v", err)
		}

		asciinemaWriters[cinemaName] = writer
	}

	return []expect.ConsoleOpt{expect.WithAsciinemaWriter(writer)}, ok, nil
}

// cinemaNameToBaseName converts the cinema name, typically a test name,
// to a useful file name.
func cinemaNameToBaseName(cinemaName string) string {
	// TODO: snake case.
	return strings.ReplaceAll(cinemaName, "/", "_")
}

// WriteASCIInemaFile writes out the asciinema file in text form for a PTY.
// The file is written to dir.
func WriteASCIInemaFile(dir, cinemaName string) error {
	asciinemaMu.Lock()
	writer := asciinemaWriters[cinemaName]
	delete(asciinemaWriters, cinemaName)
	asciinemaMu.Unlock()

	if writer == nil {
		return fmt.Errorf("WriteASCIInemaFile called for nonexistent writer %s", cinemaName)
	}

	writer.Close()

	baseName := cinemaNameToBaseName(cinemaName)
	castFile := baseName + ".cast"

	if err := writeASCIInemaToText(dir, castFile, baseName); err != nil {
		return err
	}

	return nil
}

// writeASCIInemaToText writes out a text file with the cinema recording.
// The text file is placed in the directory "golden".
// Strings are canonicalized in the text file.
func writeASCIInemaToText(dir, castFile, baseName string) error {
	castData, err := os.ReadFile(castFile)
	if err != nil {
		return fmt.Errorf("failed to read cast file %s: %v", castFile, err)
	}

	text, err := asciinemaToText(castData)
	if err != nil {
		return fmt.Errorf("failed to convert %s to text: %v", castFile, err)
	}

	text = canonicalizeString(text)

	textFile := filepath.Join(dir, baseName+".txt")
	if err := os.WriteFile(textFile, []byte(text), 0o600); err != nil {
		return fmt.Errorf("failed to write text file for %s: %v", textFile, err)
	}

	return nil
}

// asciinemaToText converts the asciinema recording to text.
func asciinemaToText(castData []byte) (string, error) {
	// asciinema has a size header, but we ignore it.
	// This isn't safe in general, but it makes sense for us,
	// in our context.
	// Width and height should both be generous for consistency
	// and to avoid losing scrollback.
	screen := ansiterm.NewScreen(1024, 16384)
	stream := ansiterm.InitByteStream(screen, false)
	stream.Attach(screen)

	// Discard header.
	_, castLines, ok := bytes.Cut(castData, []byte("\n"))
	if !ok {
		return "", errors.New("failed to cut header from cast data")
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
			return "", fmt.Errorf("failed to decode event: %v", err)
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

	// Some PTYs like to use a bunch of trailing spaces
	// followed by a series of \b,
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
	// canonicalizeMu protects canonicalize.
	canonicalizeMu sync.Mutex

	// canonicalize is used to convert indeterminate strings
	// to a canonical form for asciinema recordings.
	canonicalize = make(map[string]string)
)

// AddCanonicalization adds a canonicalization from some value to a string.
func AddCanonicalization(in any, canon string) {
	canonicalizeMu.Lock()
	defer canonicalizeMu.Unlock()

	key := fmt.Sprint(in)
	val, ok := canonicalize[key]
	if ok {
		if val != canon {
			panic(fmt.Sprintf("conflicting canonicalizations for %q: %q vs %q", key, val, canon))
		}
		return
	}
	canonicalize[key] = canon
}

// canonicalizeString canonicalizes a string for golden output.
func canonicalizeString(s string) string {
	canonicalizeMu.Lock()
	defer canonicalizeMu.Unlock()

	// Build replacements and sort by key length descending to avoid
	// substring collisions. Replace longest keys first.
	pairs := make([][2]string, 0, len(canonicalize))
	for k, v := range canonicalize {
		pairs = append(pairs, [2]string{k, v})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		// primary: length of key (desc)
		if r := cmp.Compare(len(a[0]), len(b[0])); r != 0 {
			return r
		}
		// secondary: key lexicographic (asc) for determinism
		return cmp.Compare(a[0], b[0])
	})
	kv := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		kv = append(kv, p[0], p[1])
	}
	s = strings.NewReplacer(kv...).Replace(s)

	// Now canonicalize some other stuff using regexps :/.
	s = regexp.MustCompile(`\(boldsoftware/exeuntu@sha256:[a-f0-9]{8}\)`).ReplaceAllString(s, `(boldsoftware/exeuntu@sha256:IMAGE_HASH)`)
	s = regexp.MustCompile(`Ready in [0-9.]+s!`).ReplaceAllString(s, `Ready in ELAPSED_TIME!`)
	// Canonicalize clone/copy timing (e.g., "Created foo from bar in 0.4s").
	s = regexp.MustCompile(` in [0-9.]+s(\r?\n)`).ReplaceAllString(s, ` in ELAPSED_TIME$1`)
	s = regexp.MustCompile(`(?m)^.*?@localhost: Permission denied`).ReplaceAllString(s, `USER@localhost: Permission denied`)
	s = strings.ReplaceAll(s, "Press Enter to close this connection.\n", "Press Enter to close this connection.")
	// Canonicalize share tokens (26-character alphanumeric tokens
	// that appear in share URLs or standalone).
	s = regexp.MustCompile(`(share=|\s)([A-Z0-9]{26})\b`).ReplaceAllString(s, `${1}SHARE_TOKEN`)
	// Canonicalize invitation timestamps.
	s = regexp.MustCompile(`\(invited [^)]+\)`).ReplaceAllString(s, `(invited INVITE_AGE)`)
	// Canonicalize share link creation timestamps
	// (e.g., "created now" or "created 1 second ago").
	s = regexp.MustCompile(`\(created [^,]+,`).ReplaceAllString(s, `(created SHARE_AGE,`)
	// Canonicalize random MOTD hints shown when SSHing to a box.
	s = regexp.MustCompile(`(?s)(For support and documentation, "ssh exe\.dev" or visit https://exe\.dev/\n)\n(.+?)\n(exedev@)`).ReplaceAllString(s, "$1\nMOTD HINT\n\n$3")
	// Canonicalize shelley.backup timestamps (format: YYYYMMDD-HHMMSS).
	s = regexp.MustCompile(`shelley\.backup\.\d{8}-\d{6}`).ReplaceAllString(s, `shelley.backup.TIMESTAMP`)
	// Canonicalize REPL prompt (host varies by environment).
	s = regexp.MustCompile(`(?m)^[a-z0-9.-]+ ▶`).ReplaceAllString(s, `PROMPT ▶`)
	return s
}

// TestPTY is a wrapper around PTY for testing.
// It has more or less the same methods,
// but errors cause fatal test failures rather than being returned.
type TestPTY struct {
	pty *PTY
	t   *testing.T
}

// MakeTestPTY makes a TestPTY.
func MakeTestPTY(t *testing.T, cinemaName, name string, verbose bool) (*TestPTY, bool) {
	pty, seen, err := MakePTY(cinemaName, name, verbose)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	ret := &TestPTY{
		pty: pty,
		t:   t,
	}
	return ret, seen
}

// PTY returns the PTY stored in the TestPTY.
func (tp *TestPTY) PTY() *PTY {
	return tp.pty
}

// T returns the testing.T stored in the TestPTY.
func (tp *TestPTY) T() *testing.T {
	return tp.t
}

// Close closes the TestPTY.
func (tp *TestPTY) Close() {
	if err := tp.pty.Close(); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// TTY returns the terminal associated with the TestPTY.
func (tp *TestPTY) TTY() *os.File {
	return tp.pty.TTY()
}

// Want looks for a string in the TestPTY output,
// and fails the test if not found.
func (tp *TestPTY) Want(s string) {
	if err := tp.pty.Want(s); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// Wantf is like Want, but with formatting.
func (tp *TestPTY) Wantf(msg string, args ...any) {
	if err := tp.pty.Wantf(msg, args...); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// WantRE is like Want, but with a regular expression.
func (tp *TestPTY) WantRE(re string) {
	if err := tp.pty.WantRE(re); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// SetPrompt sets a prompt for WantPrompt.
func (tp *TestPTY) SetPrompt(prompt string) {
	tp.pty.SetPrompt(prompt)
}

// SetPromptRE sets a prompt regular expression for WantPrompt.
func (tp *TestPTY) SetPromptRE(promptRE string) {
	tp.pty.SetPromptRE(promptRE)
}

// WantPrompt expects to see a prompt in the PTY output.
// Either SetPrompt or SetPromptRE must be called first.
func (tp *TestPTY) WantPrompt() {
	if err := tp.pty.WantPrompt(); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// Send writes a string to the PTY.
func (tp *TestPTY) Send(s string) {
	if err := tp.pty.Send(s); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// SendLine writes a string followed by a newline to the PTY.
func (tp *TestPTY) SendLine(s string) {
	if err := tp.pty.SendLine(s); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// Disconnect disconnects the PTY.
func (tp *TestPTY) Disconnect() {
	if err := tp.pty.Disconnect(); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// WantEOF expects EOF from the PTY.
func (tp *TestPTY) WantEOF() {
	if err := tp.pty.WantEOF(); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}

// Reject adds a reject string to the PTY.
// If the string is seen, other PTY operations will return an error.
func (tp *TestPTY) Reject(s string) {
	tp.pty.Reject(s)
}

// AttachAndStart attaches the PTY to the given command and starts it.
func (tp *TestPTY) AttachAndStart(cmd *exec.Cmd) {
	if err := tp.pty.AttachAndStart(cmd); err != nil {
		tp.t.Helper()
		tp.t.Fatal(err)
	}
}
