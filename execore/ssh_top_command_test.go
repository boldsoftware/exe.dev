package execore

import (
	"context"
	"errors"
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
				Name:       "my-vm",
				Status:     "running",
				CPUPercent: 45.2,
				MemBytes:   1024 * 1024 * 1024, // 1G
				DiskBytes:  500 * 1024 * 1024,  // 500M
				NetRx:      2048,
				NetTx:      4096,
			},
		}
		m := newTestModel(rows, nil)
		out := m.View()

		if !strings.Contains(out, "VM") || !strings.Contains(out, "STATUS") || !strings.Contains(out, "CPU%") {
			t.Errorf("View() should contain column headers, got %q", out)
		}
		if !strings.Contains(out, "my-vm") {
			t.Errorf("View() should contain VM name 'my-vm', got %q", out)
		}
		if !strings.Contains(out, "1.0G") {
			t.Errorf("View() should contain memory '1.0G', got %q", out)
		}
		if !strings.Contains(out, "500M") {
			t.Errorf("View() should contain disk '500M', got %q", out)
		}
		if !strings.Contains(out, "2K") {
			t.Errorf("View() should contain net rx '2K', got %q", out)
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
	truncated := longName[:19] + "…"
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

// TestFetchUsageCmdTimeout verifies that fetchUsageCmd creates a context with
// a timeout and that a slow fetch function gets canceled.
func TestFetchUsageCmdTimeout(t *testing.T) {
	// fetchUsageCmd creates a 10s timeout internally. We can't easily test
	// the exact 10s, but we CAN verify the context it passes is not
	// context.Background() (i.e., it has a deadline).
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

// TestFetchUsageCmdReturnsError verifies that when the fetch function returns
// an error, it's propagated through the usageMsg.
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
