package resourcemanager

import (
	"testing"

	"exe.dev/exelet/config"
)

func testConfigWithStorage(addr string) *config.ExeletConfig {
	return &config.ExeletConfig{StorageManagerAddress: addr}
}

func TestParseIOStat(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRead  uint64
		wantWrite uint64
	}{
		{
			name:      "single device",
			input:     "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=0 dios=0\n",
			wantRead:  1000,
			wantWrite: 2000,
		},
		{
			name:      "multiple devices summed",
			input:     "254:0 rbytes=1000 wbytes=2000 rios=10 wios=20 dbytes=0 dios=0\n230:16 rbytes=500 wbytes=300 rios=5 wios=3 dbytes=0 dios=0\n",
			wantRead:  1500,
			wantWrite: 2300,
		},
		{
			name:      "empty lines ignored",
			input:     "7:0 \n254:0 rbytes=100 wbytes=200 rios=1 wios=2 dbytes=0 dios=0\n",
			wantRead:  100,
			wantWrite: 200,
		},
		{
			name:      "empty file",
			input:     "",
			wantRead:  0,
			wantWrite: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRead, gotWrite, err := parseIOStat([]byte(tt.input))
			if err != nil {
				t.Fatalf("parseIOStat() error = %v", err)
			}
			if gotRead != tt.wantRead {
				t.Errorf("readBytes = %d, want %d", gotRead, tt.wantRead)
			}
			if gotWrite != tt.wantWrite {
				t.Errorf("writeBytes = %d, want %d", gotWrite, tt.wantWrite)
			}
		})
	}
}

func TestZvolDevicePath(t *testing.T) {
	tests := []struct {
		name string
		addr string
		id   string
		want string
	}{
		{"empty", "", "vm-1", ""},
		{"non-zfs", "file:///foo", "vm-1", ""},
		{"zfs no dataset", "zfs:///var/lib/exelet", "vm-1", ""},
		{"zfs ok", "zfs:///var/lib/exelet?dataset=tank", "vm-1", "/dev/zvol/tank/vm-1"},
		{"zfs nested dataset", "zfs:///var/lib/exelet?dataset=tank/instances", "abc", "/dev/zvol/tank/instances/abc"},
		{"unsafe id traversal", "zfs:///var/lib/exelet?dataset=tank", "../etc", ""},
		{"unsafe id slash", "zfs:///var/lib/exelet?dataset=tank", "a/b", ""},
		{"unsafe id dot", "zfs:///var/lib/exelet?dataset=tank", ".", ""},
		{"unsafe id empty", "zfs:///var/lib/exelet?dataset=tank", "", ""},
		{"unsafe id null", "zfs:///var/lib/exelet?dataset=tank", "a\x00b", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &ResourceManager{config: testConfigWithStorage(tc.addr)}
			if got := m.zvolDevicePath(tc.id); got != tc.want {
				t.Errorf("zvolDevicePath(%q,%q) = %q, want %q", tc.addr, tc.id, got, tc.want)
			}
		})
	}
}

func TestExt4UsageAllowed(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		allow   []string
		group   string
		want    bool
	}{
		{"disabled, no allow", false, nil, "usrFOO", false},
		{"enabled", true, nil, "usrFOO", true},
		{"allow-listed", false, []string{"usrFOO"}, "usrFOO", true},
		{"allow-listed but other group", false, []string{"usrFOO"}, "usrBAR", false},
		{"empty group never matches", false, []string{""}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.ExeletConfig{
				CollectExt4Usage:         tc.enabled,
				CollectExt4UsageGroupIDs: tc.allow,
			}
			allow := make(map[string]struct{})
			for _, g := range tc.allow {
				if g == "" {
					continue
				}
				allow[g] = struct{}{}
			}
			m := &ResourceManager{
				config:                   cfg,
				collectExt4Usage:         cfg.CollectExt4Usage,
				collectExt4UsageGroupIDs: allow,
			}
			if got := m.ext4UsageAllowed(tc.group); got != tc.want {
				t.Errorf("ext4UsageAllowed(%q) = %v, want %v", tc.group, got, tc.want)
			}
		})
	}
}
