package sqlite

import (
	"time"
)

// RFC3339Micro is the timestamp format used for SQLite storage.
// Uses microsecond precision (6 decimal places) instead of nanosecond
// to match SQLite's datetime precision while remaining lexicographically sortable.
const RFC3339Micro = "2006-01-02T15:04:05.999999Z07:00"

// FormatTime formats a time.Time as RFC3339 with microsecond precision.
// Returns a string that can be stored in SQLite DATETIME columns with consistent
// lexicographic sorting. Always converts to UTC and strips monotonic clock readings.
func FormatTime(t time.Time) string {
	return t.UTC().Round(time.Microsecond).Format(time.RFC3339Nano)
}
