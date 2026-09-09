package systemd_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestServiceContract(t *testing.T) {
	unit, err := parseUnit(readFile(t, "execonnect.service"))
	if err != nil {
		t.Fatal(err)
	}

	assertDirective(t, unit, "Service", "Type", "exec")
	assertDirective(t, unit, "Service", "User", "execonnect")
	assertDirective(t, unit, "Service", "Group", "execonnect")
	assertDirectiveWords(t, unit, "Service", "StateDirectory", []string{"execonnect"})
	assertDirectiveMode(t, unit, "Service", "StateDirectoryMode", 0o700)
	assertDirectiveWords(t, unit, "Service", "RuntimeDirectory", []string{"execonnect"})
	assertDirectiveMode(t, unit, "Service", "RuntimeDirectoryMode", 0o770)
	assertDirectiveWords(t, unit, "Service", "ExecStart", []string{
		"/usr/local/bin/execonnect",
		"--state-dir", "/var/lib/execonnect",
		"--socket", "/run/execonnect/control.sock",
		"serve",
	})
	assertDirective(t, unit, "Service", "Restart", "on-failure")
	assertDirective(t, unit, "Service", "RestartSec", "5")
	assertDirective(t, unit, "Service", "StandardOutput", "journal")
	assertDirective(t, unit, "Service", "StandardError", "journal")
	assertDirectiveBool(t, unit, "Service", "NoNewPrivileges", true)
	assertDirective(t, unit, "Service", "ProtectSystem", "strict")
	assertDirectiveBool(t, unit, "Service", "ProtectHome", true)
	assertDirectiveBool(t, unit, "Service", "PrivateTmp", true)
	assertOptionalDirectiveBool(t, unit, "Service", "DynamicUser", false)
	assertDirectiveWords(t, unit, "Install", "WantedBy", []string{"multi-user.target"})
}

func TestParseUnitRejectsUnsupportedSyntax(t *testing.T) {
	for name, contents := range map[string]string{
		"duplicate singleton override": "[Service]\nUser=execonnect\nUser=root\n",
		"quoted value":                 "[Service]\nExecStart=\"/usr/local/bin/execonnect\" serve\n",
		"continued value":              "[Service]\nExecStart=/usr/local/bin/execonnect \\\n serve\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseUnit(contents); err == nil {
				t.Fatal("parseUnit accepted unsupported syntax")
			}
		})
	}
}

func TestServiceUnitSyntax(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd-analyze is only available on Linux")
	}
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is unavailable")
	}

	unit := readFile(t, "execonnect.service")
	verifiableUnit := strings.Replace(unit, "/usr/local/bin/execonnect", "/bin/true", 1)
	if verifiableUnit == unit {
		t.Fatal("service does not execute the installed execonnect binary")
	}
	unitPath := filepath.Join(t.TempDir(), "execonnect.service")
	if err := os.WriteFile(unitPath, []byte(verifiableUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(analyze, "verify", unitPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, output)
	}
}

func TestInstallScriptProvisionsAccountsAndArtifactsIdempotently(t *testing.T) {
	installer := installerPath(t)
	info, err := os.Stat(installer)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("install.sh is not executable")
	}

	sandbox := newInstallerSandbox(t)
	binaryPath := filepath.Join(t.TempDir(), "execonnect")
	writeExecutable(t, binaryPath, "#!/bin/sh\n# first build\n")
	if output, err := sandbox.run(installer, binaryPath); err != nil {
		t.Fatalf("first install.sh: %v\n%s", err, output)
	}

	replacement := "#!/bin/sh\n# replacement build\n"
	writeExecutable(t, binaryPath, replacement)
	sandbox.removeCommand(t, "groupadd")
	sandbox.removeCommand(t, "useradd")
	if output, err := sandbox.run(installer, binaryPath); err != nil {
		t.Fatalf("second install.sh: %v\n%s", err, output)
	}

	calls := sandbox.calls(t)
	assertCallCount(t, calls, []string{"groupadd", "--system", "execonnect"}, 1)
	assertCallCount(t, calls, []string{
		"useradd", "--system", "--gid", "execonnect", "--home-dir", "/var/lib/execonnect",
		"--no-create-home", "--shell", "/usr/sbin/nologin", "execonnect",
	}, 1)
	assertCallCount(t, calls, []string{
		"install", "-m", "0755", binaryPath, "/usr/local/bin/execonnect",
	}, 2)
	unitPath, err := filepath.Abs("execonnect.service")
	if err != nil {
		t.Fatal(err)
	}
	assertCallCount(t, calls, []string{
		"install", "-m", "0644", unitPath, "/etc/systemd/system/execonnect.service",
	}, 2)
	assertCallCount(t, calls, []string{"systemctl", "daemon-reload"}, 2)
	for _, call := range calls {
		if call[0] == "systemctl" && !slices.Equal(call, []string{"systemctl", "daemon-reload"}) {
			t.Fatalf("unexpected systemctl call: %q", call)
		}
	}

	assertFile(t, filepath.Join(sandbox.root, "usr/local/bin/execonnect"), replacement, 0o755)
	assertFile(t, filepath.Join(sandbox.root, "etc/systemd/system/execonnect.service"), readFile(t, unitPath), 0o644)
}

