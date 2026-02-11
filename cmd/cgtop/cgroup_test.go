//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeFile creates a file with the given content inside dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeFakeCgroupDir creates a minimal fake cgroup directory with common files.
func makeFakeCgroupDir(t *testing.T, root, rel string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// readKeyValueFile
// ---------------------------------------------------------------------------

func TestReadKeyValueFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cpu.stat")
	content := `usage_usec 123456
user_usec 100000
system_usec 23456
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := readKeyValueFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if m["usage_usec"] != 123456 {
		t.Errorf("usage_usec = %d, want 123456", m["usage_usec"])
	}
	if m["user_usec"] != 100000 {
		t.Errorf("user_usec = %d, want 100000", m["user_usec"])
	}
	if m["system_usec"] != 23456 {
		t.Errorf("system_usec = %d, want 23456", m["system_usec"])
	}
}

func TestReadKeyValueFile_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := readKeyValueFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestReadKeyValueFile_MalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad")
	content := "only_one_field\nkey notanumber\ngood 42\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := readKeyValueFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(m), m)
	}
	if m["good"] != 42 {
		t.Errorf("good = %d, want 42", m["good"])
	}
}

func TestReadKeyValueFile_NotFound(t *testing.T) {
	_, err := readKeyValueFile("/nonexistent/path")
	if err == nil {
		t.Error("expected error")
	}
}

// ---------------------------------------------------------------------------
// readSingleUint
// ---------------------------------------------------------------------------

func TestReadSingleUint(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "value")
	if err := os.WriteFile(path, []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := readSingleUint(path)
	if err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Errorf("got %d, want 42", v)
	}
}

func TestReadSingleUint_Whitespace(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "value")
	if err := os.WriteFile(path, []byte("  1234  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := readSingleUint(path)
	if err != nil {
		t.Fatal(err)
	}
	if v != 1234 {
		t.Errorf("got %d, want 1234", v)
	}
}

func TestReadSingleUint_NotNumber(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "value")
	if err := os.WriteFile(path, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readSingleUint(path)
	if err == nil {
		t.Error("expected error for 'max'")
	}
}

// ---------------------------------------------------------------------------
// readIOStat
// ---------------------------------------------------------------------------

func TestReadIOStat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "io.stat")
	content := `259:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=300 dios=5
259:1 rbytes=500 wbytes=600 rios=3 wios=4 dbytes=100 dios=2
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	node := &CgroupNode{
		Stats: make(map[string]float64),
	}
	readIOStat(path, node)

	tests := map[string]float64{
		"io.rbytes": 1500,
		"io.wbytes": 2600,
		"io.rios":   13,
		"io.wios":   24,
		"io.dbytes": 400,
		"io.dios":   7,
	}
	for key, want := range tests {
		got := node.Stats[key]
		if got != want {
			t.Errorf("%s = %f, want %f", key, got, want)
		}
	}
}

func TestReadIOStat_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "io.stat")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	node := &CgroupNode{
		Stats: make(map[string]float64),
	}
	readIOStat(path, node)

	for _, key := range []string{"io.rbytes", "io.wbytes", "io.rios", "io.wios", "io.dbytes", "io.dios"} {
		if node.Stats[key] != 0 {
			t.Errorf("%s = %f, want 0", key, node.Stats[key])
		}
	}
}

func TestReadIOStat_FileNotFound(t *testing.T) {
	node := &CgroupNode{
		Stats: make(map[string]float64),
	}
	readIOStat("/nonexistent/path", node)
	// Should just return without error, no stats set.
	if len(node.Stats) != 0 {
		t.Errorf("expected no stats, got %v", node.Stats)
	}
}

// ---------------------------------------------------------------------------
// walkCgroupTree
// ---------------------------------------------------------------------------

