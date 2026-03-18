package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Memory collects memory metrics from /proc/meminfo.
type Memory struct {
	Total    int64
	Used     int64
	Free     int64
	SwapTotal int64
	SwapUsed  int64
	procPath string
}

func NewMemory() *Memory { return &Memory{procPath: "/proc/meminfo"} }

func (m *Memory) Name() string { return "memory" }

func (m *Memory) Collect(_ context.Context) error {
	f, err := os.Open(m.procPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", m.procPath, err)
	}
	defer f.Close()

	vals := make(map[string]int64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		v, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		// Values in /proc/meminfo are in kB.
		vals[key] = v * 1024
	}

	m.Total = vals["MemTotal"]
	if avail, ok := vals["MemAvailable"]; ok && avail > 0 {
		m.Free = avail
	} else {
		// Fallback for kernels < 3.14 that lack MemAvailable.
		m.Free = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	m.Used = m.Total - m.Free
	if m.Used < 0 {
		m.Used = 0
	}

	swapTotal := vals["SwapTotal"]
	swapFree := vals["SwapFree"]
	m.SwapTotal = swapTotal
	m.SwapUsed = swapTotal - swapFree
	if m.SwapUsed < 0 {
		m.SwapUsed = 0
	}

	return nil
}
