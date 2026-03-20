//go:build linux

package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"exe.dev/logging"
)

//go:embed index.html
var staticFS embed.FS

const sparklineLen = 20

type sparklineKey struct {
	path   string
	metric string
}

// APIResponse is the JSON response for GET /api/data.
type APIResponse struct {
	System           SystemStats                     `json:"system"`
	Tree             *CgroupNode                     `json:"tree"`
	Sparklines       map[string]map[string][]float64 `json:"sparklines"`
	SystemSparklines map[string][]float64            `json:"system_sparklines"`
	Timestamp        int64                           `json:"timestamp"`
}

type collector struct {
	mu         sync.Mutex
	cgroupRoot string

	system SystemStats
	tree   *CgroupNode

	// Previous raw values for rate computation.
	prevRawStats map[string]map[string]float64
	prevTime     time.Time

	sparkRings       map[sparklineKey]*ringBuf
	systemSparkRings map[string]*ringBuf
	prevJiffies      cpuJiffies

	// Idle tracking: only collect when clients are connected.
	lastActive time.Time
}

type ringBuf struct {
	data [sparklineLen]float64
	pos  int
	len  int
}

func (r *ringBuf) push(v float64) {
	r.data[r.pos] = v
	r.pos = (r.pos + 1) % sparklineLen
	if r.len < sparklineLen {
		r.len++
	}
}

func (r *ringBuf) values() []float64 {
	if r.len == 0 {
		return nil
	}
	out := make([]float64, r.len)
	start := (r.pos - r.len + sparklineLen) % sparklineLen
	for i := range r.len {
		out[i] = r.data[(start+i)%sparklineLen]
	}
	return out
}

// Metrics that get sparklines, split by source.
var sparklineRateMetrics = []string{
	"cpu.usage_usec", "cpu.user_usec", "cpu.system_usec",
	"cpu.throttled_usec", "cpu.nr_throttled", "cpu.nr_periods",
	"io.rbytes", "io.wbytes", "io.rios", "io.wios",
	"memory.events.oom_kill", "memory.events.oom",
	"memory.stat.pgfault", "memory.stat.pgmajfault",
}

var sparklineGaugeMetrics = []string{
	"memory.current", "memory.swap.current", "memory.zswap.current",
	"memory.stat.anon", "memory.stat.file", "memory.stat.kernel",
	"memory.stat.shmem", "memory.stat.slab",
	"pids.current",
	"psi.cpu.some.avg10", "psi.cpu.full.avg10",
	"psi.memory.some.avg10", "psi.memory.full.avg10",
	"psi.io.some.avg10", "psi.io.full.avg10",
}

func newCollector(cgroupRoot string) *collector {
	return &collector{
		cgroupRoot:       cgroupRoot,
		prevRawStats:     make(map[string]map[string]float64),
		sparkRings:       make(map[sparklineKey]*ringBuf),
		systemSparkRings: make(map[string]*ringBuf),
	}
}

func (c *collector) collect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collectLocked()
}

// collectLocked does the actual collection work. Caller must hold c.mu.
func (c *collector) collectLocked() {
	// After a long idle gap, discard stale rate baselines and sparklines
	// so we don't show misleading averaged rates on reconnect.
	if !c.prevTime.IsZero() && time.Since(c.prevTime) > 30*time.Second {
		c.prevRawStats = make(map[string]map[string]float64)
		c.sparkRings = make(map[sparklineKey]*ringBuf)
		c.systemSparkRings = make(map[string]*ringBuf)
	}

	sys, jiffies, err := readSystemStats(c.prevJiffies)
	if err != nil {
		log.Printf("system stats: %v", err)
	} else {
		c.system = sys
		c.prevJiffies = jiffies

		// Record system-level sparklines.
		sysMetrics := map[string]float64{
			"cpu_pct":           100 - sys.CPU.IdlePct,
			"psi_cpu_some10":    sys.PSICPU.Some.Avg10,
			"psi_cpu_full10":    sys.PSICPU.Full.Avg10,
			"psi_memory_some10": sys.PSIMem.Some.Avg10,
			"psi_memory_full10": sys.PSIMem.Full.Avg10,
			"psi_io_some10":     sys.PSIIO.Some.Avg10,
			"psi_io_full10":     sys.PSIIO.Full.Avg10,
		}
		for k, v := range sysMetrics {
			ring, ok := c.systemSparkRings[k]
			if !ok {
				ring = &ringBuf{}
				c.systemSparkRings[k] = ring
			}
			ring.push(v)
		}
	}

	tree, err := walkCgroupTree(c.cgroupRoot, "")
	if err != nil {
		log.Printf("cgroup walk: %v", err)
		return
	}

	now := time.Now()
	elapsed := now.Sub(c.prevTime).Seconds()
	newRaw := make(map[string]map[string]float64)

	c.processNode(tree, elapsed, newRaw)

	c.prevRawStats = newRaw
	c.prevTime = now
	c.tree = tree
}

