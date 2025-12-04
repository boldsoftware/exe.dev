// This file runs an AI agent (sketch) to test exe.dev functionality.
// The e1e framework sets up exed, exelet, and sshpiper, then the agent
// runs test prompts from e2e/*.txt against the running services.
// The test is skipped by default; set RUN_AI_AGENT_TEST=1 to enable.

package e1e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAIAgent runs an AI agent (sketch) to test exe.dev functionality.
// The test is skipped unless RUN_AI_AGENT_TEST=1 is set.
func TestAIAgent(t *testing.T) {
	if os.Getenv("RUN_AI_AGENT_TEST") != "1" {
		t.Skip("Skipping AI agent test (set RUN_AI_AGENT_TEST=1 to enable)")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("ANTHROPIC_API_KEY not set")
	}

	e1eTestsOnlyRunOnce(t)
	noGolden(t) // Agent output is non-deterministic

	// Verify sketch is in PATH
	sketchPath, err := exec.LookPath("sketch")
	if err != nil {
		t.Fatal("sketch binary not found in PATH")
	}

	// e2e/ is sibling to e1e/ (go test runs in package directory)
	e2eDir, err := filepath.Abs("../e2e")
	if err != nil {
		t.Fatalf("failed to resolve e2e directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e2eDir, "main-test-prompt.txt")); err != nil {
		t.Fatalf("e2e/main-test-prompt.txt not found: %v", err)
	}

	// Create report directory
	reportDir := filepath.Join(e2eDir, "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatalf("failed to create report directory: %v", err)
	}

	// Write environment info for the agent
	envInfoPath := filepath.Join(e2eDir, "env-info.txt")
	envInfo := fmt.Sprintf(`# E1E Test Environment

The e1e test framework has set up the following services:

- SSH proxy (sshpiper): localhost:%d
- HTTP server (exed): http://localhost:%d
- Direct SSH (exed): localhost:%d

To connect via SSH:
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d localhost

To access the web interface:
  http://localhost:%d
`,
		Env.sshPort(),
		Env.exed.HTTPPort,
		Env.exed.SSHPort,
		Env.sshPort(),
		Env.exed.HTTPPort,
	)
	if err := os.WriteFile(envInfoPath, []byte(envInfo), 0o644); err != nil {
		t.Fatalf("failed to write env-info.txt: %v", err)
	}
	defer os.Remove(envInfoPath)

	// Build the test prompt
	testPrompt := fmt.Sprintf(`Read env-info.txt for service ports, then read main-test-prompt.txt and follow its instructions.

SSH port: %d
HTTP port: %d
`, Env.sshPort(), Env.exed.HTTPPort)

	ctx := Env.context(t)

	t.Logf("Running AI agent")
	t.Logf("SSH port: %d, HTTP port: %d", Env.sshPort(), Env.exed.HTTPPort)

	cmd := exec.CommandContext(ctx, sketchPath,
		"-one-shot",
		"-skaband-addr=",
		"-unsafe",
		"-prompt", testPrompt,
	)
	cmd.Dir = e2eDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ANTHROPIC_API_KEY=%s", os.Getenv("ANTHROPIC_API_KEY")),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to get stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sketch: %v", err)
	}

	// Stream output to test log
	go func() {
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			t.Log(scanner.Text())
		}
	}()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("AI agent cancelled: %v", ctx.Err())
		}
		t.Logf("AI agent exited with error: %v (this may be expected if tests failed)", err)
	}

	// Check for result file
	resultPath := filepath.Join(e2eDir, "result.txt")
	resultBytes, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("No result.txt file found - agent may not have completed properly: %v", err)
	}

	result := strings.TrimSpace(string(resultBytes))
	t.Logf("AI agent result: %s", result)

	// Check if report was generated
	reportPath := filepath.Join(reportDir, "report.md")
	if _, err := os.Stat(reportPath); err == nil {
		reportBytes, _ := os.ReadFile(reportPath)
		t.Logf("Report generated (%d bytes)", len(reportBytes))
	}

	if result != "SUCCESS" {
		t.Fatalf("AI agent test failed with result: %s", result)
	}
}
