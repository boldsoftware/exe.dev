package termfun

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"exe.dev/ctxio"
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

func QueryBackgroundColor(w io.Writer, cr *ctxio.Reader) RGB {
	fmt.Fprint(w, "\x1b]11;?\x07")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	buf := new(strings.Builder)
	var resp string
	for buf.Len() < 1024 {
		b, err := cr.ReadByteContext(ctx)
		if err != nil {
			return RGB{0, 0, 0}
		}
		buf.WriteByte(b)
		// Check for bell/ST terminators.
		if s, ok := strings.CutSuffix(buf.String(), "\x07"); ok {
			resp = s
			break
		}
		if s, ok := strings.CutSuffix(buf.String(), "\x1b\\"); ok {
			resp = s
			break
		}
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
