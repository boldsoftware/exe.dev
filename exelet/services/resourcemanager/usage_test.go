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

func TestParseZFSListOutput(t *testing.T) {
	// Sample output from `zfs list -Hp -o name,volsize,used,logicalused -t volume,filesystem -r -d 1 tank`.
	// Includes the pool root, a sha256 base image (skipped), and two volumes.
	output := "tank\t-\t1234\t5678\n" +
		"tank/sha256:abc\t-\t100\t200\n" +
		"tank/vm-1\t10737418240\t1048576\t2097152\n" +
		"tank/vm-2\t21474836480\t2097152\t4194304\n"

	dst := zfsVolumeMap{}
	parseZFSListOutput(output, "tank", dst)

	if len(dst) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(dst), dst)
	}
	if got, want := dst["vm-1"].Volsize, uint64(10737418240); got != want {
		t.Errorf("vm-1 volsize = %d, want %d", got, want)
	}
	if got, want := dst["vm-1"].Used, uint64(1048576); got != want {
		t.Errorf("vm-1 used = %d, want %d", got, want)
	}
	if got, want := dst["vm-1"].LogicalUsed, uint64(2097152); got != want {
		t.Errorf("vm-1 logicalused = %d, want %d", got, want)
	}
	if got, want := dst["vm-2"].Volsize, uint64(21474836480); got != want {
		t.Errorf("vm-2 volsize = %d, want %d", got, want)
	}
	if _, ok := dst["sha256:abc"]; ok {
		t.Errorf("base image should be skipped")
	}
}
