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

func lerp(a, b float64, t float64) float64 {
	return a + (b-a)*t
}

func QueryBackgroundColor(w io.Writer, r io.Reader) RGB {
	fmt.Fprint(w, "\x1b]11;?\x07")

	buf := make([]byte, 256)
	done := make(chan RGB, 1)

	go func() {
		n, err := r.Read(buf)
		if err != nil || n == 0 {
			done <- RGB{0, 0, 0}
			return
		}

		response := string(buf[:n])

		if strings.Contains(response, "rgb:") {
			parts := strings.Split(response, "rgb:")
			if len(parts) >= 2 {
				colorPart := strings.TrimSuffix(parts[1], "\x07")
				colorPart = strings.TrimSuffix(colorPart, "\x1b\\")

				components := strings.Split(colorPart, "/")
				if len(components) == 3 {
					r := parseColorComponent(components[0])
					g := parseColorComponent(components[1])
					b := parseColorComponent(components[2])
					done <- RGB{r, g, b}
					return
				}
			}
		}
		done <- RGB{0, 0, 0}
	}()

	select {
	case rgb := <-done:
		return rgb
	case <-time.After(100 * time.Millisecond):
		return RGB{0, 0, 0}
	}
}

func parseColorComponent(s string) float64 {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0
	}

	if len(s) <= 2 {
		val, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return 0
		}
		return float64(val)
	}

	if len(s) == 4 {
		val, err := strconv.ParseUint(s[:2], 16, 8)
		if err != nil {
			return 0
		}
		return float64(val)
	}

	return 0
}

func FadeTextInPlace(w io.Writer, lines []string, leftPadding int, from, to RGB, dur time.Duration, frames int) {
	al := RGB{srgbToLinear(from.R), srgbToLinear(from.G), srgbToLinear(from.B)}
	bl := RGB{srgbToLinear(to.R), srgbToLinear(to.G), srgbToLinear(to.B)}

	tick := time.NewTicker(dur / time.Duration(frames))
	defer tick.Stop()

	padding := strings.Repeat(" ", leftPadding)

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
			// Clear line, add padding, then colored text
			fmt.Fprintf(w, "\r\x1b[2K%s\x1b[38;2;%d;%d;%dm%s\x1b[0m", padding, r, g, b, line)
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
