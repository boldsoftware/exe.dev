package execore

import "fmt"

// fmtBytes formats bytes using 1024-based units with GB/MB/KB labels.
// This matches user expectations ("GB" not "GiB") while using binary math.
func fmtBytes(b uint64) string {
	switch {
	case b >= 1024*1024*1024:
		v := float64(b) / (1024 * 1024 * 1024)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d GB", int64(v))
		}
		return fmt.Sprintf("%.1f GB", v)
	case b >= 1024*1024:
		v := float64(b) / (1024 * 1024)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d MB", int64(v))
		}
		return fmt.Sprintf("%.1f MB", v)
	case b >= 1024:
		v := float64(b) / 1024
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d KB", int64(v))
		}
		return fmt.Sprintf("%.1f KB", v)
	case b == 0:
		return "0 B"
	default:
		return fmt.Sprintf("%d B", b)
	}
}
