package execore

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteQRCode(t *testing.T) {
	var buf bytes.Buffer
	writeQRCode(&buf, "https://example.com")

	output := buf.String()

	// QR codes use half-block characters (▀, ▄, █) for compact terminal output
	// Verify the output contains these characters
	if !strings.ContainsAny(output, "▀▄█") {
		t.Errorf("expected QR code output to contain block characters, got: %q", output)
	}

	// QR codes should have multiple lines
	lines := strings.Split(output, "\n")
	if len(lines) < 5 {
		t.Errorf("expected QR code to have at least 5 lines, got %d", len(lines))
	}
}
