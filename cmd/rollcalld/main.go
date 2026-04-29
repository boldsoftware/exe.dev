// rollcalld compares online tailscale hosts against prometheus scrape
// targets and reports machines that are online but not being monitored.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var (
		listenAddr    string
		checkInterval time.Duration
		prometheusURL string
		once          bool
	)
	flag.StringVar(&listenAddr, "listen", ":9098", "listen address for metrics")
	flag.DurationVar(&checkInterval, "interval", 5*time.Minute, "check interval")
	flag.StringVar(&prometheusURL, "prometheus-url", "http://mon:9090", "prometheus base URL")
	flag.BoolVar(&once, "once", false, "run once, print results, and exit")
	flag.Parse()

	if once {
		runOnce(prometheusURL)
		return
	}

	registry := prometheus.NewRegistry()

	unmonitoredCount := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rollcall_unmonitored_hosts",
		Help: "Number of online tagged tailscale hosts not in any prometheus scrape config.",
	})
	unmonitoredInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rollcall_unmonitored_host_info",
		Help: "Info metric for each unmonitored host (value is always 1).",
	}, []string{"host"})
	monitoredCount := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rollcall_monitored_hosts_total",
		Help: "Total number of hosts in prometheus scrape configs.",
	})
	tailscaleCount := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rollcall_tailscale_hosts_total",
		Help: "Total number of online tagged tailscale hosts.",
	})
	lastCheckTime := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rollcall_last_check_timestamp_seconds",
		Help: "Unix timestamp of the last check run.",
	})

	registry.MustRegister(unmonitoredCount, unmonitoredInfo, monitoredCount, tailscaleCount, lastCheckTime)

	go func() {
		for {
			result, err := check(prometheusURL)
			if err != nil {
				log.Printf("check error: %v", err)
				time.Sleep(checkInterval)
				continue
			}

			unmonitoredInfo.Reset()
			unmonitoredCount.Set(float64(len(result.unmonitored)))
			for _, h := range result.unmonitored {
				unmonitoredInfo.WithLabelValues(h).Set(1)
			}
			monitoredCount.Set(float64(len(result.prometheusHosts)))
			tailscaleCount.Set(float64(len(result.tailscaleHosts)))
			lastCheckTime.SetToCurrentTime()

			if len(result.unmonitored) > 0 {
				log.Printf("unmonitored hosts (%d): %s", len(result.unmonitored), strings.Join(result.unmonitored, ", "))
			} else {
				log.Printf("all %d tagged hosts accounted for", len(result.tailscaleHosts))
			}

			time.Sleep(checkInterval)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("rollcalld listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "rollcalld: %v\n", err)
		os.Exit(1)
	}
}

type checkResult struct {
	tailscaleHosts  []string
	prometheusHosts map[string][]string // host -> list of job names
	unmonitored     []string            // in tailscale, not in prometheus
	stale           []string            // in prometheus, not in tailscale
}

func check(prometheusURL string) (*checkResult, error) {
	tsHosts, err := findTaggedOnlineHosts()
	if err != nil {
		return nil, fmt.Errorf("tailscale: %w", err)
	}

	promHosts, err := queryPrometheusHosts(prometheusURL)
	if err != nil {
		return nil, fmt.Errorf("prometheus: %w", err)
	}

	tsSet := make(map[string]bool, len(tsHosts))
	for _, h := range tsHosts {
		tsSet[h] = true
	}

	var unmonitored []string
	for _, h := range tsHosts {
		if _, ok := promHosts[h]; !ok && shouldMonitor(h) {
			unmonitored = append(unmonitored, h)
		}
	}
	sort.Strings(unmonitored)

	var stale []string
	for h := range promHosts {
		if !tsSet[h] {
			stale = append(stale, h)
		}
	}
	sort.Strings(stale)

	return &checkResult{
		tailscaleHosts:  tsHosts,
		prometheusHosts: promHosts,
		unmonitored:     unmonitored,
		stale:           stale,
	}, nil
}

func runOnce(prometheusURL string) {
	result, err := check(prometheusURL)
	if err != nil {
		log.Fatalf("check: %v", err)
	}

	fmt.Printf("Tailscale online tagged hosts (%d):\n", len(result.tailscaleHosts))
	for _, h := range result.tailscaleHosts {
		fmt.Printf("  %s\n", h)
	}

	sortedProm := make([]string, 0, len(result.prometheusHosts))
	for h := range result.prometheusHosts {
		sortedProm = append(sortedProm, h)
	}
	sort.Strings(sortedProm)
	fmt.Printf("\nPrometheus monitored hosts (%d):\n", len(result.prometheusHosts))
	for _, h := range sortedProm {
		fmt.Printf("  %-40s  jobs: %s\n", h, strings.Join(result.prometheusHosts[h], ", "))
	}

	fmt.Printf("\nUnmonitored — in tailscale, not in prometheus (%d):\n", len(result.unmonitored))
	if len(result.unmonitored) == 0 {
		fmt.Printf("  (none)\n")
	}
	for _, h := range result.unmonitored {
		fmt.Printf("  %s\n", h)
	}

	fmt.Printf("\nStale — in prometheus, not online in tailscale (%d):\n", len(result.stale))
	if len(result.stale) == 0 {
		fmt.Printf("  (none)\n")
	}
	for _, h := range result.stale {
		jobs := result.prometheusHosts[h]
		fmt.Printf("  %-40s  jobs: %s\n", h, strings.Join(jobs, ", "))
	}
}

// tailscaleStatus is the subset of "tailscale status --json" we care about.
type tailscaleStatus struct {
	Self tailscalePeer            `json:"Self"`
	Peer map[string]tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	HostName string   `json:"HostName"`
	Online   bool     `json:"Online"`
	Tags     []string `json:"Tags"`
}

// findTaggedOnlineHosts returns sorted hostnames of all online tagged-device peers.
func findTaggedOnlineHosts() ([]string, error) {
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
	if len(status.Self.Tags) > 0 && status.Self.Online {
		hosts = append(hosts, status.Self.HostName)
	}
	for _, peer := range status.Peer {
		if peer.Online && len(peer.Tags) > 0 {
			hosts = append(hosts, peer.HostName)
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

// queryPrometheusHosts queries the Prometheus API for the "up" metric to find
// all actively monitored hosts, returning a map of hostname -> list of job names.
func queryPrometheusHosts(baseURL string) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	u := baseURL + "/api/v1/query?query=" + url.QueryEscape("up")
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying prometheus: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: status=%s", result.Status)
	}

	hosts := make(map[string][]string)
	for _, r := range result.Data.Result {
		instance := r.Metric["instance"]
		job := r.Metric["job"]
		host := normalizeTarget(instance)
		if host == "" {
			continue
		}
		hosts[host] = appendUnique(hosts[host], job)
	}
	return hosts, nil
}

// normalizeTarget extracts a tailscale hostname from a prometheus target string.
// Returns "" for targets that aren't tailscale hosts (localhost, public domains).
func normalizeTarget(target string) string {
	host := target

	// Strip port
	if i := strings.LastIndex(host, ":"); i != -1 && !strings.Contains(host, "[") {
		host = host[:i]
	}

	// Strip trailing dot and tailscale domain suffix
	host = strings.TrimSuffix(host, ".")
	host = strings.TrimSuffix(host, ".crocodile-vector.ts.net")

	if host == "localhost" {
		return ""
	}

	// After stripping the tailscale suffix, any remaining dots indicate
	// a public domain (e.g. blog.exe.dev) which isn't a tailscale host.
	if strings.Contains(host, ".") {
		return ""
	}

	return host
}

// shouldMonitor returns true for hosts we care about tracking.
// Single-word hosts (no "-") are infra singletons managed separately.
// Staging hosts are lower priority and excluded.
func shouldMonitor(host string) bool {
	if !strings.Contains(host, "-") {
		return false
	}
	if strings.Contains(host, "-staging-") {
		return false
	}
	// Decommissioned hosts that are still online but intentionally unmonitored.
	switch host {
	case "docker-01", "docker-02", "sketch-dev":
		return false
	}
	return true
}

func appendUnique(ss []string, s string) []string {
	for _, existing := range ss {
		if existing == s {
			return ss
		}
	}
	return append(ss, s)
}
