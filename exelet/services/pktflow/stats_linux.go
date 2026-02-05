//go:build linux

package pktflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func ReadNetStats(iface string) (NetStats, error) {
	base := filepath.Join("/sys/class/net", iface, "statistics")
	read := func(name string) (uint64, error) {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			return 0, err
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", name, err)
		}
		return v, nil
	}

	rxBytes, err := read("rx_bytes")
	if err != nil {
		return NetStats{}, err
	}
	rxPackets, err := read("rx_packets")
	if err != nil {
		return NetStats{}, err
	}
	rxDropped, err := read("rx_dropped")
	if err != nil {
		return NetStats{}, err
	}
	rxErrors, err := read("rx_errors")
	if err != nil {
		return NetStats{}, err
	}
	txBytes, err := read("tx_bytes")
	if err != nil {
		return NetStats{}, err
	}
	txPackets, err := read("tx_packets")
	if err != nil {
		return NetStats{}, err
	}
	txDropped, err := read("tx_dropped")
	if err != nil {
		return NetStats{}, err
	}
	txErrors, err := read("tx_errors")
	if err != nil {
		return NetStats{}, err
	}

	return NetStats{
		RxBytes:   rxBytes,
		RxPackets: rxPackets,
		RxDropped: rxDropped,
		RxErrors:  rxErrors,
		TxBytes:   txBytes,
		TxPackets: txPackets,
		TxDropped: txDropped,
		TxErrors:  txErrors,
	}, nil
}
