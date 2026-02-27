package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestPullExeuntuEverywhere(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	url := fmt.Sprintf("http://localhost:%d/pull-exeuntu-everywhere-517c8a904?tag=latest", Env.servers.Exed.HTTPPort)
	t.Logf("pulling image via %s", url)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	t.Logf("response status: %d", resp.StatusCode)
	t.Logf("response body: %s", body)

	// Parse the response
	var result struct {
		Image   string `json:"image"`
		Success bool   `json:"success"`
		Results []struct {
			Host   string `json:"host"`
			Digest string `json:"digest,omitempty"`
			Error  string `json:"error,omitempty"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse response: %v\nbody: %s", err, body)
	}

	if result.Image != "ghcr.io/boldsoftware/exeuntu:latest" {
		t.Errorf("expected image ghcr.io/boldsoftware/exeuntu:latest, got %q", result.Image)
	}

	if len(result.Results) == 0 {
		t.Errorf("expected at least one result, got none")
	}

	// Check that all hosts succeeded (or already had the image)
	for _, r := range result.Results {
		if r.Error != "" {
			t.Errorf("host %s failed: %s", r.Host, r.Error)
		} else {
			t.Logf("host %s: digest=%s", r.Host, r.Digest)
		}
	}

	if !result.Success {
		t.Errorf("expected success=true, got false")
	}

	// HTTP status should be 200 if all succeeded
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected HTTP 200, got %d", resp.StatusCode)
	}
}
