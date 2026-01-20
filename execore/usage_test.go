package execore

import (
	"testing"

	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

func TestIsExtreme(t *testing.T) {
	tests := []struct {
		usage     resourceapi.MachineUsage
		isExtreme bool
	}{
		{
			usage:     resourceapi.MachineUsage{LoadAverage: 1},
			isExtreme: false,
		},
		{
			usage:     resourceapi.MachineUsage{LoadAverage: 200},
			isExtreme: true,
		},
		{
			usage:     resourceapi.MachineUsage{MemAvailable: 10 << 20},
			isExtreme: false,
		},
		{
			usage:     resourceapi.MachineUsage{MemAvailable: 1 << 10},
			isExtreme: true,
		},
		{
			usage:     resourceapi.MachineUsage{RxBytesRate: 10},
			isExtreme: false,
		},
	}

	for i, test := range tests {
		got := isExtreme(&test.usage)
		if got != test.isExtreme {
			t.Errorf("case %d (%#v): got %t want %t", i, test.usage, got, test.isExtreme)
		}
	}
}

func TestUsageCmp(t *testing.T) {
	tests := []struct {
		u1 resourceapi.MachineUsage
		c1 int32
		u2 resourceapi.MachineUsage
		c2 int32
		r  int
	}{
		{
			u1: resourceapi.MachineUsage{LoadAverage: 3},
			c1: 10,
			u2: resourceapi.MachineUsage{LoadAverage: 4},
			c2: 10,
			r:  0,
		},
		{
			u1: resourceapi.MachineUsage{LoadAverage: 10},
			c1: 10,
			u2: resourceapi.MachineUsage{LoadAverage: 2},
			c2: 10,
			r:  1,
		},
		{
			u1: resourceapi.MachineUsage{MemAvailable: 1024 << 10},
			c1: 10,
			u2: resourceapi.MachineUsage{MemAvailable: 1025 << 10},
			c2: 10,
			r:  0,
		},
		{
			u1: resourceapi.MachineUsage{MemAvailable: 10 << 20},
			c1: 10,
			u2: resourceapi.MachineUsage{MemAvailable: 100 << 20},
			c2: 10,
			r:  1,
		},
	}

	for i, test := range tests {
		var c1, c2 exeletClient
		c1.usage.Store(&test.u1)
		c1.count.Store(test.c1)
		c2.usage.Store(&test.u2)
		c2.count.Store(test.c2)
		r := exeletUsageCmp(&c1, &c2)
		if r != test.r {
			t.Errorf("case %d (%#v %d cmp %#v %d): got %d want %d", i, test.u1, test.c1, test.u2, test.c2, r, test.r)
		}
	}
}
