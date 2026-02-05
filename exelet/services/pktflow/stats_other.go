//go:build !linux

package pktflow

import "fmt"

// ReadNetStats is not supported on non-Linux platforms.
func ReadNetStats(_ string) (NetStats, error) {
	return NetStats{}, fmt.Errorf("pktflow: linux-only")
}
