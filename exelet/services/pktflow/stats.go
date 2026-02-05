package pktflow

// NetStats holds raw interface counters from /sys/class/net.
type NetStats struct {
	RxBytes   uint64
	RxPackets uint64
	RxDropped uint64
	RxErrors  uint64
	TxBytes   uint64
	TxPackets uint64
	TxDropped uint64
	TxErrors  uint64
}

func (s NetStats) Delta(prev NetStats) NetStats {
	delta := func(cur, before uint64) uint64 {
		if cur >= before {
			return cur - before
		}
		return cur
	}
	return NetStats{
		RxBytes:   delta(s.RxBytes, prev.RxBytes),
		RxPackets: delta(s.RxPackets, prev.RxPackets),
		RxDropped: delta(s.RxDropped, prev.RxDropped),
		RxErrors:  delta(s.RxErrors, prev.RxErrors),
		TxBytes:   delta(s.TxBytes, prev.TxBytes),
		TxPackets: delta(s.TxPackets, prev.TxPackets),
		TxDropped: delta(s.TxDropped, prev.TxDropped),
		TxErrors:  delta(s.TxErrors, prev.TxErrors),
	}
}
