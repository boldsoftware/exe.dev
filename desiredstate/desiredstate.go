// Package desiredstate defines the shared schema for exelet <-> exed
// desired-state synchronization.
package desiredstate

// CgroupSetting is a single cgroup file and its desired value.
type CgroupSetting struct {
	Path  string `json:"path"`  // e.g. "cpu.max"
	Value string `json:"value"` // e.g. "max 100000"
}

// Group defines cgroup settings for an account-level slice.
type Group struct {
	Name   string          `json:"name"`
	Cgroup []CgroupSetting `json:"cgroup"`
}

// VM defines the desired state for a single VM.
type VM struct {
	ID     string          `json:"id"`
	Group  string          `json:"group"`
	State  string          `json:"state"` // "running" or "stopped"
	Cgroup []CgroupSetting `json:"cgroup"`
}

// DesiredState is the top-level response from /exelet-desired.
type DesiredState struct {
	Groups []Group `json:"groups"`
	VMs    []VM    `json:"vms"`
}
