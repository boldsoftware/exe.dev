//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ringBuf
// ---------------------------------------------------------------------------

func TestRingBuf_Empty(t *testing.T) {
	var r ringBuf
	if got := r.values(); got != nil {
		t.Fatalf("empty ringBuf.values() = %v, want nil", got)
	}
}

func TestRingBuf_PartialFill(t *testing.T) {
	var r ringBuf
	r.push(1)
	r.push(2)
	r.push(3)
	got := r.values()
	want := []float64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("values()[%d] = %f, want %f", i, got[i], want[i])
		}
	}
}

func TestRingBuf_WrapAround(t *testing.T) {
	var r ringBuf
	// Push more than sparklineLen values.
	for i := range sparklineLen + 5 {
		r.push(float64(i))
	}
	got := r.values()
	if len(got) != sparklineLen {
		t.Fatalf("len = %d, want %d", len(got), sparklineLen)
	}
	// Should contain the last sparklineLen values in order.
	for i, v := range got {
		want := float64(5 + i)
		if v != want {
			t.Errorf("values()[%d] = %f, want %f", i, v, want)
		}
	}
}

func TestRingBuf_ExactFill(t *testing.T) {
	var r ringBuf
	for i := range sparklineLen {
		r.push(float64(i))
	}
	got := r.values()
	if len(got) != sparklineLen {
		t.Fatalf("len = %d, want %d", len(got), sparklineLen)
	}
	for i, v := range got {
		if v != float64(i) {
			t.Errorf("values()[%d] = %f, want %f", i, v, float64(i))
		}
	}
}

// ---------------------------------------------------------------------------
// findSubtree
// ---------------------------------------------------------------------------

func makeTree() *CgroupNode {
	return &CgroupNode{
		Name: "/",
		Path: "",
		Children: []*CgroupNode{
			{
				Name: "system.slice",
				Path: "system.slice",
				Children: []*CgroupNode{
					{Name: "ssh.service", Path: "system.slice/ssh.service"},
				},
			},
			{Name: "user.slice", Path: "user.slice"},
		},
	}
}

func TestFindSubtree_Root(t *testing.T) {
	tree := makeTree()
	got := findSubtree(tree, "")
	if got != tree {
		t.Fatal("expected root node")
	}
}

func TestFindSubtree_Found(t *testing.T) {
	tree := makeTree()
	got := findSubtree(tree, "system.slice")
	if got == nil || got.Path != "system.slice" {
		t.Fatalf("got %v, want system.slice", got)
	}
}

func TestFindSubtree_Nested(t *testing.T) {
	tree := makeTree()
	got := findSubtree(tree, "system.slice/ssh.service")
	if got == nil || got.Path != "system.slice/ssh.service" {
		t.Fatalf("got %v, want system.slice/ssh.service", got)
	}
}

