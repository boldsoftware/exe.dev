package collector

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Host collects uptime, load average, file descriptor usage,
// available system updates, and failed systemd units.
type Host struct {
	UptimeSecs  int64
	LoadAvg1    float64
	LoadAvg5    float64
	LoadAvg15   float64
	FDAllocated int64
	FDMax       int64
	Updates     []string
	FailedUnits []string
	procPath    string
	loadavgPath string
	fileNRPath  string
}

func NewHost() *Host {
	return &Host{
		procPath:    "/proc/uptime",
		loadavgPath: "/proc/loadavg",
		fileNRPath:  "/proc/sys/fs/file-nr",
	}
}

func (h *Host) Name() string { return "host" }

func (h *Host) Collect(ctx context.Context) error {
	if err := h.collectUptime(); err != nil {
		return err
	}
	if err := h.collectLoadAvg(); err != nil {
		return err
	}
	if err := h.collectFileNR(); err != nil {
		return err
	}
	h.collectUpdates(ctx)
	h.collectFailedUnits(ctx)
	return nil
}

func (h *Host) collectLoadAvg() error {
	data, err := os.ReadFile(h.loadavgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", h.loadavgPath, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return fmt.Errorf("unexpected loadavg format")
	}
	h.LoadAvg1, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return fmt.Errorf("parse loadavg1: %w", err)
	}
	h.LoadAvg5, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return fmt.Errorf("parse loadavg5: %w", err)
	}
	h.LoadAvg15, err = strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return fmt.Errorf("parse loadavg15: %w", err)
	}
	return nil
}

func (h *Host) collectFileNR() error {
	data, err := os.ReadFile(h.fileNRPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", h.fileNRPath, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return fmt.Errorf("unexpected file-nr format")
	}
	h.FDAllocated, err = strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return fmt.Errorf("parse fd allocated: %w", err)
	}
	h.FDMax, err = strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return fmt.Errorf("parse fd max: %w", err)
	}
	return nil
}

func (h *Host) collectUptime() error {
	data, err := os.ReadFile(h.procPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", h.procPath, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return fmt.Errorf("unexpected uptime format")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return fmt.Errorf("parse uptime: %w", err)
	}
	h.UptimeSecs = int64(secs)
	return nil
}

func (h *Host) collectUpdates(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradeable")
	out, err := cmd.Output()
	if err != nil {
		// apt not available or no updates; not an error.
		h.Updates = []string{}
		return
	}

	h.Updates = []string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Listing") {
			continue
		}
		if line == "" {
			continue
		}
		h.Updates = append(h.Updates, line)
	}
}

func (h *Host) collectFailedUnits(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "systemctl", "--failed", "--no-legend", "--no-pager", "--plain")
	out, err := cmd.Output()
	if err != nil {
		// systemctl not available or no failed units; not an error.
		h.FailedUnits = []string{}
		return
	}

	h.FailedUnits = []string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// First field is the unit name.
		fields := strings.Fields(line)
		if len(fields) > 0 {
			h.FailedUnits = append(h.FailedUnits, fields[0])
		}
	}
}
