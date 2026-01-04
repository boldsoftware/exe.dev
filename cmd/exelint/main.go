// Command exelint runs all bespoke exe linters.
// This is faster than serial execution because it re-uses typechecking state.
package main

import (
	"exe.dev/completeinit"
	"exe.dev/tracing/linter"
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(linter.Analyzer, completeinit.Analyzer)
}
