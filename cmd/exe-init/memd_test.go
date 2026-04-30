package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const goldenMeminfo = `MemTotal:        2048000 kB
MemFree:          512000 kB
MemAvailable:    1500000 kB
Buffers:           10000 kB
Cached:           400000 kB
Active(file):     200000 kB
Inactive(file):   180000 kB
Mlocked:           20000 kB
Dirty:              4000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
SReclaimable:      30000 kB
AnonHugePages:         0 kB
`

const goldenVmstat = `nr_free_pages 128000
workingset_refault_file 12345
workingset_refault_anon 67
pgmajfault 9999
foo_bar 1
`

const goldenPSI = `some avg10=0.10 avg60=0.20 avg300=0.30 total=12345
full avg10=0.01 avg60=0.02 avg300=0.03 total=678
`

func writeProcFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	old := memdProcRoot
	memdProcRoot = root
	t.Cleanup(func() { memdProcRoot = old })
}

func TestCollectMemdSampleHappyPath(t *testing.T) {
	root := writeProcFixture(t, map[string]string{
		"meminfo":         goldenMeminfo,
		"vmstat":          goldenVmstat,
		"pressure/memory": goldenPSI,
		"uptime":          "1234.56 9876.54\n",
	})
	withProcRoot(t, root)

	s := collectMemdSample(time.Unix(0, 0))
	if s.Version != MemdProtocolVersion {
		t.Errorf("version=%d", s.Version)
	}
	if s.Meminfo["MemTotal"] != 2048000 {
		t.Errorf("MemTotal=%d", s.Meminfo["MemTotal"])
	}
	if s.Meminfo["Active(file)"] != 200000 {
		t.Errorf("Active(file)=%d", s.Meminfo["Active(file)"])
	}
	if s.Meminfo["Mlocked"] != 20000 {
		t.Errorf("Mlocked=%d", s.Meminfo["Mlocked"])
	}
	if s.Vmstat["workingset_refault_file"] != 12345 {
		t.Errorf("refault_file=%d", s.Vmstat["workingset_refault_file"])
	}
	if _, ok := s.Vmstat["foo_bar"]; ok {
		t.Errorf("unexpected vmstat key foo_bar")
	}
	if s.PSI["some"].Avg60 != 0.20 {
		t.Errorf("some.avg60=%v", s.PSI["some"].Avg60)
	}
	if s.PSI["full"].Total != 678 {
		t.Errorf("full.total=%v", s.PSI["full"].Total)
	}
	if s.UptimeSec != 1234.56 {
		t.Errorf("uptime=%v", s.UptimeSec)
	}
	if len(s.Errors) != 0 {
		t.Errorf("errors=%v", s.Errors)
	}
}

func TestCollectMemdSampleNoPSI(t *testing.T) {
	root := writeProcFixture(t, map[string]string{
		"meminfo": goldenMeminfo,
		"vmstat":  goldenVmstat,
		"uptime":  "100.00 200.00\n",
	})
	withProcRoot(t, root)
	s := collectMemdSample(time.Unix(0, 0))
	if len(s.Errors) == 0 {
		t.Fatalf("expected error for missing PSI")
	}
	if s.Meminfo["MemTotal"] != 2048000 {
		t.Errorf("MemTotal not parsed: %d", s.Meminfo["MemTotal"])
	}
}

func TestCollectMemdSampleHugeMlocked(t *testing.T) {
	huge := strings.Replace(goldenMeminfo, "Mlocked:           20000 kB", "Mlocked:        18000000 kB", 1)
	root := writeProcFixture(t, map[string]string{
		"meminfo":         huge,
		"vmstat":          goldenVmstat,
		"pressure/memory": goldenPSI,
		"uptime":          "100.00 200.00\n",
	})
	withProcRoot(t, root)
	s := collectMemdSample(time.Unix(0, 0))
	if s.Meminfo["Mlocked"] != 18000000 {
		t.Fatalf("Mlocked=%d", s.Meminfo["Mlocked"])
	}
}

// fakeConn is a half-duplex pipe used to drive serveMemdConn.
type fakeConn struct {
	in        io.Reader
	out       io.Writer
	closeOnce sync.Once
	closed    bool
	deadlines []time.Time
}

func (f *fakeConn) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error) { return f.out.Write(p) }
func (f *fakeConn) Close() error                { f.closeOnce.Do(func() { f.closed = true }); return nil }
func (f *fakeConn) setDeadline(t time.Time) error {
	f.deadlines = append(f.deadlines, t)
	return nil
}

func TestServeMemdConnHappy(t *testing.T) {
	root := writeProcFixture(t, map[string]string{
		"meminfo":         goldenMeminfo,
		"vmstat":          goldenVmstat,
		"pressure/memory": goldenPSI,
		"uptime":          "1.0 2.0\n",
	})
	withProcRoot(t, root)

	req := bytes.NewBufferString(MemdRequest)
	var resp bytes.Buffer
	fc := &fakeConn{in: req, out: &resp}
	if err := serveMemdConn(fc, time.Now, fc.setDeadline); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !fc.closed {
		t.Errorf("conn not closed")
	}
	line := resp.Bytes()
	if n := bytes.IndexByte(line, '\n'); n < 0 || n != len(line)-1 {
		t.Fatalf("want single-line response with trailing newline, got %q", line)
	}
	var s MemdSample
	if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &s); err != nil {
		t.Fatalf("unmarshal: %v: %q", err, line)
	}
	if s.Meminfo["MemTotal"] != 2048000 {
		t.Errorf("MemTotal=%d", s.Meminfo["MemTotal"])
	}
}

func TestServeMemdConnBadRequest(t *testing.T) {
	withProcRoot(t, t.TempDir())
	req := bytes.NewBufferString("NOPE\n")
	var resp bytes.Buffer
	fc := &fakeConn{in: req, out: &resp}
	if err := serveMemdConn(fc, time.Now, fc.setDeadline); err == nil {
		t.Fatalf("expected err on bad request")
	}
}

func TestServeMemdConnViaNetPipe(t *testing.T) {
	root := writeProcFixture(t, map[string]string{
		"meminfo":         goldenMeminfo,
		"vmstat":          goldenVmstat,
		"pressure/memory": goldenPSI,
		"uptime":          "1.0 2.0\n",
	})
	withProcRoot(t, root)

	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveMemdConn(a, time.Now, a.SetDeadline) }()

	if _, err := io.WriteString(b, MemdRequest); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	var s MemdSample
	if err := json.Unmarshal(bytes.TrimRight(data, "\n"), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Version != MemdProtocolVersion {
		t.Errorf("version=%d", s.Version)
	}
}
