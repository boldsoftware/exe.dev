//go:build !linux

package netns

import (
	"context"
	"fmt"
	"io"
)

// DeviceInfo holds diagnostic information about a single network device.
type DeviceInfo struct {
	Name      string
	Location  string
	State     string
	Master    string
	Type      string
	Addrs     []string
	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
	RxDropped uint64
	TxDropped uint64
	MTU       int
	MAC       string
	Error     string
}

// DiagResult holds the full diagnostic output for one instance.
type DiagResult struct {
	InstanceID string
	NsName     string
	ExtIP      string
	Devices    []DeviceInfo
	NsIPTables string
	NsRoutes   string
}

func Diagnose(_ context.Context, _ string) (*DiagResult, error) {
	return nil, fmt.Errorf("netns diagnostics require linux")
}

func DiagnoseAll(_ context.Context) ([]*DiagResult, error) {
	return nil, fmt.Errorf("netns diagnostics require linux")
}

func DiagnoseByVMID(_ context.Context, _ string) *DiagResult {
	return &DiagResult{}
}

func FormatDiag(_ io.Writer, _ *DiagResult) {}
