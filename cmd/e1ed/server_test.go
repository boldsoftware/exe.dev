package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestServer(t *testing.T) (*Server, *mockExecutor) {
	t.Helper()

	// Create a bare git repo with a commit.
	repoDir := t.TempDir()
	bareRepo := filepath.Join(repoDir, "repo.git")

	// Initialize a regular repo, make a commit, then clone bare.
	srcRepo := filepath.Join(repoDir, "src")
	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %s (%v)", name, args, out, err)
		}
	}

	os.MkdirAll(srcRepo, 0o755)
	run(srcRepo, "git", "init")
	// Create a Makefile with a no-op exelet-fs target.
	os.WriteFile(filepath.Join(srcRepo, "Makefile"), []byte("exelet-fs:\n\t@echo done\n"), 0o644)
	run(srcRepo, "git", "add", ".")
	run(srcRepo, "git", "commit", "-m", "init")
	run(repoDir, "git", "clone", "--bare", srcRepo, bareRepo)

	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	pool := NewPool(2, time.Hour, "/tmp/ops", mock)
	pool.Start()
	t.Cleanup(pool.Stop)

	// Wait for pool to be ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	srv := NewServer(pool, bareRepo)
	return srv, mock
}

func TestHandleStatusEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Pool.Target != 2 {
		t.Fatalf("pool target: got %d, want 2", resp.Pool.Target)
	}
	if len(resp.Runs) != 0 {
		t.Fatalf("runs: got %d, want 0", len(resp.Runs))
	}
}

func TestHandleRecycle(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Wait for both slots to be ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.pool.Status().Ready == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	startsBeforeRecycle := mock.startCount.Load()

	req := httptest.NewRequest("POST", "/recycle", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var resp map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["recycled"] != 2 {
		t.Fatalf("recycled: got %d, want 2", resp["recycled"])
	}

	// Wait for VMs to be recreated.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.pool.Status().Ready == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if srv.pool.Status().Ready != 2 {
		t.Fatalf("expected 2 ready after recycle, got %+v", srv.pool.Status())
	}
	if mock.startCount.Load() < startsBeforeRecycle+2 {
		t.Fatalf("expected at least 2 new starts after recycle")
	}
}

func TestHandleRunMissingCommit(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest("POST", "/run", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestHandleRunInvalidJSON(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := strings.NewReader(`not json`)
	req := httptest.NewRequest("POST", "/run", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestHandleRunBadCommit(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := strings.NewReader(`{"commit":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}`)
	req := httptest.NewRequest("POST", "/run", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	// Should get 200 with streaming error (the request was valid JSON,
	// but the commit doesn't exist — error comes in the stream).
	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("expected 'not found' in response, got: %s", w.Body.String())
	}
}

func TestHandleRunSuccess(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Get the HEAD commit SHA from our test bare repo.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = srv.repoPath
	shaOut, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := trimNewline(string(shaOut))

	// The test worktree won't have go.mod or real test packages,
	// so go test will fail, but we should see the streaming setup messages.
	body := strings.NewReader(`{"commit":"` + sha + `","packages":["./..."]}`)
	req := httptest.NewRequest("POST", "/run", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	output := w.Body.String()
	if !strings.Contains(output, `"e1ed":true`) {
		t.Fatalf("expected e1ed messages in output, got: %s", output)
	}
	if !strings.Contains(output, "created worktree") {
		t.Fatalf("expected 'created worktree' in output, got: %s", output)
	}
	if !strings.Contains(output, "claimed environment") {
		t.Fatalf("expected 'claimed environment' in output, got: %s", output)
	}
}
