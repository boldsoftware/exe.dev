package xshelley

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetShelleyMultipleArchitectures(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			serveRelease(w, r)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write([]byte("fake-binary"))
		default:
			http.NotFound(w, r)
		}
	})
	resetTestGlobals(t, handler)

	pathAmd64, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("GetShelley(amd64) failed: %v", err)
	}

	pathArm64, err := GetShelley(context.Background(), "arm64")
	if err != nil {
		t.Fatalf("GetShelley(arm64) failed: %v", err)
	}

	if pathAmd64 == pathArm64 {
		t.Errorf("expected different paths for different architectures")
	}
}

func TestCacheRefresh(t *testing.T) {
	var apiCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			apiCalls.Add(1)
			serveRelease(w, r)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write([]byte("fake-binary"))
		default:
			http.NotFound(w, r)
		}
	})
	resetTestGlobals(t, handler)

	// First call — downloads and caches.
	path, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("first GetShelley failed: %v", err)
	}

	// Second call — cache is fresh, no API call expected.
	beforeCached := apiCalls.Load()
	path2, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("cached GetShelley failed: %v", err)
	}
	if path != path2 {
		t.Errorf("expected same path from cache")
	}
	if apiCalls.Load() != beforeCached {
		t.Errorf("expected no additional API call for fresh cache")
	}

	// Age the metadata by rewriting last_checked to 2 hours ago.
	metadataPath := filepath.Join(filepath.Dir(path), metadataFileName)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}
	var meta cacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("failed to parse metadata: %v", err)
	}
	meta.LastChecked = time.Now().Add(-2 * time.Hour)
	data, err = json.Marshal(meta)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		t.Fatalf("failed to write aged metadata: %v", err)
	}

	// Third call — stale cache should trigger refresh.
	beforeRefresh := apiCalls.Load()
	path3, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("refresh GetShelley failed: %v", err)
	}
	if path3 != path {
		t.Errorf("expected same path after refresh (same tag)")
	}
	if apiCalls.Load() == beforeRefresh {
		t.Errorf("expected API call for stale cache")
	}
}

func TestCacheInvalidation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			serveRelease(w, r)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write([]byte("fake-binary"))
		default:
			http.NotFound(w, r)
		}
	})
	resetTestGlobals(t, handler)

	path, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("GetShelley failed: %v", err)
	}

	// Delete the binary but keep metadata.
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove binary: %v", err)
	}

	// Should re-download since binary is missing.
	path2, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("GetShelley after deletion failed: %v", err)
	}
	if path != path2 {
		t.Errorf("expected same path after re-download")
	}
	if _, err := os.Stat(path2); err != nil {
		t.Fatalf("binary not re-downloaded: %v", err)
	}
}

func TestInvalidMetadata(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			serveRelease(w, r)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Write([]byte("fake-binary"))
		default:
			http.NotFound(w, r)
		}
	})
	resetTestGlobals(t, handler)

	// Pre-create the platform dir with invalid metadata.
	cacheDirPath, _ := getCacheDir()
	platformDir := filepath.Join(cacheDirPath, "linux-amd64")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatalf("failed to create platform dir: %v", err)
	}
	metadataPath := filepath.Join(platformDir, metadataFileName)
	if err := os.WriteFile(metadataPath, []byte("invalid json"), 0o644); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	path, err := GetShelley(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("GetShelley with invalid metadata failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("binary not found: %v", err)
	}

	fixed, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}
	if string(fixed) == "invalid json" {
		t.Error("metadata was not fixed")
	}
}
