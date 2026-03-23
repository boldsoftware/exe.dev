package execore

import (
	"testing"

	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	"exe.dev/region"
)

func TestIsExtreme(t *testing.T) {
	tests := []struct {
		usage     *resourceapi.MachineUsage
		isExtreme bool
	}{
		{
			usage:     &resourceapi.MachineUsage{LoadAverage: 1},
			isExtreme: false,
		},
		{
			usage:     &resourceapi.MachineUsage{LoadAverage: 200},
			isExtreme: true,
		},
		{
			usage:     &resourceapi.MachineUsage{MemAvailable: 10 << 20},
			isExtreme: false,
		},
		{
			usage:     &resourceapi.MachineUsage{MemAvailable: 1 << 10},
			isExtreme: true,
		},
		{
			usage:     &resourceapi.MachineUsage{RxBytesRate: 10},
			isExtreme: false,
		},
	}

	for _, test := range tests {
		got := isExtreme(test.usage)
		if got != test.isExtreme {
			t.Errorf("case (%#v): got %t want %t", test.usage, got, test.isExtreme)
		}
	}
}

func TestUsageCmp(t *testing.T) {
	pdx, err := region.ByCode("pdx")
	if err != nil {
		t.Fatal(err)
	}
	lax, err := region.ByCode("lax")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		u1 *resourceapi.MachineUsage
		c1 int32
		h1 int32 // vmHardLimit for client 1
		r1 region.Region
		u2 *resourceapi.MachineUsage
		c2 int32
		h2 int32 // vmHardLimit for client 2
		r2 region.Region
		r  int
	}{
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 3},
			c1: 10, h1: 400, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 4},
			c2: 10, h2: 400, r2: pdx,
			r: 0,
		},
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 10},
			c1: 10, h1: 400, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 2},
			c2: 10, h2: 400, r2: pdx,
			r: 1,
		},
		{
			u1: &resourceapi.MachineUsage{MemAvailable: 1024 << 10},
			c1: 10, h1: 400, r1: pdx,
			u2: &resourceapi.MachineUsage{MemAvailable: 1025 << 10},
			c2: 10, h2: 400, r2: pdx,
			r: 0,
		},
		{
			u1: &resourceapi.MachineUsage{MemAvailable: 10 << 20},
			c1: 10, h1: 400, r1: pdx,
			u2: &resourceapi.MachineUsage{MemAvailable: 100 << 20},
			c2: 10, h2: 400, r2: pdx,
			r: 1,
		},
		// Per-exelet extreme: count=390 is extreme for a 400-limit host but not for an 800-limit host.
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 1},
			c1: 390, h1: 400, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 1},
			c2: 390, h2: 800, r2: lax,
			r: 1, // 400-limit host is extreme, 800-limit host is not
		},
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 1},
			c1: 390, h1: 800, r1: lax,
			u2: &resourceapi.MachineUsage{LoadAverage: 1},
			c2: 390, h2: 400, r2: pdx,
			r: -1, // 400-limit host is extreme, 800-limit host is not
		},
	}

	for _, test := range tests {
		var c1, c2 exeletClient
		c1.usage.Store(test.u1)
		c1.count.Store(test.c1)
		c1.region = test.r1
		c1.vmHardLimit.Store(test.h1)
		c2.usage.Store(test.u2)
		c2.count.Store(test.c2)
		c2.region = test.r2
		c2.vmHardLimit.Store(test.h2)
		r := exeletUsageCmp(&c1, &c2)
		if r != test.r {
			t.Errorf("case (%#v %d %s cmp %#v %d %s): got %d want %d", test.u1, test.c1, test.r1.Code, test.u2, test.c2, test.r2.Code, r, test.r)
		}
	}
}

func TestUpdateVMLimits(t *testing.T) {
	tests := []struct {
		memTotalKiB int64
		wantHard    int32
		wantSoft    int32
	}{
		{memTotalKiB: 384 * 1024 * 1024, wantHard: 400, wantSoft: 350},  // exact 384 GiB
		{memTotalKiB: 377 * 1024 * 1024, wantHard: 400, wantSoft: 350},  // AWS m5d.metal (pdx) — kernel reserves ~2%
		{memTotalKiB: 768 * 1024 * 1024, wantHard: 600, wantSoft: 525},  // exact 768 GiB
		{memTotalKiB: 754 * 1024 * 1024, wantHard: 600, wantSoft: 525},  // Latitude rs4-metal-xlarge (lax)
		{memTotalKiB: 1536 * 1024 * 1024, wantHard: 800, wantSoft: 700}, // exact 1536 GiB
		{memTotalKiB: 1506 * 1024 * 1024, wantHard: 800, wantSoft: 700}, // 1.5 TiB host (reported ~1506)
		{memTotalKiB: 8 * 1024 * 1024, wantHard: 10, wantSoft: 8},       // small dev box (floor)
		{memTotalKiB: 0, wantHard: 10, wantSoft: 8},                     // zero (floor)
	}
	for _, tt := range tests {
		var ec exeletClient
		ec.updateVMLimits(tt.memTotalKiB)
		if got := ec.VMHardLimit(); got != tt.wantHard {
			t.Errorf("memTotalKiB=%d: VMHardLimit()=%d, want %d", tt.memTotalKiB, got, tt.wantHard)
		}
		if got := ec.VMSoftLimit(); got != tt.wantSoft {
			t.Errorf("memTotalKiB=%d: VMSoftLimit()=%d, want %d", tt.memTotalKiB, got, tt.wantSoft)
		}
	}
}
