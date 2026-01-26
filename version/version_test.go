package version

import (
	"strings"
	"testing"
)

func TestFullVersion(t *testing.T) {
	v := FullVersion()
	if !strings.HasPrefix(v, Name+"/") {
		t.Errorf("FullVersion() = %q, want prefix %q", v, Name+"/")
	}
}

func TestBuildVersion(t *testing.T) {
	v := BuildVersion()
	if v == "" {
		t.Errorf("BuildVersion() is empty")
	}
}
