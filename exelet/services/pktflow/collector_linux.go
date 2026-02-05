//go:build linux

package pktflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"exe.dev/exelet/config"
	api "exe.dev/pkg/api/exe/pktflow/v1"
)

// CollectorConfig configures the Linux tap collector.
type CollectorConfig struct {
	HostID         string
	DataDir        string
	Interval       time.Duration
	MappingRefresh time.Duration
	SampleRate     uint32
	MaxFlows       int
	Publish        func(*api.FlowStatsReport)
}

// Collector periodically reads tap counters and sends reports.
type Collector struct {
	cfg      CollectorConfig
	log      *slog.Logger
	mapping  map[string]VMInfo
	prev     map[string]NetStats
	samplers map[string]*tapSampler
	lastMap  time.Time
}

func NewCollector(cfg CollectorConfig, log *slog.Logger) (*Collector, error) {
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if cfg.HostID == "" {
		return nil, fmt.Errorf("host id is required")
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("data dir is required")
	}
	if cfg.Publish == nil {
		return nil, fmt.Errorf("publish callback is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = config.DefaultPktFlowInterval
	}
	if cfg.MappingRefresh <= 0 {
		cfg.MappingRefresh = time.Minute
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 1024
	}
	if cfg.MaxFlows == 0 {
		cfg.MaxFlows = 200
	}

	return &Collector{
		cfg:      cfg,
		log:      log,
		prev:     make(map[string]NetStats),
		samplers: make(map[string]*tapSampler),
	}, nil
}

func (c *Collector) Init() error {
	return c.refreshMapping()
}

func (c *Collector) Run(ctx context.Context) {
	if c.mapping == nil {
		if err := c.refreshMapping(); err != nil {
			c.log.ErrorContext(ctx, "pktflow: failed to load instance mapping", "error", err)
			return
		}
	}

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.collectOnce(ctx); err != nil {
				c.log.WarnContext(ctx, "pktflow: collect failed", "error", err)
			}
		}
	}
}

func (c *Collector) collectOnce(ctx context.Context) error {
	if time.Since(c.lastMap) >= c.cfg.MappingRefresh {
		if err := c.refreshMapping(); err != nil {
			c.log.WarnContext(ctx, "pktflow: mapping refresh failed", "error", err)
		}
	}

	now := time.Now().UTC()
	report := &api.FlowStatsReport{
		HostID:     c.cfg.HostID,
		TsUnixMs:   now.UnixMilli(),
		IntervalMs: int64(c.cfg.Interval / time.Millisecond),
		SampleRate: c.cfg.SampleRate,
	}

	for tap, info := range c.mapping {
		stats, err := ReadNetStats(tap)
		if err != nil {
			c.log.DebugContext(ctx, "pktflow: read tap stats failed", "tap", tap, "error", err)
			delete(c.prev, tap)
			continue
		}

		prev, ok := c.prev[tap]
		c.prev[tap] = stats
		if !ok {
			continue
		}
		delta := stats.Delta(prev)

		var flows []*api.FlowRecord
		if sampler := c.samplers[tap]; sampler != nil {
			for _, f := range sampler.Snapshot() {
				flows = append(flows, &api.FlowRecord{
					DstIP:     f.DstIP,
					IpVersion: uint32(f.IPVersion),
					IpProto:   uint32(f.IPProto),
					SrcPort:   uint32(f.SrcPort),
					DstPort:   uint32(f.DstPort),
					IcmpType:  uint32(f.ICMPType),
					TcpFlags:  uint32(f.TCPFlags),
					Fragment:  f.Fragment,
					Packets:   f.Packets,
					Bytes:     f.Bytes,
				})
			}
		}

		report.Vms = append(report.Vms, &api.VMFlowStats{
			VmID:      info.VMID,
			UserID:    info.UserID,
			Tap:       tap,
			TxBytes:   delta.RxBytes,
			TxPackets: delta.RxPackets,
			TxDropped: delta.RxDropped,
			TxErrors:  delta.RxErrors,
			RxBytes:   delta.TxBytes,
			RxPackets: delta.TxPackets,
			RxDropped: delta.TxDropped,
			RxErrors:  delta.TxErrors,
			Flows:     flows,
		})
	}

	if len(report.Vms) == 0 {
		return nil
	}

	c.cfg.Publish(report)
	return nil
}

func (c *Collector) refreshMapping() error {
	mapping, err := LoadInstanceMap(c.cfg.DataDir)
	if err != nil {
		return err
	}
	for tap := range mapping {
		if _, ok := c.samplers[tap]; ok {
			continue
		}
		sampler, err := newTapSampler(tap, c.cfg.SampleRate, c.cfg.MaxFlows, c.log)
		if err != nil {
			c.log.Warn("pktflow: failed to start sampler", "tap", tap, "error", err)
			continue
		}
		c.samplers[tap] = sampler
	}
	for tap, sampler := range c.samplers {
		if _, ok := mapping[tap]; ok {
			continue
		}
		sampler.Close()
		delete(c.samplers, tap)
		delete(c.prev, tap)
	}
	c.mapping = mapping
	c.lastMap = time.Now()
	return nil
}
