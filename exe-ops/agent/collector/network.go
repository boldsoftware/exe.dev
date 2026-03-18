package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Network collects network send/recv bytes from /proc/net/dev.
// It reports the delta (bytes transferred) since the previous collection.
// Error and drop counters are reported as cumulative totals.
type Network struct {
	Send      int64
	Recv      int64
	RxErrors  int64
	RxDropped int64
	TxErrors  int64
	TxDropped int64
	procPath  string

	prevSend int64
	prevRecv int64
	hasPrev  bool
}

func NewNetwork() *Network { return &Network{procPath: "/proc/net/dev"} }

func (n *Network) Name() string { return "network" }

func (n *Network) Collect(_ context.Context) error {
	f, err := os.Open(n.procPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", n.procPath, err)
	}
	defer f.Close()

	var totalRecv, totalSend int64
	var totalRxErrors, totalRxDropped, totalTxErrors, totalTxDropped int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Skip header lines.
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		// Skip loopback.
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 12 {
			continue
		}
		recv, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		send, err := strconv.ParseInt(fields[8], 10, 64)
		if err != nil {
			continue
		}
		totalRecv += recv
		totalSend += send

		// fields[2]=rx_errs, fields[3]=rx_drop, fields[10]=tx_errs, fields[11]=tx_drop
		if v, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			totalRxErrors += v
		}
		if v, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
			totalRxDropped += v
		}
		if v, err := strconv.ParseInt(fields[10], 10, 64); err == nil {
			totalTxErrors += v
		}
		if v, err := strconv.ParseInt(fields[11], 10, 64); err == nil {
			totalTxDropped += v
		}
	}

	// Error/drop counters are cumulative totals (small monotonic counters).
	n.RxErrors = totalRxErrors
	n.RxDropped = totalRxDropped
	n.TxErrors = totalTxErrors
	n.TxDropped = totalTxDropped

	if n.hasPrev {
		n.Recv = totalRecv - n.prevRecv
		n.Send = totalSend - n.prevSend
		// Guard against counter wraps or interface resets.
		if n.Recv < 0 {
			n.Recv = totalRecv
		}
		if n.Send < 0 {
			n.Send = totalSend
		}
	} else {
		// First collection — no previous baseline, report zero.
		n.Recv = 0
		n.Send = 0
	}

	n.prevRecv = totalRecv
	n.prevSend = totalSend
	n.hasPrev = true
	return nil
}
