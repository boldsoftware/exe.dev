// rummyd is a Real User Monitoring daemon that checks blog rendering
// across all exeprox machines by hitting each one's /__exe.dev/blog
// endpoint, which fetches https://blog.exe.dev/debug/gitsha externally.
package main

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/ssh"
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
		Help: "Upstream latency (X-Upstream-Duration from exeprox) of fetching blog.exe.dev/debug/gitsha.",
	}, []string{"host", "latitude", "longitude", "city"})

	blogTotalLatency := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rummy_blog_total_latency_seconds",
		Help: "Total wall-clock latency of the check request to exeprox.",
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

	// SSH health check for exe.dev — verifies the SSH endpoint is up and
	// presenting the expected host key. Alert: "ssh exe.dev down" in
	// observability/dashboards.mts.
	sshUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_ssh_up",
		Help: "Whether exe.dev:22 is reachable and presenting the expected SSH host key (1=up, 0=down).",
	})
	sshLatency := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_ssh_latency_seconds",
		Help: "Latency of the SSH host key check to exe.dev.",
	})
	sshLastCheck := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rummy_ssh_last_check_timestamp_seconds",
		Help: "Unix timestamp of the last SSH check.",
	})

	registry.MustRegister(blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA, checksTotal, lastCheckTime, certCanaryUp, certCanaryLatency, certCanaryLastCheck, sshUp, sshLatency, sshLastCheck)

	status := &statusPage{}

	// Cert issuance canary loop — runs once per hour.
	go func() {
		for {
			checkCertCanary(certCanaryUp, certCanaryLatency, certCanaryLastCheck, status)
			time.Sleep(1 * time.Hour)
		}
	}()

	// SSH health check loop — runs every minute.
	go func() {
		for {
			checkSSH(sshUp, sshLatency, sshLastCheck, status)
			time.Sleep(1 * time.Minute)
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
	sshCheck    sshCheckStatus
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

type sshCheckStatus struct {
	Host      string
	Up        bool
	Latency   float64 // seconds
	CheckedAt time.Time
	Error     string
}

type statusData struct {
	Hosts      []hostStatus
	CertCanary certCanaryStatus
	SSHCheck   sshCheckStatus
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

func (s *statusPage) setSSHCheck(ss sshCheckStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sshCheck = ss
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
		SSHCheck:   s.sshCheck,
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

<h2>Blog Checks (via exeprox)</h2>
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

<h2>SSH Health Check</h2>
<table>
<tr><th>Host</th><th>Status</th><th>Latency</th><th>Checked</th></tr>
<tr>
  <td><code>{{.SSHCheck.Host}}</code></td>
  <td>{{if .SSHCheck.Up}}<span class="up">UP</span>{{else}}<span class="down">DOWN{{if .SSHCheck.Error}} — {{.SSHCheck.Error}}{{end}}</span>{{end}}</td>
  <td>{{if .SSHCheck.Up}}{{ms .SSHCheck.Latency}}{{else}}—{{end}}</td>
  <td>{{ago .SSHCheck.CheckedAt}}</td>
</tr>
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

// checkHost hits the exeprox's /__exe.dev/blog endpoint to check blog reachability.
// The exeprox fetches blog.exe.dev/debug/gitsha externally and reports the result.
func checkHost(host string, blogUp, blogCurlLatency, blogTotalLatency, blogGitSHA *prometheus.GaugeVec, checksTotal *prometheus.CounterVec, sp *statusPage) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()

	lat, lon, city := locationForHost(host)
	latStr := fmt.Sprintf("%.4f", lat)
	lonStr := fmt.Sprintf("%.4f", lon)

	failCheck := func(msg string, elapsed time.Duration) {
		log.Printf("check %s: FAIL (%s)", host, msg)
		blogUp.WithLabelValues(host).Set(0)
		blogTotalLatency.WithLabelValues(host, latStr, lonStr, city).Set(elapsed.Seconds())
		checksTotal.WithLabelValues(host, "fail").Inc()
		sp.setHost(hostStatus{Host: host, City: city, Up: false, TotalLatency: elapsed.Seconds(), CheckedAt: time.Now(), Error: msg})
	}

	url := fmt.Sprintf("http://%s/__exe.dev/blog/debug/gitsha", host)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		failCheck(fmt.Sprintf("request creation: %v", err), 0)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		failCheck(err.Error(), elapsed)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		failCheck(fmt.Sprintf("reading body: %v", err), elapsed)
		return
	}

	if resp.StatusCode != http.StatusOK {
		failCheck(fmt.Sprintf("HTTP %d", resp.StatusCode), elapsed)
		return
	}

	sha := strings.TrimSpace(string(body))
	if !validGitSHA(sha) {
		failCheck(fmt.Sprintf("invalid SHA: %s", sha), elapsed)
		return
	}

	// X-Upstream-Duration is the time the exeprox spent fetching from blog.exe.dev,
	// analogous to the old curl time_total (HTTP latency excluding SSH overhead).
	dur := resp.Header.Get("X-Upstream-Duration")
	if dur == "" {
		failCheck("missing X-Upstream-Duration header", elapsed)
		return
	}
	curlSeconds, err := strconv.ParseFloat(dur, 64)
	if err != nil {
		failCheck(fmt.Sprintf("bad X-Upstream-Duration: %v", err), elapsed)
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

// checkSSH connects to exe.dev:22, performs an SSH handshake, and verifies
// the host presents the expected RSA public key. This is an ssh-keyscan
// equivalent that alerts if the SSH endpoint goes down or the key changes.
func checkSSH(up, latencyGauge, lastCheck prometheus.Gauge, sp *statusPage) {
	const host = "exe.dev:22"

	start := time.Now()
	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		elapsed := time.Since(start)
		log.Printf("ssh check %s: FAIL dial: %v", host, err)
		up.Set(0)
		lastCheck.SetToCurrentTime()
		sp.setSSHCheck(sshCheckStatus{Host: host, Up: false, Latency: elapsed.Seconds(), CheckedAt: time.Now(), Error: err.Error()})
		return
	}

	sshConn, _, _, err := ssh.NewClientConn(conn, host, &ssh.ClientConfig{
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if !sshHostKeyMatchesExeDev(key) {
				return fmt.Errorf("ssh: host key mismatch")
			}
			return nil
		},
		Timeout: 10 * time.Second,
	})
	elapsed := time.Since(start)
	if sshConn != nil {
		sshConn.Close()
	} else {
		conn.Close()
	}
	// We only care that the SSH handshake completed and the host key matched.
	// Authentication failure is expected (we don't send credentials) and means
	// the server is up and presenting the correct key.
	if err != nil && !strings.Contains(err.Error(), "unable to authenticate") {
		log.Printf("ssh check %s: FAIL handshake: %v", host, err)
		up.Set(0)
		lastCheck.SetToCurrentTime()
		sp.setSSHCheck(sshCheckStatus{Host: host, Up: false, Latency: elapsed.Seconds(), CheckedAt: time.Now(), Error: err.Error()})
		return
	}

	log.Printf("ssh check %s: OK in %s", host, elapsed)
	up.Set(1)
	latencyGauge.Set(elapsed.Seconds())
	lastCheck.SetToCurrentTime()
	sp.setSSHCheck(sshCheckStatus{Host: host, Up: true, Latency: elapsed.Seconds(), CheckedAt: time.Now()})
}

// sshHostKeyMatchesExeDev checks whether the given SSH public key matches the
// expected exe.dev RSA host key. Handles ssh-rsa, rsa-sha2-256, rsa-sha2-512
// algorithm names and ssh.Certificate wrappers — all of which present the same
// underlying RSA key.
func sshHostKeyMatchesExeDev(key ssh.PublicKey) bool {
	// If it's a certificate, extract the underlying key.
	if cert, ok := key.(*ssh.Certificate); ok {
		key = cert.Key
	}
	// Extract the crypto public key for comparison.
	cpk, ok := key.(ssh.CryptoPublicKey)
	if !ok {
		return false
	}
	got, ok := cpk.CryptoPublicKey().(*rsa.PublicKey)
	if !ok {
		return false
	}
	want := exeDevSSHHostKey.(ssh.CryptoPublicKey).CryptoPublicKey().(*rsa.PublicKey)
	return got.Equal(want)
}

// exeDevSSHHostKey is the expected RSA host key for exe.dev:22.
// Obtained via: ssh-keyscan exe.dev
var exeDevSSHHostKey = mustParseRSAPublicKey(
	// ssh-rsa public key base64
	"AAAAB3NzaC1yc2EAAAADAQABAAABAQDEKtEcRW8OBtro5B/MG+EaisD+ZVwwHFa5m7M8wFwBlMmPJJssY+1aGBRW3b9InAeCnTU2Kt7gazqbg/9od1KnK6x5piQNVQZ4C/lrjsC2ScBrOydnw9ry9G2+voFCAk+dQGabIrIT6gqqDJNOqxgFiG/lA3Xx6KwpfwI2BH5f3ab2fHCR2BGAC5jlB2RJXPgly80hMxYEHqexhJxYRwC+deeLrQSG795we9rSzPmdz58t9+9jLTKkyyqWKe/hmBvty1AYrEmRsefu6/TUrIGi/UWJfa+RBIQtFgWqN6xT1F6rRwELeVOfwwr5tZbsmgWY5frZU3EOtVWcF7Ve3gfL",
)

func mustParseRSAPublicKey(b64 string) ssh.PublicKey {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(fmt.Sprintf("decode ssh host key base64: %v", err))
	}
	key, err := ssh.ParsePublicKey(data)
	if err != nil {
		panic(fmt.Sprintf("parse ssh host key: %v", err))
	}
	return key
}

var gitSHARe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func validGitSHA(s string) bool {
	return gitSHARe.MatchString(s)
}
