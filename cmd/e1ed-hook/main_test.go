package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "single e1e ref",
			input: "0000000000000000000000000000000000000000 abc123def456 refs/e1e/test\n",
			want:  1,
		},
		{
			name:  "no matching refs",
			input: "0000000000000000000000000000000000000000 abc123def456 refs/heads/main\n",
			want:  0,
		},
		{
			name: "multiple e1e refs",
			input: "0000 abc1 refs/e1e/test\n" +
				"0000 def2 refs/e1e/other\n",
			want: 2,
		},
		{
			name:  "deletion skipped",
			input: "abc123def456 0000000000000000000000000000000000000000 refs/e1e/test\n",
			want:  0,
		},
		{
			name: "mixed refs",
			input: "0000 abc1 refs/heads/main\n" +
				"0000 def2 refs/e1e/test\n" +
				"0000 ghi3 refs/tags/v1.0\n",
			want: 1,
		},
		{
			name:  "empty input",
			input: "",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs, err := parseRefs(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("parseRefs() error = %v", err)
			}
			if len(refs) != tt.want {
				t.Fatalf("parseRefs() got %d refs, want %d", len(refs), tt.want)
			}
		})
	}
}

func TestParseRefValues(t *testing.T) {
	input := "oldsha newsha refs/e1e/test\n"
	refs, err := parseRefs(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	if refs[0].OldSHA != "oldsha" || refs[0].NewSHA != "newsha" || refs[0].Name != "refs/e1e/test" {
		t.Fatalf("unexpected ref: %+v", refs[0])
	}
}

func TestParseResults(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantExit   int
		wantFailed []string
	}{
		{
			name: "all pass",
			input: `{"Action":"pass","Test":"TestA","Package":"./e1e"}
{"Action":"pass","Test":"TestB","Package":"./e1e"}
{"e1ed":true,"phase":"done","exit_code":0,"duration":"1m0s"}
`,
			wantExit:   0,
			wantFailed: nil,
		},
		{
			name: "some fail",
			input: `{"Action":"pass","Test":"TestA","Package":"./e1e"}
{"Action":"fail","Test":"TestB","Package":"./e1e"}
{"Action":"fail","Test":"TestC","Package":"./e1e"}
{"e1ed":true,"phase":"done","exit_code":1,"duration":"2m0s"}
`,
			wantExit:   1,
			wantFailed: []string{"TestB", "TestC"},
		},
		{
			name: "package fail without test name ignored",
			input: `{"Action":"fail","Package":"./e1e"}
{"e1ed":true,"phase":"done","exit_code":1,"duration":"1m0s"}
`,
			wantExit:   1,
			wantFailed: nil,
		},
		{
			name: "no done message",
			input: `{"Action":"pass","Test":"TestA","Package":"./e1e"}
`,
			wantExit:   -1,
			wantFailed: nil,
		},
		{
			name: "e1ed error message",
			input: `{"e1ed":true,"phase":"error","msg":"commit not found"}
{"e1ed":true,"phase":"done","exit_code":1,"duration":"0s"}
`,
			wantExit:   1,
			wantFailed: nil,
		},
		{
			name: "interleaved e1ed and test output",
			input: `{"e1ed":true,"phase":"setup","msg":"waiting for environment..."}
{"e1ed":true,"phase":"test","msg":"starting go test"}
{"Action":"run","Test":"TestA","Package":"./e1e"}
{"Action":"pass","Test":"TestA","Package":"./e1e"}
{"Action":"run","Test":"TestB","Package":"./e1e"}
{"Action":"fail","Test":"TestB","Package":"./e1e"}
{"e1ed":true,"phase":"done","exit_code":1,"duration":"3m0s"}
`,
			wantExit:   1,
			wantFailed: []string{"TestB"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parseResults(strings.NewReader(tt.input))
			if res.ExitCode != tt.wantExit {
				t.Errorf("ExitCode = %d, want %d", res.ExitCode, tt.wantExit)
			}
			if len(res.FailedTests) != len(tt.wantFailed) {
				t.Fatalf("FailedTests = %v, want %v", res.FailedTests, tt.wantFailed)
			}
			for i := range tt.wantFailed {
				if res.FailedTests[i] != tt.wantFailed[i] {
					t.Errorf("FailedTests[%d] = %q, want %q", i, res.FailedTests[i], tt.wantFailed[i])
				}
			}
		})
	}
}

