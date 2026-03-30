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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ringSize       = 86400 // 24h at 1s intervals
	sampleInterval = 1 * time.Second
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
		for {
			<-ticker.C
			s, newCPU := collectSample(prevCPU)
			prevCPU = newCPU
			ring.Add(s)
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

		startTime := time.Unix(startTS, 0).UTC().Format("2006-01-02 15:04")
		endTime := time.Unix(endTS, 0).UTC().Format("2006-01-02 15:04")

		data := struct {
			Title       string
			SamplesJSON template.JS
		}{
			Title:       fmt.Sprintf("CI Machine Pressure — %s to %s", startTime, endTime),
			SamplesJSON: template.JS(samplesJSON),
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
