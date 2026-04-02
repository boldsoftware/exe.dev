// rummyd is a Real User Monitoring daemon that checks blog rendering
// across all exeprox machines by SSH'ing to each one and fetching
// https://blog.exe.dev/debug/gitsha.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
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

	// Cert issuance canary: on a daily basis, we use a unique domain name
	// (rummy-MM-DD.rummy.exe.cloud), which should cause us to issue a new
	// TLS cert once a day, thereby verifying cert issuance is working.
	// *.rummy.exe.cloud is a CNAME to exeblog.exe.xyz.
	// Alert: "ACME cert issuance canary" in observability/dashboards.mts.
	certCanaryUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_cert_canary_up",
		Help: "Whether the daily cert issuance canary domain is reachable with a valid TLS cert (1=up, 0=down).",
	})
	certCanaryLatency := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_cert_canary_latency_seconds",
		Help: "Latency of the cert issuance canary request.",
	})
	certCanaryLastCheck := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_cert_canary_last_check_timestamp_seconds",
		Help: "Unix timestamp of the last cert canary check.",
	})

	registry.MustRegister(blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA, checksTotal, lastCheckTime, certCanaryUp, certCanaryLatency, certCanaryLastCheck)

	status := &statusPage{}

	// Cert issuance canary loop — runs once per hour.
	go func() {
		for {
			checkCertCanary(certCanaryUp, certCanaryLatency, certCanaryLastCheck, status)
			time.Sleep(1 * time.Hour)
		}
	}()

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
					checkHost(h, blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA, checksTotal, status)
				}(host)
			}
			wg.Wait()
			lastCheckTime.SetToCurrentTime()
			status.setLastCheck(time.Now())

			time.Sleep(checkInterval)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /{$}", status.handleIndex)

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
	"atl": {33.6407, -84.4277, "Atlanta"},
	"chi": {41.9742, -87.9073, "Chicago"},
	"dal": {32.8998, -97.0403, "Dallas"},
	"dfw": {32.8998, -97.0403, "Dallas"},
	"dxb": {25.2532, 55.3657, "Dubai"},
	"fra": {50.0379, 8.5622, "Frankfurt"},
	"gru": {-23.4356, -46.4731, "São Paulo"},
	"hkg": {22.3080, 113.9185, "Hong Kong"},
	"iad": {38.9531, -77.4565, "Washington DC"},
	"jnb": {-26.1367, 28.2411, "Johannesburg"},
	"lax": {33.9425, -118.4081, "Los Angeles"},
	"lga": {40.7769, -73.8740, "New York"},
	"lhr": {51.4700, -0.4543, "London"},
	"lon": {51.4700, -0.4543, "London"},
	"mia": {25.7959, -80.2870, "Miami"},
	"nyc": {40.6413, -73.7781, "New York"},
	"otp": {44.5711, 26.0850, "Bucharest"},
	"pdx": {45.5898, -122.5951, "Portland"},
	"sea": {47.4502, -122.3088, "Seattle"},
	"sin": {1.3644, 103.9915, "Singapore"},
	"sjc": {37.3639, -121.9289, "San Jose"},
	"syd": {-33.9461, 151.1772, "Sydney"},
	"tyo": {35.5494, 139.7798, "Tokyo"},
	"yyz": {43.6777, -79.6248, "Toronto"},
}

// locationForHost extracts the airport code from an exeprox hostname and
// returns the coordinates. Supports hostname formats:
//
//	exeprox-lax2-prod-01   → lax
//	exeprox-atl-na-01      → atl
//	exeprox-na-chi-01      → chi
//
// Returns zero values if unknown.
func locationForHost(host string) (lat, lon float64, city string) {
	parts := strings.Split(host, "-")
	if len(parts) < 3 {
		return 0, 0, ""
	}
	// "exeprox-na-{code}-{num}" → code is parts[2]
	code := strings.TrimRight(parts[1], "0123456789")
	if code == "na" && len(parts) >= 4 {
		code = parts[2]
	}
	if loc, ok := locationByPrefix[code]; ok {
		return loc.lat, loc.lon, loc.city
	}
	return 0, 0, ""
}

// findExeproxHosts runs "tailscale status --json" and returns all online
// production exeprox hostnames (matching -prod- or -na-, excluding -staging-).
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
		if !strings.HasPrefix(peer.HostName, "exeprox-") || !peer.Online {
			continue
		}
		if strings.Contains(peer.HostName, "-staging-") {
			continue
		}
		hosts = append(hosts, peer.HostName)
	}
	return hosts, nil
}

