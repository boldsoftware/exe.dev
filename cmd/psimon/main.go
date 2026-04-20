//go:build linux

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ringSize       = 86400 // 24h at 1s intervals
	sampleInterval = 1 * time.Second
	// Kernel USER_HZ. Hardcoded: modern Linux x86_64 kernels ship with
	// CONFIG_HZ_100 by default (confirmed via `getconf CLK_TCK` on CI hosts).
	clkTck = 100
	// Maximum completed processes to retain (bounds memory).
	maxCompletedProcs = 20000
)

//go:embed query.html
var queryHTML string

var queryTmpl = template.Must(template.New("query").Parse(queryHTML))

type Sample struct {
	Timestamp  int64   `json:"Timestamp"`
	CPUPercent float64 `json:"CPUPercent"`
	CPUSome    float64 `json:"CPUSome"`
	IOSome     float64 `json:"IOSome"`
	IOFull     float64 `json:"IOFull"`
	MemSome    float64 `json:"MemSome"`
	MemFull    float64 `json:"MemFull"`
}

type RingBuffer struct {
	mu    sync.Mutex
	buf   [ringSize]Sample
	idx   int
	count int
}

func (r *RingBuffer) Add(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.idx] = s
	r.idx = (r.idx + 1) % ringSize
	if r.count < ringSize {
		r.count++
	}
}

func (r *RingBuffer) All() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]Sample, r.count)
	if r.count < ringSize {
		copy(out, r.buf[:r.count])
	} else {
		// oldest is at r.idx, wrap around
		n := copy(out, r.buf[r.idx:])
		copy(out[n:], r.buf[:r.idx])
	}
	return out
}

func (r *RingBuffer) Range(start, end int64) []Sample {
	all := r.All()
	var out []Sample
	for _, s := range all {
		if s.Timestamp >= start && s.Timestamp <= end {
			out = append(out, s)
		}
	}
	return out
}

// cpuTimes holds the raw fields from /proc/stat cpu line.
type cpuTimes struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func (c cpuTimes) busy() uint64 {
	return c.user + c.nice + c.system + c.irq + c.softirq + c.steal
}

func readCPUTimes() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 9 {
				return cpuTimes{}, fmt.Errorf("unexpected /proc/stat cpu line: %s", line)
			}
			vals := make([]uint64, 8)
			for i := 0; i < 8; i++ {
				v, err := strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return cpuTimes{}, fmt.Errorf("parse %s: %w", fields[i+1], err)
				}
				vals[i] = v
			}
			return cpuTimes{
				user:    vals[0],
				nice:    vals[1],
				system:  vals[2],
				idle:    vals[3],
				iowait:  vals[4],
				irq:     vals[5],
				softirq: vals[6],
				steal:   vals[7],
			}, nil
		}
	}
	return cpuTimes{}, fmt.Errorf("/proc/stat: no cpu line found")
}

func cpuPercent(prev, curr cpuTimes) float64 {
	dTotal := curr.total() - prev.total()
	if dTotal == 0 {
		return 0
	}
	dBusy := curr.busy() - prev.busy()
	return float64(dBusy) / float64(dTotal) * 100.0
}

// parsePSI reads a PSI file and returns avg10 values for "some" and "full" lines.
// If a line type doesn't exist (e.g. cpu has no "full"), the value is 0.
func parsePSI(path string) (someAvg10, fullAvg10 float64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		var lineType string
		if strings.HasPrefix(line, "some ") {
			lineType = "some"
		} else if strings.HasPrefix(line, "full ") {
			lineType = "full"
		} else {
			continue
		}
		avg10 := extractAvg10(line)
		switch lineType {
		case "some":
			someAvg10 = avg10
		case "full":
			fullAvg10 = avg10
		}
	}
	return someAvg10, fullAvg10, nil
}

// extractAvg10 finds "avg10=X.XX" in a PSI line and returns X.XX.
func extractAvg10(line string) float64 {
	for _, field := range strings.Fields(line) {
		if strings.HasPrefix(field, "avg10=") {
			v, err := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
			if err != nil {
				return 0
			}
			return v
		}
	}
	return 0
}

// ProcKey identifies a process uniquely across pid reuse.
type ProcKey struct {
	PID       int
	StartTick uint64 // from /proc/<pid>/stat field 22 (starttime in clock ticks since boot)
}

// ProcSample is one CPU-usage measurement for a process.
type ProcSample struct {
	Timestamp  int64   `json:"t"`
	CPUPercent float64 `json:"c"` // percent of one core over the last sample interval
}

