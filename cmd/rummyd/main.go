// rummyd is a Real User Monitoring daemon that checks blog rendering
// across all exeprox machines by SSH'ing to each one and fetching
// https://blog.exe.dev/debug/gitsha.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var (
		listenAddr    string
		checkInterval time.Duration
	)
	flag.StringVar(&listenAddr, "listen", ":9099", "listen address for metrics")
	flag.DurationVar(&checkInterval, "interval", 1*time.Minute, "check interval")
	flag.Parse()

	registry := prometheus.NewRegistry()

	blogUp := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rummy_blog_up",
		Help: "Whether blog.exe.dev/debug/gitsha is reachable via this exeprox (1=up, 0=down).",
	}, []string{"host"})

	blogCurlLatency := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rummy_blog_curl_latency_seconds",
		Help: "HTTP-only latency of fetching blog.exe.dev/debug/gitsha (curl time_total, excludes SSH).",
	}, []string{"host", "latitude", "longitude", "city"})

	blogTotalLatency := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rummy_blog_total_latency_seconds",
		Help: "Total wall-clock latency including SSH connect + curl.",
	}, []string{"host", "latitude", "longitude", "city"})

	blogGitSHA := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rummy_blog_gitsha_info",
		Help: "Info metric with the git SHA returned by blog.exe.dev/debug/gitsha.",
	}, []string{"host", "sha"})

	checksTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rummy_checks_total",
		Help: "Total number of checks performed.",
	}, []string{"host", "result"})

	lastCheckTime := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_last_check_timestamp_seconds",
		Help: "Unix timestamp of the last check run.",
	})

	registry.MustRegister(blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA, checksTotal, lastCheckTime)

	go func() {
		for {
			hosts, err := findExeproxHosts()
			if err != nil {
				log.Printf("error finding exeprox hosts: %v", err)
				time.Sleep(checkInterval)
				continue
			}
			if len(hosts) == 0 {
				log.Printf("no exeprox hosts found")
				time.Sleep(checkInterval)
				continue
			}

			var wg sync.WaitGroup
			for _, host := range hosts {
				wg.Add(1)
				go func(h string) {
					defer wg.Done()
					checkHost(h, blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA, checksTotal)
				}(host)
			}
			wg.Wait()
			lastCheckTime.SetToCurrentTime()

			time.Sleep(checkInterval)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("rummyd listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "rummyd: %v\n", err)
		os.Exit(1)
	}
}

// tailscaleStatus is the subset of "tailscale status --json" we care about.
type tailscaleStatus struct {
	Peer map[string]tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	HostName string `json:"HostName"`
	Online   bool   `json:"Online"`
}

// hostLocation maps airport-code prefixes from exeprox hostnames to coordinates.
type hostLocation struct {
	lat, lon float64
	city     string
}

var locationByPrefix = map[string]hostLocation{
	"lax": {33.9425, -118.4081, "Los Angeles"},
	"sjc": {37.3639, -121.9289, "San Jose"},
	"dal": {32.8998, -97.0403, "Dallas"},
	"nyc": {40.6413, -73.7781, "New York"},
	"fra": {50.0379, 8.5622, "Frankfurt"},
	"lon": {51.4700, -0.4543, "London"},
	"tyo": {35.5494, 139.7798, "Tokyo"},
	"syd": {-33.9461, 151.1772, "Sydney"},
}

// locationForHost extracts the airport code from a hostname like "exeprox-lax2-prod-01"
// and returns the coordinates. Returns zero values if unknown.
func locationForHost(host string) (lat, lon float64, city string) {
	// hostname format: exeprox-{code}{N}-{stage}-{num}
	parts := strings.SplitN(host, "-", 3)
	if len(parts) < 2 {
		return 0, 0, ""
	}
	code := strings.TrimRight(parts[1], "0123456789")
	if loc, ok := locationByPrefix[code]; ok {
		return loc.lat, loc.lon, loc.city
	}
	return 0, 0, ""
}

// findExeproxHosts runs "tailscale status --json" and returns all online exeprox-*-prod-* hostnames.
func findExeproxHosts() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status --json: %w", err)
	}

	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("parsing tailscale status: %w", err)
	}

	var hosts []string
	for _, peer := range status.Peer {
		if strings.HasPrefix(peer.HostName, "exeprox-") && strings.Contains(peer.HostName, "-prod-") && peer.Online {
			hosts = append(hosts, peer.HostName)
		}
	}
	return hosts, nil
}

// checkHost SSH's to the given host and curls blog.exe.dev/debug/gitsha.
func checkHost(host string, blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA *prometheus.GaugeVec, checksTotal *prometheus.CounterVec) {
	// 30s overall deadline: covers SSH connect + remote curl + any hangs.
	// SSH ConnectTimeout=10 covers TCP connect; curl --max-time=10 covers
	// the HTTP request; the context is a hard backstop for the whole thing.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()

	// curl -w '\n%{time_total}' appends the HTTP time_total (seconds) after
	// the response body, so we get the SHA on the first line and the curl
	// latency (excluding SSH overhead) on the last line.
	// The remote command is passed as a single string so that the quotes
	// around the -w argument survive the remote shell.
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"ubuntu@"+host,
		`curl -sf --max-time 10 -w '\n%{time_total}' https://blog.exe.dev/debug/gitsha`,
	)
	out, err := cmd.Output()
	elapsed := time.Since(start)

	lat, lon, city := locationForHost(host)
	latStr := fmt.Sprintf("%.4f", lat)
	lonStr := fmt.Sprintf("%.4f", lon)

	if err != nil {
		log.Printf("check %s: FAIL after %s (%v)", host, elapsed, err)
		blogUp.WithLabelValues(host).Set(0)
		blogTotalLatency.WithLabelValues(host, latStr, lonStr, city).Set(elapsed.Seconds())
		checksTotal.WithLabelValues(host, "fail").Inc()
		return
	}

	// Parse: line 1 = body (git SHA), last line = curl time_total in seconds
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		log.Printf("check %s: FAIL (unexpected output: %q)", host, string(out))
		blogUp.WithLabelValues(host).Set(0)
		checksTotal.WithLabelValues(host, "fail").Inc()
		return
	}

	sha := strings.TrimSpace(lines[0])
	curlSeconds, parseErr := strconv.ParseFloat(strings.TrimSpace(lines[len(lines)-1]), 64)
	if parseErr != nil {
		log.Printf("check %s: FAIL (bad curl time: %q)", host, lines[len(lines)-1])
		blogUp.WithLabelValues(host).Set(0)
		checksTotal.WithLabelValues(host, "fail").Inc()
		return
	}

	log.Printf("check %s: OK sha=%s curl=%.3fs total=%s", host, sha, curlSeconds, elapsed)
	blogUp.WithLabelValues(host).Set(1)
	blogCurlLatency.WithLabelValues(host, latStr, lonStr, city).Set(curlSeconds)
	blogTotalLatency.WithLabelValues(host, latStr, lonStr, city).Set(elapsed.Seconds())
	blogGitSHA.DeletePartialMatch(prometheus.Labels{"host": host})
	blogGitSHA.WithLabelValues(host, sha).Set(1)
	checksTotal.WithLabelValues(host, "ok").Inc()
}
