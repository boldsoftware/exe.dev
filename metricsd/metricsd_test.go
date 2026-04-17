package metricsd

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Force timezone to UTC so that tests that rely on
	// time adjustments are consistent.
	os.Setenv("TZ", "UTC")
	m.Run()
}
