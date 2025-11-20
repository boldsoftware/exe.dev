package ctrhosttest

import (
	"flag"
	"os"
	"strings"
)

const (
	defaultHost         = "ssh://lima-exe-ctr.local"
	defaultHostForTests = "ssh://lima-exe-ctr-tests.local"
)

// Detect returns a usable CTR_HOST value for tests and local dev.
// The CTR_HOST env var wins, but otherwise it will try lima-exe-ctr{,-tests}
// (depening if its being called from a unit test or not).
func Detect() string {
	if v := strings.TrimSpace(os.Getenv("CTR_HOST")); v != "" {
		return v
	}
	if flag.Lookup("test.v") != nil {
		return defaultHostForTests
	} else {
		return defaultHost
	}
}
