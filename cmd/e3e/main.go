package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed security_probe_prompt.txt
var prompt string

const (
	envSSHHost       = "EXE_E3E_SSH_HOST"
	envSSHPort       = "EXE_E3E_SSH_PORT"
	envSSHUser       = "EXE_E3E_SSH_USER"
	envSSHKeyPath    = "EXE_E3E_SSH_KEY_PATH"
	envSSHKnownHosts = "EXE_E3E_SSH_KNOWN_HOSTS"
)

type config struct {
	replSSH sshConfig
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
	Name       string `json:"vm_name"`
	SSHCommand string `json:"ssh_command"`
	SSHPort    int    `json:"ssh_port"`
	SSHDest    string `json:"ssh_dest"`
}

type agentResult struct {
	Agent   agent
	Output  string
	Status  string
	Command string
	Err     error
}

const reportHeading = "# SECURITY REPORT"

func main() {
	log.SetFlags(0)

	cfg := &config{
		replSSH: sshConfig{
			host:           cmp.Or(os.Getenv(envSSHHost), "exe.dev"),
			port:           cmp.Or(os.Getenv(envSSHPort), "22"),
			user:           cmp.Or(os.Getenv(envSSHUser), "e3e"),
			privateKeyPath: os.Getenv(envSSHKeyPath),
			knownHostsPath: os.Getenv(envSSHKnownHosts),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	box, err := createBox(ctx, cfg)
	if err != nil {
		log.Fatalf("create box: %v", err)
	}

	// TODO(https://github.com/boldsoftware/exe.dev/issues/32): remove once DNS propagation is fixed.
	log.Printf("waiting 1 minute for DNS propagation...")
	time.Sleep(time.Minute)

	if err := uploadSourceArchive(ctx, cfg, box); err != nil {
		log.Fatalf("upload source archive: %v", err)
	}

	// Run agents in serial.
	// Stop on first failure.
	rc := 0
	for _, agent := range []agent{agentCodex, agentClaude} {
		res := runAgent(ctx, cfg, box, agent)
		if res.Output != "" {
			log.Printf("## %s\n%s\n", res.Agent, res.Output)
			rc = 1
			break
		}
	}

	deleteBox(context.WithoutCancel(ctx), cfg, box.Name)
	os.Exit(rc)
}

func createBox(ctx context.Context, cfg *config) (*boxInfo, error) {
	args := cfg.replSSH.buildBaseArgs()
	args = append(args, cfg.replSSH.target(), "new", "--json", "--no-email")
	// log.Printf("creating box: ssh %s", strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ssh new: %w\n%s", err, out)
	}

	var resp boxInfo
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w\n%s", err, out)
	}
	if resp.Name == "" {
		return nil, fmt.Errorf("missing vm_name in %s", out)
	}
	if resp.SSHCommand == "" {
		return nil, fmt.Errorf("missing ssh_command in %s", out)
	}
	return &resp, nil
}

func deleteBox(ctx context.Context, cfg *config, boxName string) {
	args := cfg.replSSH.buildBaseArgs()
	args = append(args, cfg.replSSH.target(), "rm", boxName)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		log.Printf("deleteBox: ssh rm: %v\n%s", err, out)
	}
}

const remoteSourceDir = "/tmp/exe-source"

func uploadSourceArchive(ctx context.Context, cfg *config, box *boxInfo) error {
	// Create a temporary file for the archive.
	f, err := os.CreateTemp("", "exe-source-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	localPath := f.Name()
	f.Close()
	defer os.Remove(localPath)

	// Create git archive of the worktree.
	archiveCmd := exec.CommandContext(ctx, "git", "archive", "--format=tar.gz", "-o", localPath, "HEAD")
	if out, err := archiveCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git archive: %w\n%s", err, out)
	}

	// SCP the archive to the box (scp uses -P for port, not -p).
	remoteTar := remoteSourceDir + ".tar.gz"
	scpArgs := cfg.replSSH.buildCommonArgs()
	scpArgs = append(scpArgs, "-P", fmt.Sprint(box.SSHPort), localPath, box.SSHDest+":"+remoteTar)
	if out, err := exec.CommandContext(ctx, "scp", scpArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("scp: %w\n%s", err, out)
	}

	// Extract the archive on the box.
	sshArgs := cfg.replSSH.buildCommonArgs()
	sshArgs = append(sshArgs, "-p", fmt.Sprint(box.SSHPort), box.SSHDest,
		fmt.Sprintf("mkdir -p %s && tar -xzf %s -C %s && rm %s", remoteSourceDir, remoteTar, remoteSourceDir, remoteTar))
	if out, err := exec.CommandContext(ctx, "ssh", sshArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("extract archive: %w\n%s", err, out)
	}

	return nil
}

func runAgent(ctx context.Context, cfg *config, box *boxInfo, ag agent) agentResult {
	result := agentResult{Agent: ag}

	boxSSHArgs := cfg.replSSH.buildBaseArgs()
	boxSSHArgs = append(boxSSHArgs, "-p", fmt.Sprint(box.SSHPort), box.SSHDest)

	script := agentScript(ag)

	boxSSHArgs = append(boxSSHArgs, "bash", "-s")
	cmd := exec.CommandContext(ctx, "ssh", boxSSHArgs...)
	cmd.Stdin = strings.NewReader(script)

	result.Command = cmd.String()

	raw, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(raw))
	idx := strings.LastIndex(out, reportHeading)
	if idx >= 0 {
		out = strings.TrimSpace(out[idx+len(reportHeading):])
	}
	out = strings.TrimSpace(out)
	firstLine, _, _ := strings.Cut(out, "\n")
	if firstLine == "OK" {
		out = ""
	}

	result.Output = out
	result.Err = err
	return result
}

const llmGateway = "http://169.254.169.254/gateway/llm"

func agentScript(ag agent) string {
	switch ag {
	case agentCodex:
		return fmt.Sprintf(`set -euo pipefail
export OPENAI_API_KEY=implicit
export CODEX_API_KEY=implicit
export OPENAI_BASE_URL=%s/openai/v1
export PATH="/home/exedev/.local/bin:$PATH"
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
codex exec --model gpt-5.1-codex-max --config model_reasoning_effort=xhigh --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --output-last-message "$TMP" <<'EOF'
%s
EOF
cat "$TMP"
`, llmGateway, prompt)
	case agentClaude:
		return fmt.Sprintf(`set -euo pipefail
export ANTHROPIC_API_KEY=implicit
export ANTHROPIC_BASE_URL=%s/anthropic
export PATH="/home/exedev/.local/bin:$PATH"
claude --model opus --print --dangerously-skip-permissions <<'EOF'
%s
EOF
`, llmGateway, prompt)
	default:
		panic(fmt.Sprintf("unsupported agent %q", ag))
	}
}

func (cfg sshConfig) target() string {
	if cfg.user != "" {
		return fmt.Sprintf("%s@%s", cfg.user, cfg.host)
	}
	return cfg.host
}

func (cfg sshConfig) buildBaseArgs() []string {
	args := cfg.buildCommonArgs()
	if cfg.port != "" {
		args = append(args, "-p", cfg.port)
	}
	return args
}

func (cfg sshConfig) buildCommonArgs() []string {
	args := []string{"-o", "BatchMode=yes", "-o", "LogLevel=ERROR"}
	if cfg.privateKeyPath != "" {
		args = append(args, "-i", cfg.privateKeyPath)
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
