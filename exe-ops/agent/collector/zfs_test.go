package collector

import (
	"testing"

	"exe.dev/exe-ops/apitype"
)

func TestZFSHealthWorse(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"ONLINE", "", true},
		{"DEGRADED", "ONLINE", true},
		{"FAULTED", "DEGRADED", true},
		{"FAULTED", "ONLINE", true},
		{"ONLINE", "ONLINE", false},
		{"ONLINE", "DEGRADED", false},
		{"DEGRADED", "FAULTED", false},
		{"UNKNOWN", "ONLINE", false},
		{"ONLINE", "FAULTED", false},
	}

	for _, tt := range tests {
		t.Run(tt.candidate+"_vs_"+tt.current, func(t *testing.T) {
			got := zfsHealthWorse(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf("zfsHealthWorse(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestParseZpoolList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []apitype.ZFSPool
	}{
		{
			name:  "single pool",
			input: "tank\t10737418240\t5368709120\t5368709120\tONLINE\t12%\t50%\n",
			want: []apitype.ZFSPool{
				{Name: "tank", Health: "ONLINE", Used: 5368709120, Free: 5368709120, FragPct: 12, CapPct: 50},
			},
		},
		{
			name:  "multiple pools",
			input: "tank\t10737418240\t5368709120\t5368709120\tONLINE\t12%\t50%\nbackup\t21474836480\t1073741824\t20401094656\tONLINE\t3%\t5%\n",
			want: []apitype.ZFSPool{
				{Name: "tank", Health: "ONLINE", Used: 5368709120, Free: 5368709120, FragPct: 12, CapPct: 50},
				{Name: "backup", Health: "ONLINE", Used: 1073741824, Free: 20401094656, FragPct: 3, CapPct: 5},
			},
		},
		{
			name:  "frag dash (not applicable)",
			input: "rpool\t10737418240\t5368709120\t5368709120\tONLINE\t-\t50%\n",
			want: []apitype.ZFSPool{
				{Name: "rpool", Health: "ONLINE", Used: 5368709120, Free: 5368709120, FragPct: -1, CapPct: 50},
			},
		},
		{
			name:  "degraded pool",
			input: "tank\t10737418240\t5368709120\t5368709120\tDEGRADED\t25%\t50%\n",
			want: []apitype.ZFSPool{
				{Name: "tank", Health: "DEGRADED", Used: 5368709120, Free: 5368709120, FragPct: 25, CapPct: 50},
			},
		},
		{
			name:  "empty output",
			input: "",
			want:  nil,
		},
		{
			name:  "malformed line (too few fields)",
			input: "tank\t10737418240\t5368709120\n",
			want:  nil,
		},
		{
			name:  "parseable frag without percent suffix",
			input: "tank\t10737418240\t5368709120\t5368709120\tONLINE\t12\t50\n",
			want: []apitype.ZFSPool{
				{Name: "tank", Health: "ONLINE", Used: 5368709120, Free: 5368709120, FragPct: 12, CapPct: 50},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseZpoolList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseZpoolList() returned %d pools, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("pool[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseZpoolStatus(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]poolErrors
	}{
		{
			name: "mirror pool no errors",
			input: `  pool: tank
 state: ONLINE
  scan: scrub repaired 0B in 00:01:23 with 0 errors on Sun Mar  8 00:25:03 2026
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      ONLINE       0     0     0
	  mirror-0                                ONLINE       0     0     0
	    sda                                   ONLINE       0     0     0
	    sdb                                   ONLINE       0     0     0

errors: No known data errors
`,
			want: map[string]poolErrors{
				"tank": {Read: 0, Write: 0, Cksum: 0},
			},
		},
		{
			name: "mirror pool with errors on one disk",
			input: `  pool: tank
 state: DEGRADED
  scan: scrub repaired 0B in 00:01:23 with 0 errors on Sun Mar  8 00:25:03 2026
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      DEGRADED     3     1     7
	  mirror-0                                DEGRADED     3     1     7
	    sda                                   ONLINE       0     0     0
	    sdb                                   DEGRADED     3     1     7

errors: No known data errors
`,
			want: map[string]poolErrors{
				"tank": {Read: 3, Write: 1, Cksum: 7},
			},
		},
		{
			name: "raidz pool with checksum errors",
			input: `  pool: datapool
 state: ONLINE
config:

	NAME                                      STATE     READ WRITE CKSUM
	datapool                                  ONLINE       0     0    12
	  raidz1-0                                ONLINE       0     0    12
	    sda                                   ONLINE       0     0     4
	    sdb                                   ONLINE       0     0     4
	    sdc                                   ONLINE       0     0     4

errors: No known data errors
`,
			want: map[string]poolErrors{
				"datapool": {Read: 0, Write: 0, Cksum: 12},
			},
		},
		{
			name: "single disk pool",
			input: `  pool: scratch
 state: ONLINE
config:

	NAME                                      STATE     READ WRITE CKSUM
	scratch                                   ONLINE       0     0     0
	  sdd                                     ONLINE       0     0     0

errors: No known data errors
`,
			want: map[string]poolErrors{
				"scratch": {Read: 0, Write: 0, Cksum: 0},
			},
		},
		{
			name: "multiple pools",
			input: `  pool: tank
 state: ONLINE
config:

	NAME                                      STATE     READ WRITE CKSUM
	tank                                      ONLINE       0     0     0
	  mirror-0                                ONLINE       0     0     0
	    sda                                   ONLINE       0     0     0
	    sdb                                   ONLINE       0     0     0

errors: No known data errors

  pool: backup
 state: ONLINE
config:

	NAME                                      STATE     READ WRITE CKSUM
	backup                                    ONLINE       0     2     0
	  sdc                                     ONLINE       0     2     0

errors: No known data errors
`,
			want: map[string]poolErrors{
				"tank":   {Read: 0, Write: 0, Cksum: 0},
				"backup": {Read: 0, Write: 2, Cksum: 0},
			},
		},
		{
			name:  "empty output",
			input: "",
			want:  map[string]poolErrors{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseZpoolStatus(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseZpoolStatus() returned %d pools, want %d", len(got), len(tt.want))
			}
			for pool, wantErrs := range tt.want {
				gotErrs, ok := got[pool]
				if !ok {
					t.Errorf("missing pool %q in result", pool)
					continue
				}
				if gotErrs != wantErrs {
					t.Errorf("pool %q errors = %+v, want %+v", pool, gotErrs, wantErrs)
				}
			}
		})
	}
}