func TestFindSubtree_NotFound(t *testing.T) {
	tree := makeTree()
	got := findSubtree(tree, "nonexistent")
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestFindSubtree_LeadingSlash(t *testing.T) {
	tree := makeTree()
	got := findSubtree(tree, "/user.slice")
	if got == nil || got.Path != "user.slice" {
		t.Fatalf("got %v, want user.slice", got)
	}
}

// ---------------------------------------------------------------------------
// processNode
// ---------------------------------------------------------------------------

func TestProcessNode_NoPrevious(t *testing.T) {
	c := newCollector("/fake")
	node := &CgroupNode{
		Name:  "test",
		Path:  "test",
		Stats: map[string]float64{"cpu.usage_usec": 1000, "memory.current": 500},
	}
	newRaw := make(map[string]map[string]float64)
	c.processNode(node, 0, newRaw)

	// No previous data → no rates.
	if len(node.Rates) != 0 {
		t.Errorf("expected no rates without previous data, got %v", node.Rates)
	}
	// Cumulative stats should be removed from Stats.
	if _, ok := node.Stats["cpu.usage_usec"]; ok {
		t.Error("cpu.usage_usec should be deleted from Stats")
	}
	// Gauge stats remain.
	if node.Stats["memory.current"] != 500 {
		t.Error("memory.current should remain in Stats")
	}
	// Raw should be recorded.
	if newRaw["test"]["cpu.usage_usec"] != 1000 {
		t.Error("raw value not recorded")
	}
}

func TestProcessNode_WithPrevious(t *testing.T) {
	c := newCollector("/fake")
	c.prevRawStats["test"] = map[string]float64{"cpu.usage_usec": 500}

	node := &CgroupNode{
		Name:  "test",
		Path:  "test",
		Stats: map[string]float64{"cpu.usage_usec": 1500},
	}
	newRaw := make(map[string]map[string]float64)
	c.processNode(node, 2.0, newRaw)

	want := (1500.0 - 500.0) / 2.0
	if got := node.Rates["cpu.usage_usec"]; got != want {
		t.Errorf("rate = %f, want %f", got, want)
	}
}

func TestProcessNode_NegativeRateClamped(t *testing.T) {
	c := newCollector("/fake")
	c.prevRawStats["test"] = map[string]float64{"cpu.usage_usec": 2000}

	node := &CgroupNode{
		Name:  "test",
		Path:  "test",
		Stats: map[string]float64{"cpu.usage_usec": 1000},
	}
	newRaw := make(map[string]map[string]float64)
	c.processNode(node, 1.0, newRaw)

	if got := node.Rates["cpu.usage_usec"]; got != 0 {
		t.Errorf("negative rate should be clamped to 0, got %f", got)
	}
}

func TestProcessNode_Children(t *testing.T) {
	c := newCollector("/fake")
	c.prevRawStats["parent"] = map[string]float64{"cpu.usage_usec": 100}
	c.prevRawStats["parent/child"] = map[string]float64{"cpu.usage_usec": 200}

	child := &CgroupNode{
		Name:  "child",
		Path:  "parent/child",
		Stats: map[string]float64{"cpu.usage_usec": 400},
	}
	parent := &CgroupNode{
		Name:     "parent",
		Path:     "parent",
		Stats:    map[string]float64{"cpu.usage_usec": 300},
		Children: []*CgroupNode{child},
	}
	newRaw := make(map[string]map[string]float64)
	c.processNode(parent, 1.0, newRaw)

	if parent.Rates["cpu.usage_usec"] != 200 {
		t.Errorf("parent rate = %f, want 200", parent.Rates["cpu.usage_usec"])
	}
	if child.Rates["cpu.usage_usec"] != 200 {
		t.Errorf("child rate = %f, want 200", child.Rates["cpu.usage_usec"])
	}
}

// ---------------------------------------------------------------------------
// collectSparklines
// ---------------------------------------------------------------------------

func TestCollectSparklines(t *testing.T) {
	rings := make(map[sparklineKey]*ringBuf)

	// Add a sparkline for a rate metric.
	key := sparklineKey{path: "test", metric: "cpu.usage_usec"}
	ring := &ringBuf{}
	ring.push(10)
	ring.push(20)
	rings[key] = ring

	// Add a sparkline for a gauge metric.
	gaugeKey := sparklineKey{path: "test", metric: "memory.current"}
	gaugeRing := &ringBuf{}
	gaugeRing.push(999)
	rings[gaugeKey] = gaugeRing

	node := &CgroupNode{Path: "test"}
	out := make(map[string]map[string][]float64)
	collectSparklines(node, rings, out)

	if out["test"] == nil {
		t.Fatal("expected sparklines for 'test'")
	}
	if vals := out["test"]["cpu.usage_usec"]; len(vals) != 2 || vals[0] != 10 || vals[1] != 20 {
		t.Errorf("cpu sparkline = %v, want [10 20]", vals)
	}
	if vals := out["test"]["memory.current"]; len(vals) != 1 || vals[0] != 999 {
		t.Errorf("memory sparkline = %v, want [999]", vals)
	}
}

func TestCollectSparklines_NoData(t *testing.T) {
	rings := make(map[sparklineKey]*ringBuf)
	node := &CgroupNode{Path: "test"}
	out := make(map[string]map[string][]float64)
	collectSparklines(node, rings, out)

	if len(out) != 0 {
		t.Errorf("expected empty sparklines, got %v", out)
	}
}

// ---------------------------------------------------------------------------
// snapshot
// ---------------------------------------------------------------------------

func TestSnapshot_RootFilter(t *testing.T) {
	c := newCollector("/fake")
	c.tree = makeTree()
	c.prevTime = time.Now()

	resp := c.snapshot("system.slice")
	if resp.Tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if resp.Tree.Path != "system.slice" {
		t.Errorf("tree path = %q, want system.slice", resp.Tree.Path)
	}
}

func TestSnapshot_NoFilter(t *testing.T) {
	c := newCollector("/fake")
	c.tree = makeTree()
	c.prevTime = time.Now()

	resp := c.snapshot("")
	if resp.Tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if resp.Tree.Path != "" {
		t.Errorf("tree path = %q, want empty (root)", resp.Tree.Path)
	}
}

func TestSnapshot_StaleDataRefresh(t *testing.T) {
	// Create a real fake cgroup tree so collectLocked won't fail.
	tmpDir := t.TempDir()
	c := newCollector(tmpDir)
	// prevTime is zero → should trigger a collection.
	resp := c.snapshot("")
	// After snapshot, prevTime should be set.
	if c.prevTime.IsZero() {
		t.Error("prevTime should be set after snapshot")
	}
	if resp.Timestamp == 0 {
		t.Error("timestamp should be non-zero")
	}
}

// ---------------------------------------------------------------------------
// API handler
// ---------------------------------------------------------------------------

func TestAPIHandler_ValidRequest(t *testing.T) {
	tmpDir := t.TempDir()
	c := newCollector(tmpDir)
	c.tree = &CgroupNode{
		Name:   "/",
		Path:   "",
		Stats:  map[string]float64{},
		Config: map[string]any{},
	}
	c.prevTime = time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		rootParam := r.URL.Query().Get("root")
		if rootParam != "" {
			cleaned := filepath.Clean(rootParam)
			if containsDotDot(cleaned) {
				http.Error(w, "invalid root path", 400)
				return
			}
			rootParam = cleaned
		}
		resp := c.snapshot(rootParam)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		t.Fatal(err)
	}
}

