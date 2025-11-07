package e4e

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	envEnable   = "EXE_E4E_ENABLE"
	envCodexKey = "EXE_E4E_OPENAI_API_KEY"
)

//go:embed prompt.md
var prompt string

func TestDocumentationReviewAgents(t *testing.T) {
	if os.Getenv(envEnable) == "" {
		t.Skip("e4e documentation sweep skipped (set EXE_E4E_ENABLE=1 to run)")
	}

	apiKey := strings.TrimSpace(os.Getenv(envCodexKey))
	if apiKey == "" {
		t.Fatalf("%s must be set", envCodexKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	raw, err := runCodex(ctx, apiKey)
	if err != nil {
		t.Fatalf("agent: %v\n%s\n", err, raw)
	}
	// Cut at final "# DOCUMENTATION REPORT".
	// (There are usually two, so can't use strings.Cut.)
	// We don't need to see all the exploration and reasoning traces.
	const reportHeading = "# DOCUMENTATION REPORT"
	out := strings.TrimSpace(string(raw))
	i := strings.LastIndex(out, reportHeading)
	if i < 0 {
		t.Fatalf("missing report heading\n%s\n", out)
	}
	out = out[i+len(reportHeading):]
	out = strings.TrimSpace(out)
	if strings.HasSuffix(out, "\nOK") {
		t.Logf("OK")
		return
	}

	t.Error(out)
}

func runCodex(ctx context.Context, apiKey string) ([]byte, error) {
	cmd := exec.CommandContext(ctx,
		"codex", "exec",
		"--sandbox=read-only",
		"--model", "gpt-5-codex-mini",
		"--config", "model_reasoning_effort=low",
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+apiKey)
	cmd.Dir = ".."
	return cmd.CombinedOutput()
}
