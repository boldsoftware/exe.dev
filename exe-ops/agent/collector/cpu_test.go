package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCPUStat(t *testing.T) {
	content := "cpu  10132153 290696 3084719 46828483 16683 0 25195 0 0 0\ncpu0 1393280 32966 572056 13343292 6130 0 17875 0 0 0\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	idle, total, err := readCPUStat(path)
	if err != nil {
		t.Fatalf("readCPUStat: %v", err)
	}
	// idle = 46828483
	if idle != 46828483 {
		t.Errorf("idle = %d, want 46828483", idle)
	}
	// total = sum of all fields
	expectedTotal := uint64(10132153 + 290696 + 3084719 + 46828483 + 16683 + 0 + 25195 + 0 + 0 + 0)
	if total != expectedTotal {
		t.Errorf("total = %d, want %d", total, expectedTotal)
	}
}
