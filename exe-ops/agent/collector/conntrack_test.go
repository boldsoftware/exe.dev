package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConntrackCollect(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "nf_conntrack_count")
	maxPath := filepath.Join(dir, "nf_conntrack_max")

	if err := os.WriteFile(countPath, []byte("1234\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(maxPath, []byte("65536\n"), 0644); err != nil {
		t.Fatal(err)
	}

	c := &Conntrack{countPath: countPath, maxPath: maxPath}
	if err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if c.Count == nil {
		t.Fatal("Count = nil, want non-nil")
	}
	if *c.Count != 1234 {
		t.Errorf("Count = %d, want 1234", *c.Count)
	}
	if c.Max == nil {
		t.Fatal("Max = nil, want non-nil")
	}
	if *c.Max != 65536 {
		t.Errorf("Max = %d, want 65536", *c.Max)
	}
}

func TestConntrackCollectMissingFiles(t *testing.T) {
	c := &Conntrack{
		countPath: "/nonexistent/nf_conntrack_count",
		maxPath:   "/nonexistent/nf_conntrack_max",
	}
	if err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect should return nil for missing files, got: %v", err)
	}
	if c.Count != nil {
		t.Errorf("Count = %d, want nil", *c.Count)
	}
	if c.Max != nil {
		t.Errorf("Max = %d, want nil", *c.Max)
	}
}

func TestConntrackCollectCountOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	maxPath := filepath.Join(dir, "nf_conntrack_max")
	if err := os.WriteFile(maxPath, []byte("65536\n"), 0644); err != nil {
		t.Fatal(err)
	}

	c := &Conntrack{
		countPath: "/nonexistent/nf_conntrack_count",
		maxPath:   maxPath,
	}
	if err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if c.Count != nil {
		t.Errorf("Count = %d, want nil", *c.Count)
	}
	if c.Max != nil {
		t.Errorf("Max should be nil when count file is missing, got %d", *c.Max)
	}
}
