package e3e

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

//go:embed security_probe_prompt.txt
var rawSecurityProbePrompt string

const (
	envEnable          = "EXE_E3E_ENABLE"
	envSSHHost         = "EXE_E3E_SSH_HOST"
	envSSHPort         = "EXE_E3E_SSH_PORT"
	envSSHUser         = "EXE_E3E_SSH_USER"
	envSSHKeyPath      = "EXE_E3E_SSH_KEY_PATH"
	envSSHKnownHosts   = "EXE_E3E_SSH_KNOWN_HOSTS"
	envCodexAPIKey     = "EXE_E3E_OPENAI_API_KEY"
	envAnthropicAPIKey = "EXE_E3E_ANTHROPIC_API_KEY"
)

type config struct {
	replSSH         sshConfig
	prompt          string
	codexAPIKey     string
	anthropicAPIKey string
}

type sshConfig struct {
	host           string
	port           string
	user           string
	privateKeyPath string
	knownHostsPath string
}

type agent string

const (
	agentCodex  agent = "codex"
	agentClaude agent = "claude"
)

type boxInfo struct {
	Name       string `json:"box_name"`
	SSHCommand string `json:"ssh_command"`
	SSHPort    int    `json:"ssh_port"`
	SSHServer  string `json:"ssh_server"`
	SSHUser    string `json:"ssh_user"`
}

type agentResult struct {
	Agent   agent
	Output  string
	Status  string
	Command string
	Err     error
}

const reportHeading = "# SECURITY REPORT"

func TestSecurityProbeAgents(t *testing.T) {
	// Only run when explicitly requested, to avoid running on a plain `go test ./...` locally, or in regular CI runs.
	enabled := os.Getenv(envEnable) != ""
	if !enabled {
		t.Skip("e3e security probe skipped (set EXE_E3E_ENABLE=1 to run)")
	}

	prompt := strings.TrimSpace(rawSecurityProbePrompt)
	if prompt == "" {
		t.Fatal("empty prompt")
	}

	cfg := &config{
		replSSH: sshConfig{
			host:           cmp.Or(os.Getenv(envSSHHost), "exe.dev"),
			port:           cmp.Or(os.Getenv(envSSHPort), "22"),
			user:           cmp.Or(os.Getenv(envSSHUser), "e3e"),
			privateKeyPath: os.Getenv(envSSHKeyPath),
			knownHostsPath: os.Getenv(envSSHKnownHosts),
		},
		prompt:          prompt,
		codexAPIKey:     os.Getenv(envCodexAPIKey),
		anthropicAPIKey: os.Getenv(envAnthropicAPIKey),
	}
	// t.Logf("ssh config: ssh %s %s@%s", strings.Join(cfg.replSSH.buildBaseArgs(), " "), cfg.replSSH.user, cfg.replSSH.host)

	if cfg.codexAPIKey == "" || cfg.anthropicAPIKey == "" {
		t.Fatalf("both %s and %s must be set", envCodexAPIKey, envAnthropicAPIKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	box := createBox(t, ctx, cfg)
	// t.Logf("created box: %v", box)
	t.Cleanup(func() {
		if err := deleteBox(t, context.Background(), cfg, box.Name); err != nil {
			// t.Logf("cleanup: deleting box %s failed: %v", box.Name, err)
		} else {
			// t.Logf("cleanup: deleted box %s", box.Name)
		}
	})

	results := make(chan agentResult, 2)
	var wg sync.WaitGroup
	for _, agent := range []agent{agentCodex, agentClaude} {
		wg.Go(func() { results <- runAgent(t, ctx, cfg, box, agent) })
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.Err != nil {
			t.Errorf("[%s]\ncommand: %s\nerror: %v\nout:\n%s\n", res.Agent, res.Command, res.Err, res.Output)
			continue
		}
		if res.Output != "" {
			t.Errorf("[%s]\nout:\n%s\n", res.Agent, res.Output)
		}
	}
}

func createBox(t *testing.T, ctx context.Context, cfg *config) *boxInfo {
	args := cfg.replSSH.buildBaseArgs()
	args = append(args, cfg.replSSH.target(), "new", "--json")
	t.Logf("creating box: ssh %s", strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh new: %v\n%s", err, out)
	}

	var resp boxInfo
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%q", err, out)
	}
	if resp.Name == "" {
		t.Fatalf("missing box_name in %s", out)
	}
	if resp.SSHCommand == "" {
		t.Fatalf("missing ssh_command in %s", out)
	}
	return &resp
}

func deleteBox(t *testing.T, ctx context.Context, cfg *config, boxName string) error {
	t.Logf("deleting box %s", boxName)
	args := cfg.replSSH.buildBaseArgs()
	args = append(args, cfg.replSSH.target(), "rm", boxName)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh rm: %w\n%s", err, out)
	}
	return nil
}