func TestWalkCgroupTree(t *testing.T) {
	root := t.TempDir()

	// Create directory structure:
	// root/
	//   cpu.stat
	//   system.slice/
	//     cpu.stat
	//     ssh.service/
	//       cpu.stat
	//       memory.current
	//   user.slice/

	writeFile(t, root, "cpu.stat", "usage_usec 1000\nuser_usec 800\nsystem_usec 200\n")

	makeFakeCgroupDir(t, root, "system.slice")
	writeFile(t, root, "system.slice/cpu.stat", "usage_usec 500\nuser_usec 400\nsystem_usec 100\n")

	makeFakeCgroupDir(t, root, "system.slice/ssh.service")
	writeFile(t, root, "system.slice/ssh.service/cpu.stat", "usage_usec 100\nuser_usec 80\nsystem_usec 20\n")
	writeFile(t, root, "system.slice/ssh.service/memory.current", "4096\n")

	makeFakeCgroupDir(t, root, "user.slice")

	node, err := walkCgroupTree(root, "")
	if err != nil {
		t.Fatal(err)
	}

	if node.Name != "/" {
		t.Errorf("root name = %q, want /", node.Name)
	}
	if node.Path != "" {
		t.Errorf("root path = %q, want empty", node.Path)
	}
	if node.Stats["cpu.usage_usec"] != 1000 {
		t.Errorf("root cpu.usage_usec = %f, want 1000", node.Stats["cpu.usage_usec"])
	}

	if len(node.Children) != 2 {
		t.Fatalf("root has %d children, want 2", len(node.Children))
	}

	// Find system.slice.
	var systemSlice *CgroupNode
	for _, c := range node.Children {
		if c.Name == "system.slice" {
			systemSlice = c
		}
	}
	if systemSlice == nil {
		t.Fatal("system.slice not found")
	}
	if systemSlice.Path != "system.slice" {
		t.Errorf("system.slice path = %q", systemSlice.Path)
	}
	if systemSlice.Stats["cpu.usage_usec"] != 500 {
		t.Errorf("system.slice cpu.usage_usec = %f, want 500", systemSlice.Stats["cpu.usage_usec"])
	}

	// ssh.service under system.slice.
	if len(systemSlice.Children) != 1 {
		t.Fatalf("system.slice has %d children, want 1", len(systemSlice.Children))
	}
	ssh := systemSlice.Children[0]
	if ssh.Name != "ssh.service" {
		t.Errorf("child name = %q, want ssh.service", ssh.Name)
	}
	if ssh.Stats["memory.current"] != 4096 {
		t.Errorf("ssh memory.current = %f, want 4096", ssh.Stats["memory.current"])
	}
}

func TestWalkCgroupTree_NonexistentRoot(t *testing.T) {
	_, err := walkCgroupTree("/nonexistent/cgroup/root", "")
	if err == nil {
		t.Error("expected error for nonexistent root")
	}
}

func TestWalkCgroupTree_EmptyDir(t *testing.T) {
	root := t.TempDir()
	node, err := walkCgroupTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if node.Name != "/" {
		t.Errorf("name = %q, want /", node.Name)
	}
	if len(node.Children) != 0 {
		t.Errorf("expected no children, got %d", len(node.Children))
	}
}

// ---------------------------------------------------------------------------
// readAllStats
// ---------------------------------------------------------------------------

func TestReadAllStats(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "cpu.stat", "usage_usec 5000\nuser_usec 3000\nsystem_usec 2000\n")
	writeFile(t, root, "memory.current", "1048576\n")
	writeFile(t, root, "memory.swap.current", "0\n")
	writeFile(t, root, "memory.stat", "anon 500000\nfile 300000\nkernel 100000\npgfault 42\n")
	writeFile(t, root, "memory.events", "low 0\nhigh 0\nmax 0\noom 1\noom_kill 1\n")
	writeFile(t, root, "pids.current", "15\n")
	writeFile(t, root, "io.stat", "259:0 rbytes=4096 wbytes=8192 rios=1 wios=2 dbytes=0 dios=0\n")
	writeFile(t, root, "cpu.pressure", "some avg10=1.00 avg60=2.00 avg300=3.00 total=1000\nfull avg10=0.10 avg60=0.20 avg300=0.30 total=500\n")

	node := &CgroupNode{
		Stats:  make(map[string]float64),
		Config: make(map[string]any),
	}
	readAllStats(root, node)

	// CPU stats.
	if node.Stats["cpu.usage_usec"] != 5000 {
		t.Errorf("cpu.usage_usec = %f, want 5000", node.Stats["cpu.usage_usec"])
	}

	// Memory.
	if node.Stats["memory.current"] != 1048576 {
		t.Errorf("memory.current = %f, want 1048576", node.Stats["memory.current"])
	}
	if node.Stats["memory.swap.current"] != 0 {
		t.Errorf("memory.swap.current = %f, want 0", node.Stats["memory.swap.current"])
	}

	// Memory.stat.
	if node.Stats["memory.stat.anon"] != 500000 {
		t.Errorf("memory.stat.anon = %f, want 500000", node.Stats["memory.stat.anon"])
	}
	if node.Stats["memory.stat.pgfault"] != 42 {
		t.Errorf("memory.stat.pgfault = %f, want 42", node.Stats["memory.stat.pgfault"])
	}

	// Memory.events.
	if node.Stats["memory.events.oom_kill"] != 1 {
		t.Errorf("memory.events.oom_kill = %f, want 1", node.Stats["memory.events.oom_kill"])
	}

	// PIDs.
	if node.Stats["pids.current"] != 15 {
		t.Errorf("pids.current = %f, want 15", node.Stats["pids.current"])
	}

	// IO.
	if node.Stats["io.rbytes"] != 4096 {
		t.Errorf("io.rbytes = %f, want 4096", node.Stats["io.rbytes"])
	}
	if node.Stats["io.wbytes"] != 8192 {
		t.Errorf("io.wbytes = %f, want 8192", node.Stats["io.wbytes"])
	}

	// PSI.
	assertClose(t, "psi.cpu.some.avg10", node.Stats["psi.cpu.some.avg10"], 1.0)
	assertClose(t, "psi.cpu.full.avg10", node.Stats["psi.cpu.full.avg10"], 0.1)
}

