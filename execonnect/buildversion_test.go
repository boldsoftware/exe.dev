package execonnect

import "testing"

func TestBuildRevision(t *testing.T) {
	if got := BuildRevision(); got == "" {
		t.Fatal("BuildRevision returned an empty value")
	}
}