func runAgent(t *testing.T, ctx context.Context, cfg *config, box *boxInfo, ag agent) agentResult {
	boxSSHArgs := cfg.replSSH.buildBaseArgs()
	boxSSHArgs = append(boxSSHArgs, "-p", fmt.Sprint(box.SSHPort), box.SSHUser+"@"+box.SSHServer)
	script := agentScript(t, ag, cfg.prompt, cfg)
	boxSSHArgs = append(boxSSHArgs, "bash", "-s")
	cmd := exec.CommandContext(ctx, "ssh", boxSSHArgs...)
	// t.Logf("running %v", cmd.String())
	cmd.Stdin = strings.NewReader(script)
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	out = strings.TrimSpace(out)
	idx := strings.LastIndex(out, reportHeading)
	if idx >= 0 {
		out = strings.TrimSpace(out[idx+len(reportHeading):])
	}
	out = strings.TrimSpace(out)
	if strings.HasSuffix(out, "\nOK") {
		out = ""
	}
	return agentResult{
		Agent:   ag,
		Output:  out,
		Command: cmd.String(),
		Err:     err,
	}
}

func agentScript(t *testing.T, ag agent, prompt string, cfg *config) string {
	switch ag {
	case agentCodex:
		openAIKey, err := syntax.Quote(cfg.codexAPIKey, syntax.LangBash)
		if err != nil {
			t.Fatalf("quote codex OPENAI_API_KEY: %v", err)
		}
		codexKey, err := syntax.Quote(cfg.codexAPIKey, syntax.LangBash)
		if err != nil {
			t.Fatalf("quote codex CODEX_API_KEY: %v", err)
		}
		return fmt.Sprintf(`set -euo pipefail
export OPENAI_API_KEY=%s
export CODEX_API_KEY=%s
export PATH="/home/exedev/.local/bin:$PATH"
export PATH="/home/exedev/.local/bin:$PATH"
TMP=$(mktemp)
trap 'rm -f \"$TMP\"' EXIT
codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --output-last-message \"$TMP\" <<'EOF'
%s
EOF
cat \"$TMP\"
`, openAIKey, codexKey, prompt)
	case agentClaude:
		anthropicKey, err := syntax.Quote(cfg.anthropicAPIKey, syntax.LangBash)
		if err != nil {
			t.Fatalf("quote claude ANTHROPIC_API_KEY: %v", err)
		}
		return fmt.Sprintf(`set -euo pipefail
export ANTHROPIC_API_KEY=%s
export PATH="/home/exedev/.local/bin:$PATH"
claude --print --dangerously-skip-permissions <<'EOF'
%s
EOF
`, anthropicKey, prompt)
	}
	t.Fatalf("unsupported agent %q", ag)
	panic("unreachable")
}

func (cfg sshConfig) target() string {
	if cfg.user != "" {
		return fmt.Sprintf("%s@%s", cfg.user, cfg.host)
	}
	return cfg.host
}

func (cfg sshConfig) buildBaseArgs() []string {
	args := []string{"-o", "BatchMode=yes", "-o", "LogLevel=ERROR"}
	if cfg.privateKeyPath != "" {
		args = append(args, "-i", cfg.privateKeyPath)
	}
	if cfg.port != "" {
		args = append(args, "-p", cfg.port)
	}
	if cfg.knownHostsPath != "" {
		args = append(args, "-o", fmt.Sprintf("UserKnownHostsFile=%s", cfg.knownHostsPath))
	} else {
		args = append(args,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
		)
	}
	return args
}