// containsDotDot mirrors the check in main.go.
func containsDotDot(s string) bool {
	for _, part := range filepath.SplitList(s) {
		_ = part
	}
	// Simple string check like the original.
	return len(s) >= 2 && (s == ".." || s[:2] == ".." || s[len(s)-2:] == ".." || findDotDot(s))
}

func findDotDot(s string) bool {
	for i := range len(s) - 1 {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}

func TestAPIHandler_PathTraversalPrevented(t *testing.T) {
	tmpDir := t.TempDir()
	c := newCollector(tmpDir)
	c.tree = &CgroupNode{
		Name:   "/",
		Path:   "",
		Stats:  map[string]float64{},
		Config: map[string]any{},
	}
	c.prevTime = time.Now()

	// Replicate the real handler logic from main.go.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		rootParam := r.URL.Query().Get("root")
		if rootParam != "" {
			cleaned := filepath.Clean(rootParam)
			if strings.Contains(cleaned, "..") {
				http.Error(w, "invalid root path", 400)
				return
			}
			rootParam = cleaned
		}
		resp := c.snapshot(rootParam)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	tests := []struct {
		name string
		root string
		want int
	}{
		{"dotdot", "../../../etc/passwd", 400},
		{"embedded_dotdot", "system.slice/../../etc", 400},
		{"valid", "system.slice", 200},
		{"empty", "", 200},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := ts.URL + "/api/data?root=" + tc.root
			resp, err := http.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip of APIResponse
// ---------------------------------------------------------------------------

func TestSampleResponseParse(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_response.json")
	if err != nil {
		t.Skip("testdata/sample_response.json not available")
	}
	var resp APIResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to parse sample response: %v", err)
	}
	if resp.System.LoadAvg.Load1 == 0 && resp.System.LoadAvg.Load5 == 0 {
		t.Error("loadavg looks empty")
	}
	if resp.Tree == nil {
		t.Error("tree is nil")
	}
}

// ---------------------------------------------------------------------------
// collector stale data reset
// ---------------------------------------------------------------------------

func TestCollector_StaleReset(t *testing.T) {
	tmpDir := t.TempDir()
	c := newCollector(tmpDir)

	// Simulate prior data that is old.
	c.prevTime = time.Now().Add(-60 * time.Second)
	c.prevRawStats["old"] = map[string]float64{"cpu.usage_usec": 999}
	sk := sparklineKey{path: "old", metric: "cpu.usage_usec"}
	c.sparkRings[sk] = &ringBuf{}
	c.sparkRings[sk].push(42)

	c.collect()

	// After collecting with stale data, the old entries should be cleared.
	if _, ok := c.prevRawStats["old"]; ok {
		t.Error("old prevRawStats should be cleared on stale reset")
	}
	if _, ok := c.sparkRings[sk]; ok {
		t.Error("old sparkRings should be cleared on stale reset")
	}
}
