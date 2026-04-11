package execore

const (
	// cgroupUnitBytes is the base cgroup unit size: 8 GiB of RAM.
	cgroupUnitBytes = 8 * 1024 * 1024 * 1024
)

// cgroupUnitCapacityTiers maps nominal memory tiers (GiB) to cgroup-unit capacity.
// The kernel reports MemTotal slightly below physical size (~2% reserved),
// so callers round up to the nearest tier before lookup, same as vmLimitTiers.
var cgroupUnitCapacityTiers = []struct {
	memGiB   int64
	capacity int32
}{
	{384, 75},
	{768, 150},
	{1536, 300},
}

// cgroupUnitCapacityFromMem returns the cgroup-unit capacity for a host with
// the given total memory (in KiB). Follows the same tier-matching logic as
// updateVMLimits.
func cgroupUnitCapacityFromMem(memTotalKiB int64) int32 {
	memGiB := memTotalKiB / (1024 * 1024)
	var capacity int32 = 25 // floor for tiny/dev hosts
	for _, tier := range cgroupUnitCapacityTiers {
		if memGiB <= tier.memGiB && memGiB >= tier.memGiB*95/100 {
			capacity = tier.capacity
			break
		}
		if memGiB > tier.memGiB {
			capacity = tier.capacity
		}
	}
	return capacity
}

// userCgroupUnits returns how many cgroup units a user consumes based on
// their resource limits. A nil or default-memory user counts as 1 unit.
func userCgroupUnits(limits *UserLimits) int {
	if limits == nil || limits.MaxMemory == 0 {
		return 1
	}
	units := int(limits.MaxMemory / cgroupUnitBytes)
	if units < 1 {
		units = 1
	}
	return units
}
