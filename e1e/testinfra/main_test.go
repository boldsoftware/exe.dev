package testinfra

import (
	"os"
	"testing"
)

// TestMain ensures that we run cleanups either when exiting
// or when panicking in the main goroutine.
func TestMain(m *testing.M) {
	defer RunCleanups()
	exitCode := m.Run()
	RunCleanups()
	os.Exit(exitCode)
}
