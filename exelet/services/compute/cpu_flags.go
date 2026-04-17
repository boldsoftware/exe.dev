package compute

import (
	"os"
	"sort"
	"strings"
)

// readCPUFlags reads CPU feature flags from /proc/cpuinfo.
// Returns a sorted, deduplicated list of flag names (e.g., "avx2", "sse4_2").
func readCPUFlags() []string {
	return parseCPUFlags("/proc/cpuinfo")
}

// parseCPUFlags parses CPU flags from the given cpuinfo file path.
func parseCPUFlags(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		// /proc/cpuinfo format: "flags\t\t: flag1 flag2 flag3 ..."
		if !strings.HasPrefix(line, "flags") {
			continue
		}
		_, after, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		for _, flag := range strings.Fields(after) {
			seen[flag] = struct{}{}
		}
		break // All cores report the same flags; one line is enough.
	}

	flags := make([]string, 0, len(seen))
	for f := range seen {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	return flags
}