func TestInstallScriptMissingUnitDoesNotMutate(t *testing.T) {
	sandbox := newInstallerSandbox(t)
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeExecutable(t, installer, readFile(t, installerPath(t)))

	output, err := sandbox.run(installer, writeInstallerInput(t))
	if code := exitCode(err); code != 1 {
		t.Fatalf("install.sh exit code = %d, want 1\noutput:\n%s", code, output)
	}
	if !strings.Contains(output, "systemd unit must be a readable file") {
		t.Fatalf("install.sh output = %q", output)
	}
	assertNoMutatingCalls(t, sandbox.calls(t))
	assertPathAbsent(t, filepath.Join(sandbox.root, "usr/local/bin/execonnect"))
	assertPathAbsent(t, filepath.Join(sandbox.root, "etc/systemd/system/execonnect.service"))
}

func TestInstallScriptMissingCommandsDoNotMutate(t *testing.T) {
	installer := installerPath(t)
	for _, command := range []string{"id", "getent", "install", "systemctl", "groupadd", "useradd"} {
		t.Run(command, func(t *testing.T) {
			sandbox := newInstallerSandbox(t)
			sandbox.removeCommand(t, command)
			output, err := sandbox.run(installer, writeInstallerInput(t))
			if code := exitCode(err); code != 1 {
				t.Fatalf("install.sh exit code = %d, want 1\noutput:\n%s", code, output)
			}
			if !strings.Contains(output, "required command not found: "+command) {
				t.Fatalf("install.sh output = %q", output)
			}
			assertNoMutatingCalls(t, sandbox.calls(t))
			assertPathAbsent(t, filepath.Join(sandbox.root, "usr/local/bin/execonnect"))
			assertPathAbsent(t, filepath.Join(sandbox.root, "etc/systemd/system/execonnect.service"))
		})
	}
}

func TestInstallScriptFailuresDoNotMutate(t *testing.T) {
	installer := installerPath(t)
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, sandbox *installerSandbox) []string
		code    int
	}{
		{
			name: "wrong arity",
			prepare: func(t *testing.T, sandbox *installerSandbox) []string {
				return nil
			},
			code: 2,
		},
		{
			name: "non-executable input",
			prepare: func(t *testing.T, sandbox *installerSandbox) []string {
				binary := filepath.Join(t.TempDir(), "execonnect")
				if err := os.WriteFile(binary, []byte("not executable"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{binary}
			},
			code: 1,
		},
		{
			name: "non-root caller",
			prepare: func(t *testing.T, sandbox *installerSandbox) []string {
				sandbox.uid = "1000"
				return []string{writeInstallerInput(t)}
			},
			code: 1,
		},
		{
			name: "existing user has wrong primary group",
			prepare: func(t *testing.T, sandbox *installerSandbox) []string {
				sandbox.markAccountExists(t)
				sandbox.primaryGroup = "operators"
				return []string{writeInstallerInput(t)}
			},
			code: 1,
		},
		{
			name: "existing user with missing group",
			prepare: func(t *testing.T, sandbox *installerSandbox) []string {
				sandbox.markUserExists(t)
				sandbox.primaryGroup = "operators"
				return []string{writeInstallerInput(t)}
			},
			code: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sandbox := newInstallerSandbox(t)
			output, err := sandbox.run(installer, test.prepare(t, sandbox)...)
			if code := exitCode(err); code != test.code {
				t.Fatalf("install.sh exit code = %d, want %d\noutput:\n%s", code, test.code, output)
			}
			assertNoMutatingCalls(t, sandbox.calls(t))
			assertPathAbsent(t, filepath.Join(sandbox.root, "usr/local/bin/execonnect"))
			assertPathAbsent(t, filepath.Join(sandbox.root, "etc/systemd/system/execonnect.service"))
		})
	}
}

