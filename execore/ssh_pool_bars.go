package execore

import (
	"fmt"
	"math"
	"strings"
)

const barWidth = 30

// poolBar renders an ASCII usage bar: [████████░░░░░░░░░░░░] 2.0 / 4 cores
// Color: green < 70%, yellow 70-90%, red >= 90%.
func poolBar(used, max float64, suffix string) string {
	if max == 0 {
		return ""
	}
	pct := used / max
	if pct > 1 {
		pct = 1
	}
	filled := int(math.Round(pct * barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	var color string
	switch {
	case pct >= 0.9:
		color = "\033[31m" // red
	case pct >= 0.7:
		color = "\033[33m" // yellow
	default:
		color = "\033[32m" // green
	}

	bar := color + strings.Repeat("█", filled) + "\033[0m" +
		"\033[2m" + strings.Repeat("░", barWidth-filled) + "\033[0m"

	return fmt.Sprintf("%s %s", bar, suffix)
}

// poolBarBytes is poolBar for byte values, formatting as GB/MB.
func poolBarBytes(used, max uint64, label string) string {
	if max == 0 {
		return ""
	}
	suffix := fmt.Sprintf("%s / %s %s", fmtBytes(clampU64(used, max)), fmtBytes(max), label)
	return poolBar(float64(used), float64(max), suffix)
}

func clampU64(v, max uint64) uint64 {
	if v > max {
		return max
	}
	return v
}

func clampF64(v, max float64) float64 {
	if v > max {
		return max
	}
	return v
}