// processNode computes rates for cumulative stats and updates sparklines.
func (c *collector) processNode(node *CgroupNode, elapsed float64, newRaw map[string]map[string]float64) {
	raw := make(map[string]float64)
	node.Rates = make(map[string]float64)

	for key, val := range node.Stats {
		if !cumulativeStats[key] {
			continue
		}
		raw[key] = val
		if elapsed > 0 {
			if prev, ok := c.prevRawStats[node.Path]; ok {
				if prevVal, ok := prev[key]; ok {
					rate := (val - prevVal) / elapsed
					if rate < 0 {
						rate = 0
					}
					node.Rates[key] = rate
				}
			}
		}
		delete(node.Stats, key)
	}
	newRaw[node.Path] = raw

	// Sparklines for rates.
	for _, key := range sparklineRateMetrics {
		if v, ok := node.Rates[key]; ok {
			sk := sparklineKey{path: node.Path, metric: key}
			ring, ok := c.sparkRings[sk]
			if !ok {
				ring = &ringBuf{}
				c.sparkRings[sk] = ring
			}
			ring.push(v)
		}
	}
	// Sparklines for gauges.
	for _, key := range sparklineGaugeMetrics {
		if v, ok := node.Stats[key]; ok {
			sk := sparklineKey{path: node.Path, metric: key}
			ring, ok := c.sparkRings[sk]
			if !ok {
				ring = &ringBuf{}
				c.sparkRings[sk] = ring
			}
			ring.push(v)
		}
	}

	for _, child := range node.Children {
		c.processNode(child, elapsed, newRaw)
	}
}

func (c *collector) snapshot(rootFilter string) APIResponse {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastActive = time.Now()

	// Collect fresh data on demand if stale.
	if c.prevTime.IsZero() || time.Since(c.prevTime) > 10*time.Second {
		c.collectLocked()
	}

	tree := c.tree
	if rootFilter != "" && tree != nil {
		tree = findSubtree(tree, rootFilter)
	}

	sparklines := make(map[string]map[string][]float64)
	if tree != nil {
		collectSparklines(tree, c.sparkRings, sparklines)
	}

	systemSparklines := make(map[string][]float64)
	for k, ring := range c.systemSparkRings {
		if ring.len > 0 {
			systemSparklines[k] = ring.values()
		}
	}

	return APIResponse{
		System:           c.system,
		Tree:             tree,
		Sparklines:       sparklines,
		SystemSparklines: systemSparklines,
		Timestamp:        time.Now().Unix(),
	}
}

func collectSparklines(node *CgroupNode, rings map[sparklineKey]*ringBuf, out map[string]map[string][]float64) {
	allMetrics := append(sparklineRateMetrics, sparklineGaugeMetrics...)
	for _, metric := range allMetrics {
		key := sparklineKey{path: node.Path, metric: metric}
		if ring, ok := rings[key]; ok && ring.len > 0 {
			if out[node.Path] == nil {
				out[node.Path] = make(map[string][]float64)
			}
			out[node.Path][metric] = ring.values()
		}
	}
	for _, child := range node.Children {
		collectSparklines(child, rings, out)
	}
}

func findSubtree(node *CgroupNode, path string) *CgroupNode {
	path = strings.TrimPrefix(path, "/")
	if node.Path == path {
		return node
	}
	for _, child := range node.Children {
		if found := findSubtree(child, path); found != nil {
			return found
		}
	}
	return nil
}

func main() {
	httpAddr := flag.String("http", ":9090", "HTTP listen address")
	cgroupRoot := flag.String("root", "", "cgroup2 root path (auto-detected if empty)")
	flag.Parse()

	root := *cgroupRoot
	if root == "" {
		root = findCgroupRoot()
	}
	log.Printf("cgroup root: %s", root)

	c := newCollector(root)
	c.collect()

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.mu.Lock()
			active := time.Since(c.lastActive) < 60*time.Second
			if active {
				c.collectLocked()
			}
			c.mu.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("index.html")
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		rootParam := r.URL.Query().Get("root")
		if rootParam != "" {
			cleaned := filepath.Clean(rootParam)
			if strings.Contains(cleaned, "..") {
				http.Error(w, "invalid root path", 400)
				return
			}
			rootParam = cleaned
		}
		resp := c.snapshot(rootParam)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("json encode: %v", err)
		}
	})

	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	mux.Handle("GET /debug/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /debug/gitsha", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, logging.GitCommit())
	})

	fmt.Printf("listening on %s\n", *httpAddr)
	log.Fatal(http.ListenAndServe(*httpAddr, mux))
}
