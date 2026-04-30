package resourcemanager

import "testing"

func TestMemwatchDisabledByEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"anything-else", true},
	}
	for _, c := range cases {
		got := memwatchDisabledByEnv(func(string) string { return c.val })
		if got != c.want {
			t.Errorf("memwatchDisabledByEnv(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestRuntimeDataDirFromAddress(t *testing.T) {
	if got := runtimeDataDirFromAddress("cloudhypervisor:///var/tmp/exelet/runtime"); got != "/var/tmp/exelet/runtime" {
		t.Errorf("got %q", got)
	}
	if got := runtimeDataDirFromAddress(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestMemdSocketPath(t *testing.T) {
	if got := memdSocketPath("/var/tmp/runtime", "abc"); got != "/var/tmp/runtime/abc/opssh.sock" {
		t.Errorf("got %q", got)
	}
	if got := memdSocketPath("", "abc"); got != "" {
		t.Errorf("got %q", got)
	}
}
