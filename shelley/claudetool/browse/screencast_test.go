package browse

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestScreencastStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Verify no screencast is running.
	active, _, _, _ := tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast")
	}

	// Start via combined tool.
	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast recording") {
		if strings.Contains(text, "failed to start browser") || strings.Contains(text, "ffmpeg") {
			t.Skip("Browser or ffmpeg not available")
		}
		t.Fatalf("unexpected start result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 in start message, got: %s", text)
	}
	t.Logf("Start result: %s", text)

	// Double-start should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "already active") {
		t.Fatalf("expected already-active error, got: %s", text)
	}

	// Navigate to generate some frames.
	out = tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"data:text/html,<h1>Screencast Test</h1>"}`))
	text = contentText(t, out)
	if strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}

	// Poll until we have at least one frame.
	var sessionID string
	var frameCount int
	var elapsed time.Duration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active bool
		active, sessionID, frameCount, elapsed = tools.screencastStatus()
		if active && frameCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frameCount == 0 {
		t.Fatal("expected at least one screencast frame")
	}
	t.Logf("Status: session=%s frames=%d elapsed=%v", sessionID, frameCount, elapsed)

	// Stop via combined tool.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "Screencast stopped") {
		t.Fatalf("unexpected stop result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 path in stop message, got: %s", text)
	}
	t.Logf("Stop result: %s", text)

	// Verify MP4 file exists on disk.
	mp4Path := ScreencastDir + "/" + sessionID + ".mp4"
	info, err := os.Stat(mp4Path)
	if err != nil {
		t.Fatalf("MP4 file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("MP4 file is empty")
	}
	t.Logf("MP4 file: %s (%d bytes)", mp4Path, info.Size())

	// Double-stop should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "no active screencast") {
		t.Fatalf("expected no-active error, got: %s", text)
	}

	// Status should show inactive.
	active, _, _, _ = tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast after stop")
	}
}

func TestScreencastLimitsAreReasonable(t *testing.T) {
	if ScreencastMaxFrames < 1000 {
		t.Fatalf("ScreencastMaxFrames too low: %d", ScreencastMaxFrames)
	}
	if ScreencastMaxDuration < 10*time.Minute {
		t.Fatalf("ScreencastMaxDuration too low: %v", ScreencastMaxDuration)
	}
}

func TestScreencastStatusWhenInactive(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_status"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "No active screencast") {
		t.Fatalf("expected no-active message, got: %s", text)
	}
}

func TestScreencastSchemaIncludes(t *testing.T) {
	tools := NewBrowseTools(context.Background(), 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	for _, action := range []string{"screencast_start", "screencast_stop", "screencast_status"} {
		found := false
		for _, a := range schema.Properties["action"].Enum {
			if a == action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("action %q not in schema enum", action)
		}
	}

	for _, prop := range []string{"format", "quality", "max_width", "max_height", "every_nth_frame"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Errorf("expected property %q in schema", prop)
		}
	}
}

// contentText extracts the text from a tool output, including errors.
func contentText(t *testing.T, out llm.ToolOut) string {
	t.Helper()
	if out.Error != nil {
		return out.Error.Error()
	}
	var parts []string
	for _, c := range out.LLMContent {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