// Process holds metadata + time series for one process.
type Process struct {
	Key       ProcKey           `json:"key"`
	PID       int               `json:"pid"`
	PPID      int               `json:"ppid,omitempty"`
	StartUnix int64             `json:"start"` // unix time of first observation
	LastUnix  int64             `json:"end"`   // unix time of last observation
	Comm      string            `json:"comm"`
	Cmdline   string            `json:"cmdline"`
	Buildkite map[string]string `json:"bk,omitempty"` // BUILDKITE_* env vars
	Samples   []ProcSample      `json:"samples"`

	// internal - not serialized.
	prevCPUTicks uint64 `json:"-"`
	haveLast     bool   `json:"-"`
}

type ProcStore struct {
	mu        sync.Mutex
	active    map[ProcKey]*Process
	completed []*Process
	bootUnix  int64
}

func newProcStore(bootUnix int64) *ProcStore {
	return &ProcStore{
		active:   make(map[ProcKey]*Process),
		bootUnix: bootUnix,
	}
}

// readBootTime returns the system boot time as unix seconds.
func readBootTime() (int64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return 0, err
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("/proc/stat: no btime")
}

// parseProcStat parses fields we care about from /proc/<pid>/stat.
// Returns comm, parent pid, utime+stime (ticks), starttime (ticks).
func parseProcStat(pid int) (comm string, ppid int, cpuTicks, startTicks uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, 0, 0, err
	}
	// comm can contain spaces and parens; find last ')' to split reliably.
	s := string(data)
	lp := strings.IndexByte(s, '(')
	rp := strings.LastIndexByte(s, ')')
	if lp < 0 || rp < 0 || rp < lp {
		return "", 0, 0, 0, fmt.Errorf("bad stat: %q", s)
	}
	comm = s[lp+1 : rp]
	rest := strings.Fields(s[rp+1:])
	// rest[0] = state, rest[1] = ppid ... field indices per proc(5) starting at 3.
	// utime = field 14 -> rest index 14-3 = 11
	// stime = field 15 -> rest index 12
	// starttime = field 22 -> rest index 19
	if len(rest) < 20 {
		return "", 0, 0, 0, fmt.Errorf("stat fields: %d", len(rest))
	}
	ppid64, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil {
		return "", 0, 0, 0, err
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return "", 0, 0, 0, err
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return "", 0, 0, 0, err
	}
	start, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return comm, int(ppid64), utime + stime, start, nil
}

func readCmdline(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	// NUL-separated; replace with spaces for display.
	return strings.TrimRight(strings.ReplaceAll(string(data), "\x00", " "), " ")
}

