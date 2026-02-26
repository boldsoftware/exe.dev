package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	code, err := hookMain(os.Stdin, os.Stderr, "http://127.0.0.1:7723/run", "/data/e1ed/runs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// ref represents a single line from git post-receive stdin.
type ref struct {
	OldSHA string
	NewSHA string
	Name   string
}

// parseRefs reads post-receive stdin and returns matching e1e refs.
func parseRefs(r io.Reader) ([]ref, error) {
	var matches []ref
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		re := ref{OldSHA: parts[0], NewSHA: parts[1], Name: parts[2]}
		if !strings.HasPrefix(re.Name, "refs/e1e/") {
			continue
		}
		// Skip deletions (new SHA is all zeros).
		if strings.Trim(re.NewSHA, "0") == "" {
			continue
		}
		matches = append(matches, re)
	}
	return matches, scanner.Err()
}

// goTestEvent is the subset of go test -json output we need.
type goTestEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

// e1edMsg matches the e1ed server's message format.
type e1edMsg struct {
	E1ED     bool `json:"e1ed"`
	ExitCode *int `json:"exit_code,omitempty"`
}

// result holds parsed NDJSON output.
type result struct {
	ExitCode    int
	FailedTests []string
}

// parseResults reads NDJSON lines and extracts test results.
func parseResults(r io.Reader) result {
	var res result
	res.ExitCode = -1 // default if no done message
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()

		// Try as e1ed message first.
		var msg e1edMsg
		if json.Unmarshal(line, &msg) == nil && msg.E1ED && msg.ExitCode != nil {
			res.ExitCode = *msg.ExitCode
			continue
		}

		// Try as go test event.
		var ev goTestEvent
		if json.Unmarshal(line, &ev) == nil && ev.Action == "fail" && ev.Test != "" {
			res.FailedTests = append(res.FailedTests, ev.Test)
		}
	}
	return res
}

// formatSummary returns the two-line summary printed to the user.
func formatSummary(res result, outputPath string) string {
	scpLine := fmt.Sprintf("scp root@edric:%s .", outputPath)
	if len(res.FailedTests) > 0 {
		return fmt.Sprintf("FAIL: %s\n%s", strings.Join(res.FailedTests, ", "), scpLine)
	}
	if res.ExitCode != 0 {
		return fmt.Sprintf("FAIL (exit code %d)\n%s", res.ExitCode, scpLine)
	}
	return fmt.Sprintf("All tests passed.\n%s", scpLine)
}

func hookMain(stdin io.Reader, stderr io.Writer, e1edURL, runsDir string) (int, error) {
	refs, err := parseRefs(stdin)
	if err != nil {
		return 1, fmt.Errorf("reading refs: %w", err)
	}
	if len(refs) == 0 {
		return 0, nil
	}
	if len(refs) > 1 {
		return 1, fmt.Errorf("push one ref at a time, got %d", len(refs))
	}

	sha := refs[0].NewSHA

	// POST to e1ed.
	body := fmt.Sprintf(`{"commit":"%s"}`, sha)
	resp, err := http.Post(e1edURL, "application/json", strings.NewReader(body))
	if err != nil {
		return 1, fmt.Errorf("POST /run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return 1, fmt.Errorf("e1ed returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	// Write response to file while collecting it for parsing.
	sha8 := sha
	if len(sha8) > 8 {
		sha8 = sha8[:8]
	}
	outputPath := filepath.Join(runsDir, fmt.Sprintf("%s-%d.jsonl", sha8, time.Now().Unix()))

	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return 1, fmt.Errorf("create runs dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return 1, fmt.Errorf("create output file: %w", err)
	}

	// Tee response to file and collect for parsing.
	tee := io.TeeReader(resp.Body, f)
	res := parseResults(tee)
	f.Close()

	fmt.Fprint(stderr, formatSummary(res, outputPath))
	fmt.Fprint(stderr, "\n")

	return res.ExitCode, nil
}
