package agent

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"exe.dev/exe-ops/agent/client"
	"exe.dev/exe-ops/agent/collector"
	"exe.dev/exe-ops/apitype"
)

// Agent orchestrates metric collection and reporting.
type Agent struct {
	name     string
	tags     []string
	version  string
	interval time.Duration
	client   *client.Client
	log      *slog.Logger

	cpu       *collector.CPU
	memory    *collector.Memory
	disk      *collector.Disk
	network   *collector.Network
	host      *collector.Host
	zfs       *collector.ZFS
	zfsArc    *collector.ZFSArc
	conntrack *collector.Conntrack
	exe       *collector.Exe
}

// New creates a new Agent.
func New(name string, tags []string, version string, interval time.Duration, c *client.Client, log *slog.Logger) *Agent {
	return &Agent{
		name:      name,
		tags:      tags,
		version:   version,
		interval:  interval,
		client:    c,
		log:       log,
		cpu:       collector.NewCPU(),
		memory:    collector.NewMemory(),
		disk:      collector.NewDisk(),
		network:   collector.NewNetwork(),
		host:      collector.NewHost(),
		zfs:       collector.NewZFS(),
		zfsArc:    collector.NewZFSArc(),
		conntrack: collector.NewConntrack(),
		exe:       collector.NewExe(),
	}
}

// Run starts the agent loop. It blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent starting", "name", a.name, "version", a.version, "interval", a.interval)

	// Start SSE presence stream in background.
	go a.maintainStream(ctx)

	// Collect and send immediately on start.
	a.collectAndSend(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.log.Info("agent stopping")
			return ctx.Err()
		case <-ticker.C:
			a.collectAndSend(ctx)
		}
	}
}

// maintainStream keeps a persistent SSE connection to the server for presence detection.
// It reconnects with backoff on failure and handles server-pushed events.
func (a *Agent) maintainStream(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		a.log.Info("connecting to server stream")
		resp, err := a.client.StreamConnect(ctx, a.name)
		if err != nil {
			a.log.Warn("stream connect failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		// Read SSE lines from the stream.
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: upgrade-available") {
				a.log.Info("upgrade available via stream, starting self-upgrade")
				if err := a.selfUpgrade(ctx); err != nil {
					a.log.Error("self-upgrade failed", "error", err)
				}
			}
		}
		resp.Body.Close()

		a.log.Warn("stream disconnected, reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (a *Agent) collectAndSend(ctx context.Context) {
	report, err := a.collect(ctx)
	if err != nil {
		a.log.Error("collect failed", "error", err)
		return
	}

	resp, err := a.client.SendReport(ctx, report)
	if err != nil {
		a.log.Error("send report failed", "error", err)
		return
	}

	a.log.Info("report sent", "cpu", report.CPU, "mem_used", report.MemUsed)

	if resp.UpgradeAvailable {
		a.log.Info("upgrade available, starting self-upgrade")
		if err := a.selfUpgrade(ctx); err != nil {
			a.log.Error("self-upgrade failed", "error", err)
		}
	}
}

func (a *Agent) collect(ctx context.Context) (*apitype.Report, error) {
	collectors := []collector.Collector{
		a.cpu, a.memory, a.disk, a.network, a.host, a.zfs, a.zfsArc, a.conntrack, a.exe,
	}

	for _, c := range collectors {
		if err := c.Collect(ctx); err != nil {
			a.log.Warn("collector failed", "collector", c.Name(), "error", err)
			// Continue with other collectors.
		}
	}

	report := &apitype.Report{
		Name:           a.name,
		Tags:           a.tags,
		Timestamp:      time.Now().UTC(),
		AgentVersion:   a.version,
		Arch:           runtime.GOARCH,
		CPU:            a.cpu.Percent,
		MemTotal:       a.memory.Total,
		MemUsed:        a.memory.Used,
		MemFree:        a.memory.Free,
		MemSwap:        a.memory.SwapUsed,
		MemSwapTotal:   a.memory.SwapTotal,
		DiskTotal:      a.disk.Total,
		DiskUsed:       a.disk.Used,
		DiskFree:       a.disk.Free,
		NetSend:        a.network.Send,
		NetRecv:        a.network.Recv,
		ZFSUsed:        a.zfs.Used,
		ZFSFree:        a.zfs.Free,
		BackupZFSUsed:  a.zfs.BackupUsed,
		BackupZFSFree:  a.zfs.BackupFree,
		UptimeSecs:     a.host.UptimeSecs,
		LoadAvg1:       a.host.LoadAvg1,
		LoadAvg5:       a.host.LoadAvg5,
		LoadAvg15:      a.host.LoadAvg15,
		ZFSPoolHealth:  a.zfs.PoolHealth,
		ZFSArcSize:     a.zfsArc.Size,
		ZFSArcHitRate:  a.zfsArc.HitRate,
		NetRxErrors:    a.network.RxErrors,
		NetRxDropped:   a.network.RxDropped,
		NetTxErrors:    a.network.TxErrors,
		NetTxDropped:   a.network.TxDropped,
		ConntrackCount: a.conntrack.Count,
		ConntrackMax:   a.conntrack.Max,
		FDAllocated:    a.host.FDAllocated,
		FDMax:          a.host.FDMax,
		Updates:        a.host.Updates,
		FailedUnits:    a.host.FailedUnits,
	}

	for _, c := range a.exe.Components {
		report.Components = append(report.Components, apitype.Component{
			Name:    c.Name,
			Version: c.Version,
			Status:  c.Status,
		})
	}

	report.ZFSPools = a.zfs.Pools

	return report, nil
}

// selfUpgrade downloads a new binary from the server, replaces the current
// binary, and re-execs the process with the same arguments.
func (a *Agent) selfUpgrade(ctx context.Context) error {
	data, newVersion, err := a.client.DownloadAgentBinary(ctx, a.name, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("downloaded binary is empty")
	}

	if newVersion != "" && newVersion == a.version {
		a.log.Warn("server offered same version, skipping upgrade", "version", newVersion)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// Resolve symlinks to get the real path.
	exe, err = evalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	// Get current file permissions.
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("stat executable: %w", err)
	}

	// Write new binary to a temp file in the same directory.
	tmpPath := exe + ".upgrade"
	if err := os.WriteFile(tmpPath, data, info.Mode()); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}

	// Atomically replace the old binary.
	if err := os.Rename(tmpPath, exe); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	a.log.Info("binary replaced, re-executing", "new_version", newVersion, "path", exe)

	// Re-exec with the same arguments.
	return syscall.Exec(exe, os.Args, os.Environ())
}

func evalSymlinks(path string) (string, error) {
	resolved, err := os.Readlink(path)
	if err != nil {
		// Not a symlink, use as-is.
		return path, nil
	}
	return resolved, nil
}
