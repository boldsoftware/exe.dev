package desiredstate

import (
	"fmt"
	"strings"
)

// ParseOverrides parses the cgroup_overrides text column format.
// Format is newline-separated "path:value" pairs, e.g.:
//
//	cpu.max:10000 100000
//	memory.high:1073741824
//
// An empty string returns nil.
func ParseOverrides(s string) []CgroupSetting {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []CgroupSetting
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out = append(out, CgroupSetting{Path: path, Value: value})
	}
	return out
}

// FormatOverrides serializes cgroup settings to the DB text column format.
// Returns an empty string if there are no settings.
func FormatOverrides(settings []CgroupSetting) string {
	if len(settings) == 0 {
		return ""
	}
	var lines []string
	for _, s := range settings {
		lines = append(lines, s.Path+":"+s.Value)
	}
	return strings.Join(lines, "\n")
}

// MergeOverrides merges new settings into existing ones.
// If a new setting has an empty value, it removes that path.
// Otherwise it replaces or appends.
func MergeOverrides(existing, updates []CgroupSetting) []CgroupSetting {
	// Build ordered map from existing
	type entry struct {
		path  string
		value string
	}
	var entries []entry
	seen := make(map[string]int) // path -> index
	for _, s := range existing {
		seen[s.Path] = len(entries)
		entries = append(entries, entry{s.Path, s.Value})
	}
	for _, s := range updates {
		if idx, ok := seen[s.Path]; ok {
			if s.Value == "" {
				// Mark for removal
				entries[idx].value = ""
				entries[idx].path = ""
			} else {
				entries[idx].value = s.Value
			}
		} else if s.Value != "" {
			entries = append(entries, entry{s.Path, s.Value})
		}
	}
	var out []CgroupSetting
	for _, e := range entries {
		if e.path != "" {
			out = append(out, CgroupSetting{Path: e.path, Value: e.value})
		}
	}
	return out
}

// ApplyOverrides returns the final cgroup settings for a VM or group,
// starting from base settings and applying overrides on top.
// Override entries with empty values remove the corresponding base entry.
func ApplyOverrides(base, overrides []CgroupSetting) []CgroupSetting {
	return MergeOverrides(base, overrides)
}

// CPUFractionToMax converts a fractional CPU value (e.g. 0.1 for 10% of 1 CPU)
// to a cpu.max value string.
// The value represents a fraction of one CPU core:
//   - 0.1 = 10% of 1 CPU core
//   - 1.0 = 100% of 1 CPU core
//   - 2.5 = 250% (2.5 CPU cores)
func CPUFractionToMax(fraction float64) string {
	const period = 100000 // 100ms in microseconds
	quota := int64(fraction * float64(period))
	if quota < 1 {
		quota = 1
	}
	return fmt.Sprintf("%d %d", quota, period)
}
