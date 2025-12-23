package testinfra

import "sync"

// cleanups holds a list of functions to run on exit.
// These are invoked by calling RunCleanups.
var cleanups []func()

// cleanupsMu is a lock for cleanups.
var cleanupsMu sync.Mutex

// AddCleanup adds a function to the list of cleanups that are
// run when the program exits. This is like testing.T.Cleanup but
// for use from TestMain or for something shared by multiple tests.
// The TestMain function is responsible for calling RunCleanups.
// The cleanups are run in reverse order of registering them.
func AddCleanup(fn func()) {
	cleanupsMu.Lock()
	defer cleanupsMu.Unlock()

	cleanups = append(cleanups, fn)
}

// RunCleanups run all the cleanups.
// This should be called on program exit.
func RunCleanups() {
	cleanupsMu.Lock()

	run := cleanups
	// Don't run the cleanups again.
	cleanups = nil

	cleanupsMu.Unlock()

	// Run the cleanups in reverse order.
	for i := len(run) - 1; i >= 0; i-- {
		run[i]()
	}
}