// readBuildkiteEnv reads /proc/<pid>/environ and returns BUILDKITE_* vars that identify the task.
func readBuildkiteEnv(pid int) map[string]string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, kv := range bytes.Split(data, []byte{0}) {
		if len(kv) == 0 {
			continue
		}
		eq := bytes.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		k := string(kv[:eq])
		if !isInterestingEnv(k) {
			continue
		}
		out[k] = string(kv[eq+1:])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isInterestingEnv(k string) bool {
	switch k {
	case "BUILDKITE_BUILD_ID", "BUILDKITE_BUILD_NUMBER", "BUILDKITE_BUILD_URL",
		"BUILDKITE_JOB_ID", "BUILDKITE_LABEL", "BUILDKITE_STEP_KEY",
		"BUILDKITE_PIPELINE_SLUG", "BUILDKITE_BRANCH", "BUILDKITE_COMMIT",
		"BUILDKITE_PARALLEL_JOB", "BUILDKITE_PARALLEL_JOB_COUNT",
		"BUILDKITE_COMMAND", "BUILDKITE_AGENT_NAME":
		return true
	}
	return false
}

func listPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if n[0] < '0' || n[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(n)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// scan walks /proc and updates the store with a new sample at `now`.
// totalTickDelta is the wall-clock delta in clock ticks since last scan (used to compute %).
func (ps *ProcStore) scan(now int64, tickDelta uint64) {
	pids, err := listPIDs()
	if err != nil {
		log.Printf("list pids: %v", err)
		return
	}
	seen := make(map[ProcKey]bool, len(pids))

	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, pid := range pids {
		comm, ppid, cpuTicks, startTicks, err := parseProcStat(pid)
		if err != nil {
			continue
		}
		key := ProcKey{PID: pid, StartTick: startTicks}
		seen[key] = true
		p, ok := ps.active[key]
		if !ok {
			p = &Process{
				Key:       key,
				PID:       pid,
				PPID:      ppid,
				StartUnix: ps.bootUnix + int64(startTicks)/clkTck,
				Comm:      comm,
				Cmdline:   readCmdline(pid),
				Buildkite: readBuildkiteEnv(pid),
			}
			ps.active[key] = p
			p.prevCPUTicks = cpuTicks
			p.haveLast = false
			p.LastUnix = now
			// No sample yet; we need a delta to compute percent.
			continue
		}
		// Compute CPU percent (of one core) over the sample interval.
		var pct float64
		if tickDelta > 0 && cpuTicks >= p.prevCPUTicks {
			pct = float64(cpuTicks-p.prevCPUTicks) / float64(tickDelta) * 100.0
		}
		p.Samples = append(p.Samples, ProcSample{Timestamp: now, CPUPercent: pct})
		p.prevCPUTicks = cpuTicks
		p.LastUnix = now
		p.haveLast = true
		// Refresh cmdline/env if we didn't have one yet (early exec of a wrapper).
		if p.Cmdline == "" {
			p.Cmdline = readCmdline(pid)
		}
		if p.Buildkite == nil {
			p.Buildkite = readBuildkiteEnv(pid)
		}
	}

	// Inherit BUILDKITE_* env vars from ancestors. Some processes (e.g.
	// headless-shell launched by Playwright/Chromium) clear or scrub env
	// before exec, losing the markers we use to group by pipeline step.
	// Walk the ppid chain until we find a process with Buildkite set.
	byPID := make(map[int]*Process, len(ps.active))
	for _, p := range ps.active {
		byPID[p.PID] = p
	}
	for _, p := range ps.active {
		if p.Buildkite != nil {
			continue
		}
		// Cap the walk to avoid cycles if /proc races produce stale ppids.
		cur := p.PPID
		for depth := 0; depth < 32 && cur > 1; depth++ {
			anc, ok := byPID[cur]
			if !ok {
				break
			}
			if anc.Buildkite != nil {
				p.Buildkite = anc.Buildkite
				break
			}
			cur = anc.PPID
		}
	}

	// Reap gone processes.
	for key, p := range ps.active {
		if seen[key] {
			continue
		}
		delete(ps.active, key)
		// Require ≥1s of samples; single-sample (no delta) processes are discarded.
		if len(p.Samples) < 1 {
			continue
		}
		// Duration check: first→last sample ≥1s.
		if p.Samples[len(p.Samples)-1].Timestamp-p.Samples[0].Timestamp < 1 {
			continue
		}
		ps.completed = append(ps.completed, p)
	}

	// Bound memory.
	if len(ps.completed) > maxCompletedProcs {
		ps.completed = ps.completed[len(ps.completed)-maxCompletedProcs:]
	}
}

// Range returns processes that overlap [start,end], including still-active ones.
//
// Filters out "background noise": processes that started more than 30s before
// the query window AND whose peak CPU in-window was below 2%. These are things
// like long-running systemd daemons, agents, etc. that predate the build and
// would otherwise dominate the uncategorized section of the visualization.
func (ps *ProcStore) Range(start, end int64) []*Process {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []*Process
	const (
		noisePeakCPUThreshold = 2.0 // percent of one core
		noiseStartGap         = 30  // seconds before window start
	)
	add := func(p *Process) {
		if len(p.Samples) < 2 {
			return
		}
		first := p.Samples[0].Timestamp
		last := p.Samples[len(p.Samples)-1].Timestamp
		if last < start || first > end {
			return
		}
		// Clip samples to window and compute peak CPU in-window.
		var clipped []ProcSample
		var peak float64
		for _, s := range p.Samples {
			if s.Timestamp >= start && s.Timestamp <= end {
				clipped = append(clipped, s)
				if s.CPUPercent > peak {
					peak = s.CPUPercent
				}
			}
		}
		if len(clipped) < 2 {
			return
		}
		if p.StartUnix < start-noiseStartGap && peak < noisePeakCPUThreshold {
			return
		}
		copy := *p
		copy.Samples = clipped
		out = append(out, &copy)
	}
	for _, p := range ps.completed {
		add(p)
	}
	for _, p := range ps.active {
		add(p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Samples[0].Timestamp < out[j].Samples[0].Timestamp
	})
	return out
}

func collectSample(prevCPU cpuTimes) (Sample, cpuTimes) {
	now := time.Now().Unix()

	currCPU, err := readCPUTimes()
	if err != nil {
		log.Printf("read cpu times: %v", err)
		currCPU = prevCPU
	}
	cpuPct := cpuPercent(prevCPU, currCPU)

	cpuSome, _, _ := parsePSI("/proc/pressure/cpu")
	ioSome, ioFull, _ := parsePSI("/proc/pressure/io")
	memSome, memFull, _ := parsePSI("/proc/pressure/memory")

	s := Sample{
		Timestamp:  now,
		CPUPercent: cpuPct,
		CPUSome:    cpuSome,
		IOSome:     ioSome,
		IOFull:     ioFull,
		MemSome:    memSome,
		MemFull:    memFull,
	}
	return s, currCPU
}

func main() {
	port := flag.Int("port", 9100, "HTTP listen port")
	flag.Parse()

	ring := &RingBuffer{}

	bootUnix, err := readBootTime()
	if err != nil {
		log.Fatalf("read boot time: %v", err)
	}
	procStore := newProcStore(bootUnix)

	// Seed CPU delta with two readings 100ms apart.
	prevCPU, err := readCPUTimes()
	if err != nil {
		log.Fatalf("initial cpu read: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	seedCPU, err := readCPUTimes()
	if err != nil {
		log.Fatalf("seed cpu read: %v", err)
	}
	_ = cpuPercent(prevCPU, seedCPU) // discard seed value
	prevCPU = seedCPU

	// Collector goroutine.
	go func() {
		ticker := time.NewTicker(sampleInterval)
		defer ticker.Stop()
		lastNanos := time.Now().UnixNano()
		for {
			<-ticker.C
			s, newCPU := collectSample(prevCPU)
			prevCPU = newCPU
			ring.Add(s)

			nowNanos := time.Now().UnixNano()
			tickDelta := uint64((nowNanos - lastNanos) * clkTck / int64(time.Second))
			if tickDelta == 0 {
				tickDelta = 1
			}
			lastNanos = nowNanos
			procStore.scan(s.Timestamp, tickDelta)
		}
	}()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		samples := ring.All()
		if samples == nil {
			samples = []Sample{}
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(samples); err != nil {
			log.Printf("encode metrics: %v", err)
		}
	})

	http.HandleFunc("/processes", func(w http.ResponseWriter, r *http.Request) {
		startStr := r.URL.Query().Get("start")
		endStr := r.URL.Query().Get("end")
		if startStr == "" || endStr == "" {
			http.Error(w, "start and end query parameters required", http.StatusBadRequest)
			return
		}
		startTS, _ := strconv.ParseInt(startStr, 10, 64)
		endTS, _ := strconv.ParseInt(endStr, 10, 64)
		procs := procStore.Range(startTS, endTS)
		if procs == nil {
			procs = []*Process{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(procs); err != nil {
			log.Printf("encode processes: %v", err)
		}
	})

	http.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		startStr := r.URL.Query().Get("start")
		endStr := r.URL.Query().Get("end")
		if startStr == "" || endStr == "" {
			http.Error(w, "start and end query parameters required (unix timestamps)", http.StatusBadRequest)
			return
		}
		startTS, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid start timestamp", http.StatusBadRequest)
			return
		}
		endTS, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid end timestamp", http.StatusBadRequest)
			return
		}

		samples := ring.Range(startTS, endTS)
		if samples == nil {
			samples = []Sample{}
		}

		samplesJSON, err := json.Marshal(samples)
		if err != nil {
			http.Error(w, "json marshal error", http.StatusInternalServerError)
			return
		}

		procs := procStore.Range(startTS, endTS)
		if procs == nil {
			procs = []*Process{}
		}
		procsJSON, err := json.Marshal(procs)
		if err != nil {
			http.Error(w, "json marshal error", http.StatusInternalServerError)
			return
		}

		startTime := time.Unix(startTS, 0).UTC().Format("2006-01-02 15:04")
		endTime := time.Unix(endTS, 0).UTC().Format("2006-01-02 15:04")

		data := struct {
			Title         string
			SamplesJSON   template.JS
			ProcessesJSON template.JS
		}{
			Title:         fmt.Sprintf("CI Machine Pressure — %s to %s", startTime, endTime),
			SamplesJSON:   template.JS(samplesJSON),
			ProcessesJSON: template.JS(procsJSON),
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := queryTmpl.Execute(w, data); err != nil {
			log.Printf("template execute: %v", err)
		}
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("psimon listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