// ---------------------------------------------------------------------------
// readAllConfig
// ---------------------------------------------------------------------------

func TestReadAllConfig(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "cpu.weight", "100\n")
	writeFile(t, root, "cpu.max", "100000 100000\n")
	writeFile(t, root, "memory.max", "max\n")
	writeFile(t, root, "memory.min", "0\n")
	writeFile(t, root, "pids.max", "1000\n")

	node := &CgroupNode{
		Stats:  make(map[string]float64),
		Config: make(map[string]any),
	}
	readAllConfig(root, node)

	if node.Config["cpu.weight"] != uint64(100) {
		t.Errorf("cpu.weight = %v, want 100", node.Config["cpu.weight"])
	}
	if node.Config["cpu.max"] != "100000 100000" {
		t.Errorf("cpu.max = %v, want '100000 100000'", node.Config["cpu.max"])
	}
	if node.Config["memory.max"] != "max" {
		t.Errorf("memory.max = %v, want 'max'", node.Config["memory.max"])
	}
	if node.Config["memory.min"] != uint64(0) {
		t.Errorf("memory.min = %v (type %T), want uint64(0)", node.Config["memory.min"], node.Config["memory.min"])
	}
	if node.Config["pids.max"] != uint64(1000) {
		t.Errorf("pids.max = %v, want 1000", node.Config["pids.max"])
	}
}

func TestReadAllConfig_MissingFiles(t *testing.T) {
	root := t.TempDir()
	node := &CgroupNode{
		Stats:  make(map[string]float64),
		Config: make(map[string]any),
	}
	readAllConfig(root, node)

	// Should just have no config entries, no panics.
	if len(node.Config) != 0 {
		t.Errorf("expected no config, got %v", node.Config)
	}
}

// ---------------------------------------------------------------------------
// Integration: walkCgroupTree with full stats and config
// ---------------------------------------------------------------------------

func TestWalkCgroupTree_FullIntegration(t *testing.T) {
	root := t.TempDir()

	// Root cgroup.
	writeFile(t, root, "cpu.stat", "usage_usec 10000\nuser_usec 8000\nsystem_usec 2000\n")
	writeFile(t, root, "memory.current", "2097152\n")
	writeFile(t, root, "pids.current", "50\n")
	writeFile(t, root, "cpu.weight", "100\n")
	writeFile(t, root, "memory.max", "max\n")

	// Child cgroup.
	childDir := makeFakeCgroupDir(t, root, "app.slice")
	writeFile(t, childDir, "cpu.stat", "usage_usec 5000\nuser_usec 4000\nsystem_usec 1000\n")
	writeFile(t, childDir, "memory.current", "1048576\n")
	writeFile(t, childDir, "pids.current", "10\n")
	writeFile(t, childDir, "memory.max", "4194304\n")

	node, err := walkCgroupTree(root, "")
	if err != nil {
		t.Fatal(err)
	}

	// Root checks.
	if node.Stats["cpu.usage_usec"] != 10000 {
		t.Errorf("root cpu.usage_usec = %f", node.Stats["cpu.usage_usec"])
	}
	if node.Config["memory.max"] != "max" {
		t.Errorf("root memory.max = %v", node.Config["memory.max"])
	}

	// Child checks.
	if len(node.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(node.Children))
	}
	child := node.Children[0]
	if child.Name != "app.slice" {
		t.Errorf("child name = %q", child.Name)
	}
	if child.Stats["memory.current"] != 1048576 {
		t.Errorf("child memory.current = %f", child.Stats["memory.current"])
	}
	if child.Config["memory.max"] != uint64(4194304) {
		t.Errorf("child memory.max = %v (type %T)", child.Config["memory.max"], child.Config["memory.max"])
	}
}
