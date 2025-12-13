package termfun

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

type RGB struct {
	R, G, B float64
}

func srgbToLinear(c float64) float64 {
	c /= 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92 * 255
	}
	return (1.055*math.Pow(c, 1.0/2.4) - 0.055) * 255
}

func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// QueryBackgroundColor queries the terminal for its background color using
// OSC 11. If the terminal doesn't respond within the timeout, or doesn't
// support OSC 11, it returns black (RGB{0, 0, 0}).
//
// Note: This function should NOT be used on live SSH sessions because
// the timeout handling can cause issues. On timeout, an orphan goroutine
// remains blocked on Read() which can steal user input.
func QueryBackgroundColor(rwc io.ReadWriteCloser) RGB {
	fmt.Fprint(rwc, "\x1b]11;?\x07")

	ch := make(chan string, 1)
	go func() {
		var b strings.Builder
		var c [1]byte

		for b.Len() < 1024 {
			_, err := rwc.Read(c[:])
			if err != nil {
				ch <- ""
				return
			}
			b.WriteByte(c[0])

			// Check for bell/ST terminators.
			if s, ok := strings.CutSuffix(b.String(), "\x07"); ok {
				ch <- s
				return
			}
			if s, ok := strings.CutSuffix(b.String(), "\x1b\\"); ok {
				ch <- s
				return
			}
		}

		ch <- b.String()
	}()

	var resp string
	select {
	case resp = <-ch:
	case <-time.After(150 * time.Millisecond):
		// Timeout - terminal doesn't support OSC 11.
		// Don't close the connection - caller owns it.
		resp = ""
	}

	if resp == "" {
		return RGB{0, 0, 0}
	}

	resp = strings.TrimPrefix(resp, "\x1b]11;")
	resp = strings.TrimSpace(resp)
	colorPart, ok := strings.CutPrefix(resp, "rgb:")
	if !ok {
		return RGB{0, 0, 0}
	}

	components := strings.Split(colorPart, "/")
	if len(components) != 3 {
		return RGB{0, 0, 0}
	}

	red := parseColorComponent(components[0])
	green := parseColorComponent(components[1])
	blue := parseColorComponent(components[2])

	return RGB{red, green, blue}
}

func parseColorComponent(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	// Response length indicates precision: 2 = 8-bit, 4 = 16-bit.
	// We need to scale accordingly.
	switch {
	case len(s) <= 2:
		val, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return 0
		}
		return float64(val)
	case len(s) <= 4:
		val, err := strconv.ParseUint(s, 16, 16)
		if err != nil {
			return 0
		}
		return math.Round(float64(val) / 65535 * 255)
	default:
		return 0
	}
}

func FadeTextInPlace(w io.Writer, lines []string, from, to RGB, dur time.Duration, frames int) {
	al := RGB{srgbToLinear(from.R), srgbToLinear(from.G), srgbToLinear(from.B)}
	bl := RGB{srgbToLinear(to.R), srgbToLinear(to.G), srgbToLinear(to.B)}

	tick := time.NewTicker(dur / time.Duration(frames))
	defer tick.Stop()

	for i := 0; i <= frames; i++ {
		t := float64(i) / float64(frames)

		// Smooth easing
		t = t * t * (3.0 - 2.0*t)

		rl := lerp(al.R, bl.R, t)
		gl := lerp(al.G, bl.G, t)
		blin := lerp(al.B, bl.B, t)

		r := int(math.Round(math.Max(0, math.Min(255, linearToSrgb(rl)))))
		g := int(math.Round(math.Max(0, math.Min(255, linearToSrgb(gl)))))
		b := int(math.Round(math.Max(0, math.Min(255, linearToSrgb(blin)))))

		// For all frames, redraw from the current position
		// The cursor is already at the start of the art area
		for j, line := range lines {
			// Clear line, then colored text
			fmt.Fprintf(w, "\r\x1b[2K\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, line)
			if j < len(lines)-1 {
				fmt.Fprint(w, "\r\n")
			}
		}

		// After drawing all lines, move cursor back to start for next frame
		// (except on the last frame)
		if i < frames {
			fmt.Fprintf(w, "\x1b[%dA", len(lines)-1)
			fmt.Fprint(w, "\r")
		}

		// Wait before next frame
		if i != frames {
			<-tick.C
		}
	}
}
