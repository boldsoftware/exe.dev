package termfun

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSrgbConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		wantBack float64
		epsilon  float64
	}{
		{"black", 0, 0, 0.01},
		{"white", 255, 255, 0.01},
		{"mid-gray", 128, 128, 0.5},
		{"dark", 50, 50, 0.5},
		{"bright", 200, 200, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			linear := srgbToLinear(tt.input)
			back := linearToSrgb(linear)

			if math.Abs(back-tt.wantBack) > tt.epsilon {
				t.Errorf("srgbToLinear(%v) -> linearToSrgb() = %v, want %v (±%v)",
					tt.input, back, tt.wantBack, tt.epsilon)
			}
		})
	}
}

func TestLerp(t *testing.T) {
	tests := []struct {
		a, b, t, want float64
	}{
		{0, 100, 0, 0},
		{0, 100, 1, 100},
		{0, 100, 0.5, 50},
		{10, 20, 0.25, 12.5},
		{100, 0, 0.5, 50},
	}

	for _, tt := range tests {
		got := lerp(tt.a, tt.b, tt.t)
		if math.Abs(got-tt.want) > 0.001 {
			t.Errorf("lerp(%v, %v, %v) = %v, want %v", tt.a, tt.b, tt.t, got, tt.want)
		}
	}
}

func TestFadeTextInPlace(t *testing.T) {
	var buf bytes.Buffer

	lines := []string{"TEST", "LINE2"}
	from := RGB{255, 0, 0}
	to := RGB{0, 0, 255}

	FadeTextInPlace(&buf, lines, 0, from, to, 100*time.Millisecond, 5)

	output := buf.String()

	if !strings.Contains(output, "TEST") {
		t.Error("Text not found in output")
	}

	if !strings.Contains(output, "LINE2") {
		t.Error("Second line not found in output")
	}

	if !strings.Contains(output, "\x1b[38;2;") {
		t.Error("No 24-bit color sequences found")
	}

	if !strings.Contains(output, "\x1b[0m") {
		t.Error("Missing reset sequence")
	}
}

func TestParseColorComponent(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"00", 0},
		{"ff", 255},
		{"FF", 255},
		{"80", 128},
		{"0000", 0},
		{"ffff", 255},
		{"8080", 128},
		{"", 0},
		{"xyz", 0},
	}

	for _, tt := range tests {
		got := parseColorComponent(tt.input)
		if got != tt.want {
			t.Errorf("parseColorComponent(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestQueryBackgroundColor(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     RGB
	}{
		{
			name:     "valid response",
			response: "\x1b]11;rgb:0000/0000/0000\x07",
			want:     RGB{0, 0, 0},
		},
		{
			name:     "white background",
			response: "\x1b]11;rgb:ffff/ffff/ffff\x07",
			want:     RGB{255, 255, 255},
		},
		{
			name:     "custom color",
			response: "\x1b]11;rgb:8080/4040/2020\x07",
			want:     RGB{128, 64, 32},
		},
		{
			name:     "no response",
			response: "",
			want:     RGB{0, 0, 0},
		},
		{
			name:     "invalid response",
			response: "invalid",
			want:     RGB{0, 0, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w bytes.Buffer
			r := strings.NewReader(tt.response)

			got := QueryBackgroundColor(&w, r)

			if got != tt.want {
				t.Errorf("QueryBackgroundColor() = %v, want %v", got, tt.want)
			}

			if !strings.Contains(w.String(), "\x1b]11;?\x07") {
				t.Error("Query sequence not sent")
			}
		})
	}
}

func TestFadeTextInPlaceFrames(t *testing.T) {
	var buf bytes.Buffer

	lines := []string{"FADE"}
	from := RGB{255, 255, 255}
	to := RGB{0, 0, 0}
	frames := 3

	FadeTextInPlace(&buf, lines, 0, from, to, 30*time.Millisecond, frames)

	output := buf.String()

	colorCount := strings.Count(output, "\x1b[38;2;")
	if colorCount != frames+1 {
		t.Errorf("Expected %d color changes (frames + 1), got %d", frames+1, colorCount)
	}
}

func TestColorInterpolation(t *testing.T) {
	from := RGB{255, 0, 0}
	to := RGB{0, 255, 0}

	fromLinear := RGB{
		srgbToLinear(from.R),
		srgbToLinear(from.G),
		srgbToLinear(from.B),
	}
	toLinear := RGB{
		srgbToLinear(to.R),
		srgbToLinear(to.G),
		srgbToLinear(to.B),
	}

	mid := RGB{
		lerp(fromLinear.R, toLinear.R, 0.5),
		lerp(fromLinear.G, toLinear.G, 0.5),
		lerp(fromLinear.B, toLinear.B, 0.5),
	}

	midSrgb := RGB{
		linearToSrgb(mid.R),
		linearToSrgb(mid.G),
		linearToSrgb(mid.B),
	}

	if midSrgb.R < 50 || midSrgb.R > 200 {
		t.Errorf("Mid-point red out of expected range: %v", midSrgb.R)
	}
	if midSrgb.G < 50 || midSrgb.G > 200 {
		t.Errorf("Mid-point green out of expected range: %v", midSrgb.G)
	}
	if midSrgb.B > 50 {
		t.Errorf("Mid-point blue should be near 0: %v", midSrgb.B)
	}
}

func BenchmarkFadeTextInPlace(b *testing.B) {
	var buf bytes.Buffer
	lines := []string{"BENCHMARK"}
	from := RGB{255, 0, 0}
	to := RGB{0, 0, 255}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		FadeTextInPlace(&buf, lines, 0, from, to, 10*time.Millisecond, 2)
	}
}

func ExampleFadeTextInPlace() {
	var buf bytes.Buffer

	lines := []string{"Example"}
	from := RGB{80, 255, 120}
	to := RGB{0, 0, 0}

	FadeTextInPlace(&buf, lines, 0, from, to, 50*time.Millisecond, 2)

	output := buf.String()
	if strings.Contains(output, "Example") && strings.Contains(output, "\x1b[38;2;") {
		fmt.Println("Fade animation generated successfully")
	}
}
