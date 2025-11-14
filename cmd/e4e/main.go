package main

import (
	"context"
	_ "embed"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	envCodexKey = "E4E_OPENAI_API_KEY"
)

const reportHeading = "# DOCUMENTATION REPORT"

//go:embed prompt.md
var prompt string

func main() {
	log.SetFlags(0)
	apiKey := strings.TrimSpace(os.Getenv(envCodexKey))
	if apiKey == "" {
		log.Fatalf("%s must be set", envCodexKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	raw, err := runCodex(ctx, apiKey)
	if err != nil {
		log.Fatalf("agent: %v\n%s\n", err, raw)
	}
	// Cut at final "# DOCUMENTATION REPORT".
	// (There are usually two, so can't use strings.Cut.)
	// We don't need to see all the exploration and reasoning traces.
	out := strings.TrimSpace(string(raw))
	i := strings.LastIndex(out, reportHeading)
	if i < 0 {
		log.Fatalf("missing report heading\n%s\n", out)
	}
	out = out[i+len(reportHeading):]
	out = strings.TrimSpace(out)
	if strings.HasSuffix(out, "\nOK") {
		return
	}

	log.Fatalf("%s", out)
}

func runCodex(ctx context.Context, apiKey string) ([]byte, error) {
	cmd := exec.CommandContext(ctx,
		"codex", "exec",
		"--sandbox=read-only",
		"--model", "gpt-5.1-codex-mini",
		"--config", "model_reasoning_effort=low",
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+apiKey)
	return cmd.CombinedOutput()
}
