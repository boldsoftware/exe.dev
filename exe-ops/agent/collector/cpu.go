package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CPU collects CPU usage by sampling /proc/stat.
type CPU struct {
	Percent  float64
	procStat string // override for testing
}

func NewCPU() *CPU { return &CPU{procStat: "/proc/stat"} }

func (c *CPU) Name() string { return "cpu" }

func (c *CPU) Collect(ctx context.Context) error {
	idle1, total1, err := readCPUStat(c.procStat)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}

	idle2, total2, err := readCPUStat(c.procStat)
	if err != nil {
		return err
	}

	idleDelta := float64(idle2 - idle1)
	totalDelta := float64(total2 - total1)
	if totalDelta == 0 {
		c.Percent = 0
		return nil
	}

	c.Percent = (1.0 - idleDelta/totalDelta) * 100.0
	return nil
}

func readCPUStat(path string) (idle, total uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("unexpected /proc/stat format: %q", line)
		}
		// fields: cpu user nice system idle iowait irq softirq steal guest guest_nice
		for i := 1; i < len(fields); i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse field %d: %w", i, err)
			}
			total += v
			if i == 4 { // idle
				idle = v
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("cpu line not found in %s", path)
}
