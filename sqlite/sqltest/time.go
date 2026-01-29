// Package sqltest registers a replacement for SQLite's built-in time functions
// CURRENT_TIMESTAMP, CURRENT_DATE, and CURRENT_TIME that uses Go's [time.Now]
// so they are in sync with synctest bubble controlled time progression.
//
// To enable this behavior import this package for its side effects.
//
// Example:
//
//	import _ "exe.dev/sqlite/sqltest"
package sqltest

import (
	"database/sql/driver"
	"time"

	sqlite "modernc.org/sqlite"
)

func init() {
	// Make builtin time functions use [time.Now].
	register := func(name, format string) {
		sqlite.MustRegisterScalarFunction(name, 0, func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return time.Now().UTC().Round(0).Format(format), nil
		})
	}
	register("CURRENT_TIMESTAMP", "2006-01-02 15:04:05")
	register("CURRENT_DATE", "2006-01-02")
	register("CURRENT_TIME", "15:04:05")
}
