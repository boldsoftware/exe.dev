//reservevms:ok — file-level directive skips all tests.
package c

import "testing"

func reserveVMs(t *testing.T, n int) {
	t.Helper()
}

func TestSkippedByFileDirective(t *testing.T) {
}

func TestAlsoSkipped(t *testing.T) {
}