func TestFormatSummary(t *testing.T) {
	tests := []struct {
		name string
		res  result
		path string
		want string
	}{
		{
			name: "all pass",
			res:  result{ExitCode: 0},
			path: "/data/e1ed/runs/abc12345-1740000000.jsonl",
			want: "All tests passed.\nscp root@edric:/data/e1ed/runs/abc12345-1740000000.jsonl .",
		},
		{
			name: "failures",
			res:  result{ExitCode: 1, FailedTests: []string{"TestFoo", "TestBar"}},
			path: "/data/e1ed/runs/abc12345-1740000000.jsonl",
			want: "FAIL: TestFoo, TestBar\nscp root@edric:/data/e1ed/runs/abc12345-1740000000.jsonl .",
		},
		{
			name: "nonzero exit no failed tests",
			res:  result{ExitCode: 2},
			path: "/data/e1ed/runs/abc12345-1740000000.jsonl",
			want: "FAIL (exit code 2)\nscp root@edric:/data/e1ed/runs/abc12345-1740000000.jsonl .",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSummary(tt.res, tt.path)
			if got != tt.want {
				t.Errorf("formatSummary() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func buildNDJSON(lines ...any) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		data, _ := json.Marshal(l)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func TestIntegrationAllPass(t *testing.T) {
	ndjson := buildNDJSON(
		map[string]any{"e1ed": true, "phase": "setup", "msg": "created worktree"},
		map[string]any{"e1ed": true, "phase": "test", "msg": "starting go test"},
		map[string]any{"Action": "run", "Test": "TestA", "Package": "./e1e"},
		map[string]any{"Action": "pass", "Test": "TestA", "Package": "./e1e"},
		map[string]any{"e1ed": true, "phase": "done", "exit_code": 0, "duration": "1m0s"},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Commit string `json:"commit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.Commit != "abc123def456" {
			http.Error(w, fmt.Sprintf("unexpected commit: %s", req.Commit), 400)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write(ndjson)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	stdin := strings.NewReader("0000000000000000000000000000000000000000 abc123def456 refs/e1e/test\n")
	var stderr bytes.Buffer

	code, err := hookMain(stdin, &stderr, srv.URL+"/run", tmpDir)
	if err != nil {
		t.Fatalf("hookMain() error: %v", err)
	}
	if code != 0 {
		t.Fatalf("hookMain() exit code = %d, want 0", code)
	}

	output := stderr.String()
	if !strings.Contains(output, "All tests passed.") {
		t.Errorf("expected 'All tests passed.' in output, got: %s", output)
	}
	if !strings.Contains(output, "scp root@edric:") {
		t.Errorf("expected scp line in output, got: %s", output)
	}

	// Verify output file was written.
	files, err := filepath.Glob(filepath.Join(tmpDir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(files))
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `"e1ed":true`) {
		t.Errorf("output file missing e1ed messages: %s", string(content))
	}
}

func TestIntegrationFail(t *testing.T) {
	ndjson := buildNDJSON(
		map[string]any{"Action": "run", "Test": "TestA", "Package": "./e1e"},
		map[string]any{"Action": "pass", "Test": "TestA", "Package": "./e1e"},
		map[string]any{"Action": "run", "Test": "TestB", "Package": "./e1e"},
		map[string]any{"Action": "fail", "Test": "TestB", "Package": "./e1e"},
		map[string]any{"e1ed": true, "phase": "done", "exit_code": 1, "duration": "2m0s"},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write(ndjson)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	stdin := strings.NewReader("0000000000000000000000000000000000000000 abc123def456 refs/e1e/test\n")
	var stderr bytes.Buffer

	code, err := hookMain(stdin, &stderr, srv.URL+"/run", tmpDir)
	if err != nil {
		t.Fatalf("hookMain() error: %v", err)
	}
	if code != 1 {
		t.Fatalf("hookMain() exit code = %d, want 1", code)
	}

	output := stderr.String()
	if !strings.Contains(output, "FAIL: TestB") {
		t.Errorf("expected 'FAIL: TestB' in output, got: %s", output)
	}
	if !strings.Contains(output, "scp root@edric:") {
		t.Errorf("expected scp line in output, got: %s", output)
	}
}

func TestIntegrationNoMatchingRefs(t *testing.T) {
	stdin := strings.NewReader("0000 abc1 refs/heads/main\n")
	var stderr bytes.Buffer
	_, err := hookMain(stdin, &stderr, "http://unused", "/unused")
	if err != nil {
		t.Fatalf("expected no error for non-matching refs, got: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no output, got: %s", stderr.String())
	}
}

func TestIntegrationMultipleRefs(t *testing.T) {
	stdin := strings.NewReader("0000 abc1 refs/e1e/test\n0000 def2 refs/e1e/other\n")
	var stderr bytes.Buffer
	_, err := hookMain(stdin, &stderr, "http://unused", "/unused")
	if err == nil {
		t.Fatal("expected error for multiple refs")
	}
	if !strings.Contains(err.Error(), "one ref at a time") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegrationE1edError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "commit not found in repo", 400)
	}))
	defer srv.Close()

	stdin := strings.NewReader("0000000000000000000000000000000000000000 abc123def456 refs/e1e/test\n")
	var stderr bytes.Buffer
	_, err := hookMain(stdin, &stderr, srv.URL+"/run", t.TempDir())
	if err == nil {
		t.Fatal("expected error for bad response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("unexpected error: %v", err)
	}
}
