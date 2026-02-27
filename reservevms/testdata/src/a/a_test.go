package a

import "testing"

func reserveVMs(t *testing.T, n int) {
	t.Helper()
}

func TestGood(t *testing.T) {
	reserveVMs(t, 1)
}

func TestGoodZero(t *testing.T) {
	reserveVMs(t, 0)
}

func TestBad(t *testing.T) { // want "TestBad does not call reserveVMs"
}

func helperWithReserve(t *testing.T) {
	reserveVMs(t, 1)
}

func TestGoodViaHelper(t *testing.T) {
	helperWithReserve(t)
}

func helperWithoutReserve(t *testing.T) {
}

func TestBadViaHelper(t *testing.T) { // want "TestBadViaHelper does not call reserveVMs"
	helperWithoutReserve(t)
}

// Not a test function (wrong signature), should not be flagged.
func NotATestFunc(t *testing.T) {
}

//reservevms:ok — tests pool infrastructure, not VMs.
func TestSkippedByDirective(t *testing.T) {
}
