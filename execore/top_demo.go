//go:build topdemo

package execore

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"exe.dev/container"
	tea "github.com/charmbracelet/bubbletea"
)

// RunTopDemo runs the top Bubble Tea UI populated with n synthetic VMs.
// Used by cmd/topdemo for manual testing of the scrolling UX.
func RunTopDemo(n int, r *rand.Rand) error {
	rows := make([]vmUsageRow, n)
	for i := range rows {
		rows[i] = vmUsageRow{
			Name:         fmt.Sprintf("demo-vm-%02d", i),
			Status:       string(container.StatusRunning),
			CPUPercent:   r.Float64() * 200,
			MemBytes:     uint64(r.Intn(8)+1) * (1 << 30),
			SwapBytes:    uint64(r.Intn(3)) * (1 << 28),
			MemCapacity:  4 << 30,
			DiskCapacity: 10 << 30,
			NetRx:        uint64(r.Int63n(1 << 30)),
			NetTx:        uint64(r.Int63n(1 << 30)),
		}
	}
	model := &topModel{
		rows:      rows,
		startTime: time.Now(),
		fetchFunc: func(ctx context.Context) ([]vmUsageRow, error) { return rows, nil },
	}
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
