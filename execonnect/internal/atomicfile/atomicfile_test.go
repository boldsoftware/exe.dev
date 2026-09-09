package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFileSync(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFileSync: %v", err)
	}
	if err := WriteFileSync(path, []byte("second"), 0o640); err != nil {
		t.Fatalf("WriteFileSync replacement: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "second" {
		t.Fatalf("contents = %q, want second", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions = %o, want 640", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory entries = %v, want only state.json", entries)
	}
}