type parsedUnit map[string]map[string]string

// parseUnit supports only the checked-in unit's unquoted, single-line subset;
// rejecting quotes and continuations avoids silently misparsing broader systemd syntax.
func parseUnit(contents string) (parsedUnit, error) {
	unit := make(parsedUnit)
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			return nil, fmt.Errorf("line %d: continuations are unsupported", lineNumber)
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 || strings.Count(line, "]") != 1 {
				return nil, fmt.Errorf("line %d: malformed section", lineNumber)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("line %d: empty section", lineNumber)
			}
			if unit[section] == nil {
				unit[section] = make(map[string]string)
			}
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: directive outside a section", lineNumber)
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("line %d: malformed directive", lineNumber)
		}
		if strings.ContainsAny(value, "\\\"'") {
			return nil, fmt.Errorf("line %d: quoting and escaping are unsupported", lineNumber)
		}
		if _, exists := unit[section][key]; exists {
			return nil, fmt.Errorf("line %d: duplicate [%s] %s directive", lineNumber, section, key)
		}
		unit[section][key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return unit, nil
}

func assertDirective(t *testing.T, unit parsedUnit, section, key, want string) {
	t.Helper()
	if got := requiredDirective(t, unit, section, key); got != want {
		t.Fatalf("[%s] %s = %q, want %q", section, key, got, want)
	}
}

func assertDirectiveWords(t *testing.T, unit parsedUnit, section, key string, want []string) {
	t.Helper()
	got, err := simpleWords(requiredDirective(t, unit, section, key))
	if err != nil {
		t.Fatalf("[%s] %s: %v", section, key, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("[%s] %s argv = %q, want %q", section, key, got, want)
	}
}

func assertDirectiveMode(t *testing.T, unit parsedUnit, section, key string, want os.FileMode) {
	t.Helper()
	value := requiredDirective(t, unit, section, key)
	mode, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		t.Fatalf("[%s] %s = %q: %v", section, key, value, err)
	}
	if got := os.FileMode(mode); got != want {
		t.Fatalf("[%s] %s = %#o, want %#o", section, key, got, want)
	}
}

func assertDirectiveBool(t *testing.T, unit parsedUnit, section, key string, want bool) {
	t.Helper()
	value := requiredDirective(t, unit, section, key)
	got, err := parseSystemdBool(value)
	if err != nil {
		t.Fatalf("[%s] %s = %q: %v", section, key, value, err)
	}
	if got != want {
		t.Fatalf("[%s] %s = %t, want %t", section, key, got, want)
	}
}

func assertOptionalDirectiveBool(t *testing.T, unit parsedUnit, section, key string, want bool) {
	t.Helper()
	value, ok := unit[section][key]
	if !ok {
		if want {
			t.Fatalf("[%s] %s is unset, want true", section, key)
		}
		return
	}
	got, err := parseSystemdBool(value)
	if err != nil {
		t.Fatalf("[%s] %s = %q: %v", section, key, value, err)
	}
	if got != want {
		t.Fatalf("[%s] %s = %t, want %t", section, key, got, want)
	}
}

func requiredDirective(t *testing.T, unit parsedUnit, section, key string) string {
	t.Helper()
	value, ok := unit[section][key]
	if !ok {
		t.Fatalf("[%s] %s is unset", section, key)
	}
	return value
}

func parseSystemdBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "1", "yes", "true", "on":
		return true, nil
	case "0", "no", "false", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid systemd boolean")
	}
}

func simpleWords(value string) ([]string, error) {
	if strings.ContainsAny(value, "\\\"'") {
		return nil, fmt.Errorf("quoting and escaping are unsupported")
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil, fmt.Errorf("empty value")
	}
	return words, nil
}

type installerSandbox struct {
	root         string
	binDir       string
	stateDir     string
	logPath      string
	uid          string
	primaryGroup string
}

