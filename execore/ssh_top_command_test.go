package execore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// TestTopFmtBytes
// ---------------------------------------------------------------------------

func TestTopFmtBytes(t *testing.T) {
	tests := []struct {
		input    uint64
		expected string
	}{
		{0, "-"},
		{1, "1B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1K"},
		{1536, "2K"},
		{100 * 1024, "100K"},
		{1024 * 1024, "1M"},
		{500 * 1024 * 1024, "500M"},
		{1024 * 1024 * 1024, "1.0G"},
		{2*1024*1024*1024 + 512*1024*1024, "2.5G"},
		{10 * 1024 * 1024 * 1024, "10.0G"},
	}
	for _, tt := range tests {
		got := topFmtBytes(tt.input)
		if got != tt.expected {
			t.Errorf("topFmtBytes(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// TestFmtNetRate
// ---------------------------------------------------------------------------

func TestFmtNetRate(t *testing.T) {
	tests := []struct {
		name         string
		bytesPerSec  float64
		wantContains string
	}{
		{"zero", 0, "-"},
		{"small_bps", 10, "bps"},
		{"kbps", 10_000, "Kbps"},
		{"mbps", 1_000_000, "Mbps"},         // 1 MB/s = 8 Mbps
		{"gbps", 200_000_000, "Gbps"},       // 200 MB/s = 1.6 Gbps
		{"12_mbps", 1_500_000, "12.0 Mbps"}, // 1.5 MB/s = 12 Mbps
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmtNetRate(tt.bytesPerSec)
			if !strings.Contains(got, tt.wantContains) {
				t.Errorf("fmtNetRate(%v) = %q, want to contain %q", tt.bytesPerSec, got, tt.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestColorizeCPU
// ---------------------------------------------------------------------------

func TestColorizeCPU(t *testing.T) {
	tests := []struct {
		name      string
		pct       float64
		wantANSI  string // expected ANSI prefix
		wantValue string // expected numeric substring
	}{
		{"dim_low", 0.0, "\033[2m", "0.0%"},
		{"dim_19", 19.9, "\033[2m", "19.9%"},
		{"green_20", 20.0, "\033[32m", "20.0%"},
		{"green_49", 49.9, "\033[32m", "49.9%"},
		{"yellow_50", 50.0, "\033[33m", "50.0%"},
		{"yellow_69", 69.9, "\033[33m", "69.9%"},
		{"red_70", 70.0, "\033[31m", "70.0%"},
		{"red_89", 89.9, "\033[31m", "89.9%"},
		{"bright_red_90", 90.0, "\033[1;31m", "90.0%"},
		{"bright_red_100", 100.0, "\033[1;31m", "100.0%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorizeCPU(tt.pct)
			if !strings.Contains(got, tt.wantANSI) {
				t.Errorf("colorizeCPU(%v) = %q, want ANSI prefix %q", tt.pct, got, tt.wantANSI)
			}
			if !strings.Contains(got, tt.wantValue) {
				t.Errorf("colorizeCPU(%v) = %q, want value %q", tt.pct, got, tt.wantValue)
			}
			if !strings.HasSuffix(got, "\033[0m") {
				t.Errorf("colorizeCPU(%v) = %q, want reset suffix", tt.pct, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestColorizeMemory
// ---------------------------------------------------------------------------

func TestColorizeMemory(t *testing.T) {
	tests := []struct {
		name     string
		bytes    uint64
		wantANSI string
	}{
		{"dim_zero", 0, "\033[2m"},
		{"dim_256M", 256 * 1024 * 1024, "\033[2m"},
		{"green_512M", 512 * 1024 * 1024, "\033[32m"},
		{"green_1G", 1024 * 1024 * 1024, "\033[32m"},
		{"yellow_2G", 2 * 1024 * 1024 * 1024, "\033[33m"},
		{"yellow_3G", 3 * 1024 * 1024 * 1024, "\033[33m"},
		{"red_4G", 4 * 1024 * 1024 * 1024, "\033[1;31m"},
		{"red_8G", 8 * 1024 * 1024 * 1024, "\033[1;31m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorizeMemory(tt.bytes)
			if !strings.Contains(got, tt.wantANSI) {
				t.Errorf("colorizeMemory(%d) = %q, want ANSI %q", tt.bytes, got, tt.wantANSI)
			}
			if !strings.HasSuffix(got, "\033[0m") {
				t.Errorf("colorizeMemory(%d) = %q, want reset suffix", tt.bytes, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestColorizeStatus
// ---------------------------------------------------------------------------

func TestColorizeStatus(t *testing.T) {
	tests := []struct {
		status   string
		wantANSI string
	}{
		{"running", "\033[1;32m"},
		{"stopped", "\033[2m"},
		{"failed", "\033[1;31m"},
		{"building", "\033[1;33m"},
		{"pending", "\033[1;33m"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := colorizeStatus(tt.status)
			if !strings.Contains(got, tt.wantANSI) {
				t.Errorf("colorizeStatus(%q) = %q, want ANSI %q", tt.status, got, tt.wantANSI)
			}
			if !strings.Contains(got, tt.status) {
				t.Errorf("colorizeStatus(%q) = %q, want status text", tt.status, got)
			}
		})
	}

	// Unknown status should be returned as-is with no ANSI.
	t.Run("unknown", func(t *testing.T) {
		got := colorizeStatus("unknown-state")
		if got != "unknown-state" {
			t.Errorf("colorizeStatus(%q) = %q, want plain string", "unknown-state", got)
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func dummyFetchFunc(_ context.Context) ([]vmUsageRow, error) {
	return nil, nil
}

func newTestModel(rows []vmUsageRow, err error) *topModel {
	return &topModel{
		rows:      rows,
		err:       err,
		startTime: time.Now(),
		fetchFunc: dummyFetchFunc,
	}
}

// ---------------------------------------------------------------------------
// TestTopModelView
// ---------------------------------------------------------------------------

func TestTopModelView(t *testing.T) {
	t.Run("empty_rows", func(t *testing.T) {
		m := newTestModel(nil, nil)
		out := m.View()
		if !strings.Contains(out, "no running VMs") {
			t.Errorf("View() with no rows should contain 'no running VMs', got %q", out)
		}
		if !strings.Contains(out, "exe top") {
			t.Errorf("View() should contain header 'exe top', got %q", out)
		}
	})

	t.Run("with_rows", func(t *testing.T) {
		rows := []vmUsageRow{
			{
				Name:             "my-vm",
				Status:           "running",
				CPUPercent:       45.2,
				MemBytes:         1024 * 1024 * 1024,      // 1G RSS
				SwapBytes:        512 * 1024 * 1024,       // 512M swap
				MemCapacity:      4 * 1024 * 1024 * 1024,  // 4G allocated
				DiskBytes:        300 * 1024 * 1024,       // 300M compressed
				DiskLogicalBytes: 500 * 1024 * 1024,       // 500M logical
				DiskCapacity:     10 * 1024 * 1024 * 1024, // 10G capacity
				NetRx:            2048,
				NetTx:            4096,
			},
		}
		m := newTestModel(rows, nil)
		out := m.View()

		if !strings.Contains(out, "VM") || !strings.Contains(out, "STATUS") || !strings.Contains(out, "CPU%") {
			t.Errorf("View() should contain column headers, got %q", out)
		}
		for _, h := range []string{"MEM", "SWAP", "RAM", "DISK"} {
			if !strings.Contains(out, h) {
				t.Errorf("View() should contain column header %q, got %q", h, out)
			}
		}
		if !strings.Contains(out, "my-vm") {
			t.Errorf("View() should contain VM name 'my-vm', got %q", out)
		}
		// MEM is now active memory only (RSS) = 1.0G.
		if !strings.Contains(out, "1.0G") {
			t.Errorf("View() should show active memory '1.0G', got %q", out)
		}
		// SWAP shows 512M.
		if !strings.Contains(out, "512M") {
			t.Errorf("View() should show swap '512M', got %q", out)
		}
		// RAM (allocation) shows 4.0G.
		if !strings.Contains(out, "4.0G") {
			t.Errorf("View() should show allocated RAM '4.0G', got %q", out)
		}
		// DISK column now shows allocated capacity only.
		if !strings.Contains(out, "10.0G") {
			t.Errorf("View() should show allocated disk capacity '10.0G', got %q", out)
		}
	})

	t.Run("with_net_rates", func(t *testing.T) {
		rows := []vmUsageRow{
			{
				Name:       "my-vm",
				Status:     "running",
				CPUPercent: 10.0,
				MemBytes:   100 * 1024 * 1024,
			},
		}
		m := newTestModel(rows, nil)
		m.netRxRate = map[string]float64{"my-vm": 1_250_000} // 10 Mbps
		m.netTxRate = map[string]float64{"my-vm": 125_000}   // 1 Mbps
		out := m.View()
		if !strings.Contains(out, "Mbps") {
			t.Errorf("View() with net rates should contain 'Mbps', got %q", out)
		}
		if !strings.Contains(out, "10.0 Mbps") {
			t.Errorf("View() should show '10.0 Mbps' for rx, got %q", out)
		}
		if !strings.Contains(out, "1.0 Mbps") {
			t.Errorf("View() should show '1.0 Mbps' for tx, got %q", out)
		}
	})

	t.Run("error_state", func(t *testing.T) {
		m := newTestModel(nil, errors.New("connection refused"))
		out := m.View()
		if !strings.Contains(out, "error") || !strings.Contains(out, "connection refused") {
			t.Errorf("View() with error should show error message, got %q", out)
		}
	})

	t.Run("quitting", func(t *testing.T) {
		m := newTestModel(nil, nil)
		m.quitting = true
		out := m.View()
		if out != "" {
			t.Errorf("View() when quitting should return empty string, got %q", out)
		}
	})
}

// ---------------------------------------------------------------------------
// TestTopModelUpdate
// ---------------------------------------------------------------------------

func TestTopModelUpdate(t *testing.T) {
	t.Run("usageMsg_updates_rows", func(t *testing.T) {
		m := newTestModel(nil, nil)
		rows := []vmUsageRow{{Name: "vm1", Status: "running"}}
		updated, _ := m.Update(usageMsg{rows: rows, err: nil})
		um := updated.(*topModel)
		if len(um.rows) != 1 || um.rows[0].Name != "vm1" {
			t.Errorf("Update(usageMsg) should set rows, got %+v", um.rows)
		}
	})

	t.Run("usageMsg_updates_error", func(t *testing.T) {
		m := newTestModel(nil, nil)
		testErr := errors.New("test error")
		updated, _ := m.Update(usageMsg{rows: nil, err: testErr})
		um := updated.(*topModel)
		if um.err != testErr {
			t.Errorf("Update(usageMsg) should set err, got %v", um.err)
		}
	})

	t.Run("usageMsg_computes_net_rates", func(t *testing.T) {
		m := newTestModel(nil, nil)
		// First poll: establish baseline.
		m.Update(usageMsg{
			rows: []vmUsageRow{{Name: "vm1", NetRx: 1000, NetTx: 500}},
		})
		// Simulate time passing.
		m.prevTime = time.Now().Add(-5 * time.Second)
		// Second poll: compute rates.
		updated, _ := m.Update(usageMsg{
			rows: []vmUsageRow{{Name: "vm1", NetRx: 6000, NetTx: 3000}},
		})
		um := updated.(*topModel)
		if um.netRxRate == nil {
			t.Fatal("netRxRate should be non-nil after two polls")
		}
		// (6000-1000)/5 = 1000 bytes/sec
		if rate := um.netRxRate["vm1"]; rate < 900 || rate > 1100 {
			t.Errorf("netRxRate[vm1] = %v, want ~1000", rate)
		}
		if rate := um.netTxRate["vm1"]; rate < 400 || rate > 600 {
			t.Errorf("netTxRate[vm1] = %v, want ~500", rate)
		}
	})

	t.Run("key_q_quits", func(t *testing.T) {
		m := newTestModel(nil, nil)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		um := updated.(*topModel)
		if !um.quitting {
			t.Error("Update(KeyMsg 'q') should set quitting")
		}
		if cmd == nil {
			t.Error("Update(KeyMsg 'q') should return tea.Quit cmd")
		}
	})

	t.Run("key_esc_quits", func(t *testing.T) {
		m := newTestModel(nil, nil)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
		um := updated.(*topModel)
		if !um.quitting {
			t.Error("Update(KeyMsg 'esc') should set quitting")
		}
		if cmd == nil {
			t.Error("Update(KeyMsg 'esc') should return tea.Quit cmd")
		}
	})

	t.Run("tick_triggers_fetch", func(t *testing.T) {
		m := newTestModel(nil, nil)
		_, cmd := m.Update(topTickMsg{})
		if cmd == nil {
			t.Error("Update(topTickMsg) should return non-nil cmd batch")
		}
		if m.quitting {
			t.Error("Update(topTickMsg) should not set quitting when within duration")
		}
	})

	t.Run("tick_after_max_duration_quits", func(t *testing.T) {
		m := newTestModel(nil, nil)
		m.startTime = time.Now().Add(-11 * time.Minute)
		updated, cmd := m.Update(topTickMsg{})
		um := updated.(*topModel)
		if !um.quitting {
			t.Error("Update(topTickMsg) after max duration should set quitting")
		}
		if cmd == nil {
			t.Error("Update(topTickMsg) after max duration should return tea.Quit cmd")
		}
	})

	t.Run("window_size_msg", func(t *testing.T) {
		m := newTestModel(nil, nil)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		um := updated.(*topModel)
		if um.width != 120 || um.height != 40 {
			t.Errorf("Update(WindowSizeMsg) should set width/height, got %d/%d", um.width, um.height)
		}
	})
}

// ---------------------------------------------------------------------------
// TestTopModelViewTruncatesLongNames
// ---------------------------------------------------------------------------

func TestTopModelViewTruncatesLongNames(t *testing.T) {
	longName := "this-is-a-very-long-vm-name-that-exceeds-limit"
	rows := []vmUsageRow{
		{
			Name:   longName,
			Status: "running",
		},
	}
	m := newTestModel(rows, nil)
	out := m.View()

	// The name should be truncated to 19 chars + ellipsis.
	truncated := longName[:19] + "\u2026"
	if !strings.Contains(out, truncated) {
		t.Errorf("View() should truncate long name to %q, got %q", truncated, out)
	}
	// The full name should NOT appear.
	if strings.Contains(out, longName) {
		t.Errorf("View() should not contain full long name %q", longName)
	}
}

// ---------------------------------------------------------------------------
// TestTopModelInit
// ---------------------------------------------------------------------------

func TestTopModelInit(t *testing.T) {
	m := newTestModel(nil, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init() should return a non-nil cmd (batch)")
	}
}

// ---------------------------------------------------------------------------
// TestFetchUsageCmdTimeout
// ---------------------------------------------------------------------------

func TestFetchUsageCmdTimeout(t *testing.T) {
	var gotDeadline bool
	var gotCtxDone bool

	slowFetch := func(ctx context.Context) ([]vmUsageRow, error) {
		_, gotDeadline = ctx.Deadline()
		gotCtxDone = ctx.Done() != nil
		return []vmUsageRow{{Name: "test"}}, nil
	}

	cmd := fetchUsageCmd(slowFetch)
	msg := cmd()
	um, ok := msg.(usageMsg)
	if !ok {
		t.Fatalf("fetchUsageCmd returned %T, want usageMsg", msg)
	}

	if !gotDeadline {
		t.Error("fetchUsageCmd should pass a context with a deadline")
	}
	if !gotCtxDone {
		t.Error("fetchUsageCmd should pass a cancellable context")
	}
	if um.err != nil {
		t.Errorf("unexpected error: %v", um.err)
	}
	if len(um.rows) != 1 || um.rows[0].Name != "test" {
		t.Errorf("unexpected rows: %+v", um.rows)
	}
}

func TestFetchUsageCmdReturnsError(t *testing.T) {
	errorFetch := func(ctx context.Context) ([]vmUsageRow, error) {
		return nil, errors.New("exelet unreachable")
	}

	cmd := fetchUsageCmd(errorFetch)
	msg := cmd()
	um, ok := msg.(usageMsg)
	if !ok {
		t.Fatalf("fetchUsageCmd returned %T, want usageMsg", msg)
	}

	if um.err == nil || um.err.Error() != "exelet unreachable" {
		t.Errorf("expected 'exelet unreachable' error, got %v", um.err)
	}
	if um.rows != nil {
		t.Errorf("expected nil rows on error, got %+v", um.rows)
	}
}

// ---------------------------------------------------------------------------
// TestTopViewDiskShowsCapacity
// ---------------------------------------------------------------------------

func TestTopViewDiskShowsCapacity(t *testing.T) {
	rows := []vmUsageRow{{
		Name:             "vm1",
		Status:           "running",
		DiskBytes:        1 * 1024 * 1024 * 1024,
		DiskLogicalBytes: 2 * 1024 * 1024 * 1024,
		DiskCapacity:     20 * 1024 * 1024 * 1024,
	}}
	m := newTestModel(rows, nil)
	out := m.View()
	if !strings.Contains(out, "20.0G") {
		t.Errorf("View() should show allocated disk capacity 20.0G, got %q", out)
	}
	if strings.Contains(out, "/") {
		t.Errorf("View() should not show used/capacity composite, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestTopViewMemSwapColumnsSeparate
// ---------------------------------------------------------------------------

func TestTopViewMemSwapColumnsSeparate(t *testing.T) {
	rows := []vmUsageRow{{
		Name:      "vm1",
		Status:    "running",
		MemBytes:  1024 * 1024 * 1024,     // 1G active
		SwapBytes: 2 * 1024 * 1024 * 1024, // 2G swap
	}}
	m := newTestModel(rows, nil)
	out := m.View()
	// MEM = 1.0G, SWAP = 2.0G — must be reported as distinct values.
	if !strings.Contains(out, "1.0G") {
		t.Errorf("View() should show active memory 1.0G, got %q", out)
	}
	if !strings.Contains(out, "2.0G") {
		t.Errorf("View() should show swap 2.0G, got %q", out)
	}
}

// TestTopSortByCycles verifies the 's' key cycles the sort column
// and that running rows always sort before stopped ones.
func TestTopSortByCycles(t *testing.T) {
	rows := []vmUsageRow{
		{Name: "a", Status: "running", CPUPercent: 10, MemBytes: 100, MemCapacity: 1000, DiskCapacity: 5000, SwapBytes: 0},
		{Name: "b", Status: "running", CPUPercent: 90, MemBytes: 50, MemCapacity: 4000, DiskCapacity: 1000, SwapBytes: 200},
		{Name: "c", Status: "stopped", CPUPercent: 100, MemBytes: 999},
	}
	m := newTestModel(rows, nil)
	m.sortRows()
	if m.rows[0].Name != "b" || m.rows[2].Name != "c" {
		t.Errorf("sort by CPU: want [b a c], got %v", names(m.rows))
	}
	// 's' should advance to MEM.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.sortBy != sortMem {
		t.Errorf("after one 's': sortBy=%v want sortMem", m.sortBy)
	}
	if m.rows[0].Name != "a" {
		t.Errorf("sort by MEM: want a first, got %v", names(m.rows))
	}
	// Cycle through all columns; should end up back at sortCPU.
	for i := 0; i < int(sortColumnCount)-1; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	}
	if m.sortBy != sortCPU {
		t.Errorf("after %d 's' presses: sortBy=%v want sortCPU", sortColumnCount, m.sortBy)
	}
	// Stopped rows must always sort last regardless of column.
	if m.rows[len(m.rows)-1].Name != "c" {
		t.Errorf("stopped row should be last, got %v", names(m.rows))
	}
}

func names(rows []vmUsageRow) []string {
	ns := make([]string, len(rows))
	for i, r := range rows {
		ns[i] = r.Name
	}
	return ns
}

// TestTopViewTruncatesToTerminalHeight verifies that when there are more
// VMs than fit on the terminal, only height-3 rows are shown plus a note.
func TestTopViewTruncatesToTerminalHeight(t *testing.T) {
	var rows []vmUsageRow
	for i := 0; i < 50; i++ {
		rows = append(rows, vmUsageRow{
			Name:   fmt.Sprintf("vm-%02d", i),
			Status: "running",
		})
	}
	m := newTestModel(rows, nil)
	m.height = 10 // expect 10-3 = 7 rows visible
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) > 12 { // header + col header + 7 rows + note + trailing newline
		t.Errorf("View() with height=10 produced %d lines, want <=12; got:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "more not shown") {
		t.Errorf("View() should mention truncation when rows exceed height, got %q", out)
	}
	if !strings.Contains(out, "vm-00") {
		t.Errorf("View() should show first row 'vm-00', got %q", out)
	}
	if strings.Contains(out, "vm-49") {
		t.Errorf("View() should not show last row 'vm-49' when truncated, got %q", out)
	}
}

// TestTopViewMemSubtractsFileCache verifies that the displayed memory
// excludes host page cache attributed to the VM (memory.stat "file"),
// since that cache is reclaimable and not part of the guest working set.
func TestTopViewMemSubtractsFileCache(t *testing.T) {
	rows := []vmUsageRow{{
		Name:         "vm1",
		Status:       "running",
		MemBytes:     3 * 1024 * 1024 * 1024, // 3G memory.current
		MemFileBytes: 2 * 1024 * 1024 * 1024, // 2G page cache (reclaimable)
	}}
	m := newTestModel(rows, nil)
	out := m.View()
	// Should show 1.0G (memory.current - file cache).
	if !strings.Contains(out, "1.0G") {
		t.Errorf("View() should subtract file cache and show 1.0G, got %q", out)
	}
	if strings.Contains(out, "3.0G") {
		t.Errorf("View() should not show raw memory.current (3.0G), got %q", out)
	}
}

func TestDisplayMemBytesSaturating(t *testing.T) {
	// File cache larger than memory.current (can happen briefly with
	// charge migration / accounting skew) — saturate at zero.
	r := vmUsageRow{MemBytes: 1024, MemFileBytes: 4096}
	if got := r.DisplayMemBytes(); got != 0 {
		t.Errorf("DisplayMemBytes: got %d, want 0", got)
	}
	// Old exelet (no breakdown) — fall back to MemBytes.
	r = vmUsageRow{MemBytes: 4096, MemFileBytes: 0}
	if got := r.DisplayMemBytes(); got != 4096 {
		t.Errorf("DisplayMemBytes (no breakdown): got %d, want 4096", got)
	}
}
