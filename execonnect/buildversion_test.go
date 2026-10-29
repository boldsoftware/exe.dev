package execonnect

import "testing"

func TestBuildVersion(t *testing.T) {
	if got, want := BuildVersion(), BuildRevision(); got != want {
		t.Fatalf("unreleased BuildVersion() = %q, want %q", got, want)
	}
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v0.8.9123"
	if got, want := BuildVersion(), "v0.8.9123 ("+BuildRevision()+")"; got != want {
		t.Fatalf("released BuildVersion() = %q, want %q", got, want)
	}
}
