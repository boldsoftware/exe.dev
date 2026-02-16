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
		r1 region.Region
		u2 *resourceapi.MachineUsage
		c2 int32
		r2 region.Region
		r  int
	}{
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 3},
			c1: 10, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 4},
			c2: 10, r2: pdx,
			r: 0,
		},
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 10},
			c1: 10, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 2},
			c2: 10, r2: pdx,
			r: 1,
		},
		{
			u1: &resourceapi.MachineUsage{MemAvailable: 1024 << 10},
			c1: 10, r1: pdx,
			u2: &resourceapi.MachineUsage{MemAvailable: 1025 << 10},
			c2: 10, r2: pdx,
			r: 0,
		},
		{
			u1: &resourceapi.MachineUsage{MemAvailable: 10 << 20},
			c1: 10, r1: pdx,
			u2: &resourceapi.MachineUsage{MemAvailable: 100 << 20},
			c2: 10, r2: pdx,
			r: 1,
		},
		// Per-region extreme: count=390 is extreme for pdx (VMHardLimit=400) but not for lax (VMHardLimit=800).
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 1},
			c1: 390, r1: pdx,
			u2: &resourceapi.MachineUsage{LoadAverage: 1},
			c2: 390, r2: lax,
			r: 1, // pdx is extreme, lax is not
		},
		{
			u1: &resourceapi.MachineUsage{LoadAverage: 1},
			c1: 390, r1: lax,
			u2: &resourceapi.MachineUsage{LoadAverage: 1},
			c2: 390, r2: pdx,
			r: -1, // pdx is extreme, lax is not
		},
	}

	for _, test := range tests {
		var c1, c2 exeletClient
		c1.usage.Store(test.u1)
		c1.count.Store(test.c1)
		c1.region = test.r1
		c2.usage.Store(test.u2)
		c2.count.Store(test.c2)
		c2.region = test.r2
		r := exeletUsageCmp(&c1, &c2)
		if r != test.r {
			t.Errorf("case (%#v %d %s cmp %#v %d %s): got %d want %d", test.u1, test.c1, test.r1.Code, test.u2, test.c2, test.r2.Code, r, test.r)
		}
	}
}