func newInstallerSandbox(t *testing.T) *installerSandbox {
	t.Helper()
	tempDir := t.TempDir()
	sandbox := &installerSandbox{
		root:         filepath.Join(tempDir, "root"),
		binDir:       filepath.Join(tempDir, "bin"),
		stateDir:     filepath.Join(tempDir, "state"),
		logPath:      filepath.Join(tempDir, "calls"),
		uid:          "0",
		primaryGroup: "execonnect",
	}
	for _, directory := range []string{sandbox.root, sandbox.binDir, sandbox.stateDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	dispatcher := filepath.Join(sandbox.binDir, "fake-command")
	writeExecutable(t, dispatcher, fakeCommand)
	for _, name := range []string{"id", "getent", "groupadd", "useradd", "install", "systemctl"} {
		if err := os.Symlink(dispatcher, filepath.Join(sandbox.binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return sandbox
}

const fakeCommand = `#!/bin/sh
set -eu

command=${0##*/}
record() {
	printf '%s\000' "$command" >> "$FAKE_LOG"
	for argument in "$@"; do printf '%s\000' "$argument" >> "$FAKE_LOG"; done
	printf '\000' >> "$FAKE_LOG"
}

case "$command" in
	id)
		case "$*" in
			-u) echo "$FAKE_UID" ;;
			"-gn execonnect") echo "$FAKE_PRIMARY_GROUP" ;;
			*) exit 1 ;;
		esac
		;;
	getent)
		[ "$#" -eq 2 ] && [ "$2" = execonnect ] && test -f "$FAKE_STATE/$1"
		;;
	groupadd)
		record "$@"
		: > "$FAKE_STATE/group"
		;;
	useradd)
		record "$@"
		: > "$FAKE_STATE/passwd"
		;;
	install)
		record "$@"
		[ "$#" -eq 4 ] && [ "$1" = -m ]
		mode=$2
		source=$3
		target=$FAKE_ROOT$4
		/bin/mkdir -p "${target%/*}"
		/bin/cp "$source" "$target"
		/bin/chmod "$mode" "$target"
		;;
	systemctl)
		record "$@"
		;;
	*) exit 1 ;;
esac
`

func (sandbox *installerSandbox) run(installer string, arguments ...string) (string, error) {
	command := exec.Command(installer, arguments...)
	command.Env = []string{
		"PATH=" + sandbox.binDir,
		"FAKE_ROOT=" + sandbox.root,
		"FAKE_STATE=" + sandbox.stateDir,
		"FAKE_LOG=" + sandbox.logPath,
		"FAKE_UID=" + sandbox.uid,
		"FAKE_PRIMARY_GROUP=" + sandbox.primaryGroup,
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func (sandbox *installerSandbox) calls(t *testing.T) [][]string {
	t.Helper()
	contents, err := os.ReadFile(sandbox.logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	for _, record := range bytes.Split(contents, []byte{0, 0}) {
		if len(record) == 0 {
			continue
		}
		var call []string
		for _, field := range bytes.Split(record, []byte{0}) {
			call = append(call, string(field))
		}
		calls = append(calls, call)
	}
	return calls
}

func (sandbox *installerSandbox) markAccountExists(t *testing.T) {
	t.Helper()
	sandbox.markGroupExists(t)
	sandbox.markUserExists(t)
}

func (sandbox *installerSandbox) markGroupExists(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sandbox.stateDir, "group"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (sandbox *installerSandbox) markUserExists(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sandbox.stateDir, "passwd"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (sandbox *installerSandbox) removeCommand(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(sandbox.binDir, name)); err != nil {
		t.Fatal(err)
	}
}

func installerPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeInstallerInput(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "execonnect")
	writeExecutable(t, path, "#!/bin/sh\n# test input\n")
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if got := readFile(t, path); got != contents {
		t.Fatalf("%s contents:\n got: %q\nwant: %q", path, got, contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("%s mode = %#o, want %#o", path, got, mode)
	}
}

func assertCallCount(t *testing.T, calls [][]string, wantCall []string, want int) {
	t.Helper()
	count := 0
	for _, call := range calls {
		if slices.Equal(call, wantCall) {
			count++
		}
	}
	if count != want {
		t.Fatalf("call %q count = %d, want %d\ncalls: %q", wantCall, count, want, calls)
	}
}

func assertNoMutatingCalls(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		switch call[0] {
		case "groupadd", "useradd", "install", "systemctl":
			t.Fatalf("installer mutated state before rejecting input: %q", call)
		}
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v, want not exist", path, err)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}
