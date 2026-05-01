//go:build topdemo

// Demo / manual test program for the `top` Bubble Tea UI. Builds only
// with `-tags topdemo` so it doesn't pollute the normal build graph.
//
// Usage:
//
//	go build -tags topdemo -o /tmp/topdemo ./cmd/topdemo
//	/tmp/topdemo 30   # 30 fake VMs
//
// This is a regular Bubble Tea program: arrow keys / j / k / PgUp /
// PgDn / g / G to scroll, s to cycle sort, q to quit.
package main

import (
	"fmt"
	"math/rand"
	"os"

	"exe.dev/execore"
)

func main() {
	n := 25
	if len(os.Args) > 1 {
		_, _ = fmt.Sscanf(os.Args[1], "%d", &n)
	}
	if err := execore.RunTopDemo(n, rand.New(rand.NewSource(1))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