// statusPage holds the latest check results for the web UI.
type statusPage struct {
	mu          sync.Mutex
	hosts       map[string]hostStatus
	certCanary  certCanaryStatus
	lastCheckAt time.Time
}

type hostStatus struct {
	Host         string
	City         string
	Up           bool
	CurlLatency  float64 // seconds
	TotalLatency float64 // seconds
	SHA          string
	CheckedAt    time.Time
	Error        string
}

type certCanaryStatus struct {
	Domain    string
	Up        bool
	Latency   float64 // seconds
	CheckedAt time.Time
	Error     string
}

type statusData struct {
	Hosts      []hostStatus
	CertCanary certCanaryStatus
	LastCheck  time.Time
	Now        time.Time
}

func (s *statusPage) setHost(hs hostStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts == nil {
		s.hosts = make(map[string]hostStatus)
	}
	s.hosts[hs.Host] = hs
}

func (s *statusPage) setCertCanary(cs certCanaryStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.certCanary = cs
}

func (s *statusPage) setLastCheck(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheckAt = t
}

func (s *statusPage) snapshot() statusData {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]hostStatus, 0, len(s.hosts))
	for _, h := range s.hosts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].City != hosts[j].City {
			return hosts[i].City < hosts[j].City
		}
		return hosts[i].Host < hosts[j].Host
	})
	return statusData{
		Hosts:      hosts,
		CertCanary: s.certCanary,
		LastCheck:  s.lastCheckAt,
		Now:        time.Now(),
	}
}

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := time.Since(t).Truncate(time.Second)
		return d.String() + " ago"
	},
	"ms": func(s float64) string {
		return fmt.Sprintf("%.0fms", s*1000)
	},
}).Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>rummyd</title>
<meta http-equiv="refresh" content="60">
<style>
  body { font-family: -apple-system, system-ui, sans-serif; margin: 2em; background: #fff; color: #111; }
  h1 { font-size: 1.4em; }
  h2 { font-size: 1.1em; margin-top: 1.5em; }
  table { border-collapse: collapse; width: 100%; max-width: 900px; }
  th, td { text-align: left; padding: 6px 12px; border-bottom: 1px solid #ddd; }
  th { background: #f5f5f5; font-weight: 600; }
  .up { color: #16a34a; }
  .down { color: #dc2626; font-weight: 600; }
  .meta { color: #666; font-size: 0.85em; margin-top: 0.5em; }
  code { background: #f0f0f0; padding: 2px 5px; border-radius: 3px; font-size: 0.9em; }
</style>
</head>
<body>
<h1>rummyd — Real User Monitoring</h1>
<p class="meta">Last blog check: {{ago .LastCheck}} · Page auto-refreshes every 60s</p>

<h2>Blog Checks (via exeprox SSH)</h2>
<table>
<tr><th>Host</th><th>City</th><th>Status</th><th>Curl Latency</th><th>Total Latency</th><th>Git SHA</th><th>Checked</th></tr>
{{range .Hosts}}
<tr>
  <td><code>{{.Host}}</code></td>
  <td>{{.City}}</td>
  <td>{{if .Up}}<span class="up">UP</span>{{else}}<span class="down">DOWN{{if .Error}} — {{.Error}}{{end}}</span>{{end}}</td>
  <td>{{if .Up}}{{ms .CurlLatency}}{{else}}—{{end}}</td>
  <td>{{ms .TotalLatency}}</td>
  <td>{{if .SHA}}<code>{{.SHA}}</code>{{else}}—{{end}}</td>
  <td>{{ago .CheckedAt}}</td>
</tr>
{{end}}
</table>

<h2>ACME Cert Issuance Canary</h2>
<table>
<tr><th>Domain</th><th>Status</th><th>Latency</th><th>Checked</th></tr>
<tr>
  <td><code>{{.CertCanary.Domain}}</code></td>
  <td>{{if .CertCanary.Up}}<span class="up">UP</span>{{else}}<span class="down">DOWN{{if .CertCanary.Error}} — {{.CertCanary.Error}}{{end}}</span>{{end}}</td>
  <td>{{if .CertCanary.Up}}{{ms .CertCanary.Latency}}{{else}}—{{end}}</td>
  <td>{{ago .CertCanary.CheckedAt}}</td>
</tr>
</table>
</body>
</html>
`))

func (s *statusPage) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, s.snapshot())
}

// checkHost SSH's to the given host and curls blog.exe.dev/debug/gitsha.
func checkHost(host string, blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA *prometheus.GaugeVec, checksTotal *prometheus.CounterVec, sp *statusPage) {
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
		`curl -sSf --max-time 10 -w '\n%{time_total}' https://blog.exe.dev/debug/gitsha`,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	elapsed := time.Since(start)

	lat, lon, city := locationForHost(host)
	latStr := fmt.Sprintf("%.4f", lat)
	lonStr := fmt.Sprintf("%.4f", lon)

	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			switch ee.ExitCode() {
			case 6:
				detail = " (DNS resolution failed)"
			case 22:
				detail = " (HTTP error from blog)"
			case 28:
				detail = " (curl timeout)"
			case 35:
				detail = " (SSL connect error)"
			case 60:
				detail = " (SSL certificate problem)"
			case 255:
				detail = " (SSH connection failed)"
			}
		}
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			log.Printf("check %s: FAIL after %s (%v)%s: %s", host, elapsed, err, detail, stderrStr)
		} else {
			log.Printf("check %s: FAIL after %s (%v)%s", host, elapsed, err, detail)
		}
		blogUp.WithLabelValues(host).Set(0)
		blogTotalLatency.WithLabelValues(host, latStr, lonStr, city).Set(elapsed.Seconds())
		checksTotal.WithLabelValues(host, "fail").Inc()
		sp.setHost(hostStatus{Host: host, City: city, Up: false, TotalLatency: elapsed.Seconds(), CheckedAt: time.Now(), Error: strings.TrimSpace(detail)})
		return
	}

	// Parse: line 1 = body (git SHA), last line = curl time_total in seconds
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		log.Printf("check %s: FAIL (unexpected output: %q)", host, string(out))
		blogUp.WithLabelValues(host).Set(0)
		checksTotal.WithLabelValues(host, "fail").Inc()
		sp.setHost(hostStatus{Host: host, City: city, Up: false, CheckedAt: time.Now(), Error: "unexpected output"})
		return
	}

	sha := strings.TrimSpace(lines[0])
	curlSeconds, parseErr := strconv.ParseFloat(strings.TrimSpace(lines[len(lines)-1]), 64)
	if parseErr != nil {
		log.Printf("check %s: FAIL (bad curl time: %q)", host, lines[len(lines)-1])
		blogUp.WithLabelValues(host).Set(0)
		checksTotal.WithLabelValues(host, "fail").Inc()
		sp.setHost(hostStatus{Host: host, City: city, Up: false, CheckedAt: time.Now(), Error: "bad curl time"})
		return
	}

	log.Printf("check %s: OK sha=%s curl=%.3fs total=%s", host, sha, curlSeconds, elapsed)
	blogUp.WithLabelValues(host).Set(1)
	blogCurlLatency.WithLabelValues(host, latStr, lonStr, city).Set(curlSeconds)
	blogTotalLatency.WithLabelValues(host, latStr, lonStr, city).Set(elapsed.Seconds())
	blogGitSHA.DeletePartialMatch(prometheus.Labels{"host": host})
	blogGitSHA.WithLabelValues(host, sha).Set(1)
	checksTotal.WithLabelValues(host, "ok").Inc()
	sp.setHost(hostStatus{Host: host, City: city, Up: true, CurlLatency: curlSeconds, TotalLatency: elapsed.Seconds(), SHA: sha, CheckedAt: time.Now()})
}

// checkCertCanary fetches https://rummy-MM-DD.rummy.exe.cloud/ with a fresh
// TLS handshake. The wildcard CNAME points to exeblog.exe.xyz, so the request
// should succeed if cert issuance (ACME) is working. By rotating the subdomain
// daily, we force a new certificate to be issued each day.
func checkCertCanary(up, latencyGauge, lastCheck prometheus.Gauge, sp *statusPage) {
	domain := fmt.Sprintf("rummy-%s.rummy.exe.cloud", time.Now().UTC().Format("01-02"))
	url := "https://" + domain + "/"

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}

	start := time.Now()
	resp, err := client.Get(url)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("cert canary %s: FAIL after %s: %v", domain, elapsed, err)
		up.Set(0)
		lastCheck.SetToCurrentTime()
		sp.setCertCanary(certCanaryStatus{Domain: domain, Up: false, Latency: elapsed.Seconds(), CheckedAt: time.Now(), Error: err.Error()})
		return
	}
	resp.Body.Close()

	log.Printf("cert canary %s: OK (HTTP %d) in %s", domain, resp.StatusCode, elapsed)
	up.Set(1)
	latencyGauge.Set(elapsed.Seconds())
	lastCheck.SetToCurrentTime()
	sp.setCertCanary(certCanaryStatus{Domain: domain, Up: true, Latency: elapsed.Seconds(), CheckedAt: time.Now()})
}
