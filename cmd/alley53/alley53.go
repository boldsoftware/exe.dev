// Command alley53 is a standalone local DNS server for exe.cloud development and testing.
//
// When started, it:
//   - Sets up loopback IP aliases (127.21.0.0/24)
//   - Configures DNS resolution for *.exe.cloud:
//   - macOS: /etc/resolver/exe.cloud
//   - Linux: iptables DNAT redirect from port 53 to 5335
//   - Runs a DNS server on 127.21.0.0:5335
//   - Runs an HTTP API and web UI for managing DNS records
//
// When stopped (SIGINT/SIGTERM), it tears down the loopback aliases and resolver config.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"exe.dev/domz"
	"exe.dev/logging"
	"exe.dev/publicips"
	"exe.dev/stage"
	"github.com/go4org/hashtriemap"
)

const (
	// dnsIP is the IP address the DNS server binds to.
	dnsIP = "127.21.0.0"

	// dnsPort is the port the DNS server listens on.
	// Can't use 53 (requires root), can't use 5353 (mDNSResponder hijacks it).
	dnsPort = 5335

	// boxHost is the domain suffix we handle.
	boxHost = "exe.cloud"
)

// numShards returns the number of IP shards for local development.
var numShards = sync.OnceValue(func() int {
	n := stage.Local().NumShards
	if n > 253 {
		panic("numShards cannot exceed 253 (must fit in 127.21.0.0/24)")
	}
	return n
})

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alley53 must be run as root")
	}

	httpAddr := flag.String("http", ":5380", "HTTP API and web UI address")
	flag.Parse()

	plat, err := getPlatform()
	if err != nil {
		return err
	}

	logging.SetupLogger(stage.Local(), nil)
	slog.Info("starting alley53", "platform", runtime.GOOS)

	// Set up local DNS infrastructure
	if err := plat.Setup(); err != nil {
		return fmt.Errorf("failed to set up local DNS: %w", err)
	}
	slog.Info("local DNS infrastructure configured")

	// Create server
	srv := newServer()

	// Start DNS server
	if err := srv.startDNS(); err != nil {
		plat.Teardown()
		return fmt.Errorf("failed to start DNS server: %w", err)
	}
	slog.Info("DNS server started", "addr", dnsListenAddr())

	// Start HTTP API
	httpServer := &http.Server{
		Addr:    *httpAddr,
		Handler: srv.httpHandler(),
	}
	go func() {
		slog.Info("HTTP server starting", "addr", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	slog.Info("shutting down...")

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)

	// Shutdown DNS server
	srv.stopDNS(ctx)

	// Tear down local DNS infrastructure
	if err := plat.Teardown(); err != nil {
		slog.WarnContext(ctx, "failed to tear down local DNS", "error", err)
	}

	slog.InfoContext(ctx, "shutdown complete")
	return nil
}

// dnsListenAddr returns the full address for the DNS server.
func dnsListenAddr() string {
	return fmt.Sprintf("%s:%d", dnsIP, dnsPort)
}

// server holds the DNS server state and record store.
type server struct {
	records   hashtriemap.HashTrieMap[string, net.IP]
	dnsServer *dns.Server
	dnsConn   net.PacketConn
}

func newServer() *server {
	s := &server{}
	// Pre-register shard hostnames so the exed server can
	// resolve them at startup to build the IP→shard map.
	// This mirrors prod where Route53 has A records for s001.exe.xyz, etc.
	for shard := 1; shard <= numShards(); shard++ {
		name := publicips.ShardSub(shard)
		ip := net.IPv4(127, 21, 0, byte(shard))
		s.records.Store(name, ip)
	}
	return s
}

// startDNS starts the DNS server.
func (s *server) startDNS() error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleDNS)

	addr := dnsListenAddr()
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.dnsConn = conn
	s.dnsServer = &dns.Server{
		PacketConn: conn,
		Net:        "udp",
		Handler:    mux,
	}

	go func() {
		if err := s.dnsServer.ActivateAndServe(); err != nil && !strings.Contains(err.Error(), "bad listeners") {
			slog.Error("DNS server error", "error", err)
		}
	}()

	return nil
}

// stopDNS gracefully stops the DNS server.
func (s *server) stopDNS(ctx context.Context) {
	if s.dnsConn != nil {
		s.dnsServer.Shutdown(ctx)
	}
}

// handleDNS processes DNS queries.
func (s *server) handleDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) {
	slog.DebugContext(ctx, "DNS query", "remote", w.RemoteAddr(), "questions", r.Question)

	resp := new(dns.Msg)
	resp.Authoritative = true
	dnsutil.SetReply(resp, r)

	var knownHost bool
	for _, question := range r.Question {
		if dns.RRToType(question) != dns.TypeA {
			continue
		}

		header := question.Header()
		boxName := domz.Label(header.Name, boxHost)
		if boxName == "" {
			continue
		}
		knownHost = true

		ip := s.lookupIP(boxName)
		slog.DebugContext(ctx, "DNS response", "name", header.Name, "boxName", boxName, "ip", ip)

		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.Header{Name: header.Name, Class: header.Class},
			A:   ip,
		})
	}

	if !knownHost {
		resp.Rcode = dns.RcodeNameError
	}
	if _, err := resp.WriteTo(w); err != nil {
		slog.WarnContext(ctx, "DNS reply failed", "error", err)
	}
}

// lookupIP returns the IP for a box name.
// Default: returns 127.0.0.1 for unknown boxes.
func (s *server) lookupIP(boxName string) net.IP {
	if ip, ok := s.records.Load(boxName); ok {
		return ip
	}
	return net.IPv4(127, 0, 0, 1)
}

// allRecords returns all records as a sorted slice of name/IP pairs.
func (s *server) allRecords() []record {
	var records []record
	s.records.Range(func(name string, ip net.IP) bool {
		records = append(records, record{Name: name, IP: ip.String()})
		return true
	})
	slices.SortFunc(records, func(a, b record) int {
		return strings.Compare(a.Name, b.Name)
	})
	return records
}

type record struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// httpHandler returns the HTTP handler for the API and web UI.
func (s *server) httpHandler() http.Handler {
	mux := http.NewServeMux()

	// Web UI
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /ui", s.handleUI)
	mux.HandleFunc("POST /ui/add", s.handleUIAdd)
	mux.HandleFunc("POST /ui/delete", s.handleUIDelete)

	// REST API
	mux.HandleFunc("POST /upsert", s.handleUpsert)
	mux.HandleFunc("DELETE /record", s.handleDelete)
	mux.HandleFunc("GET /records", s.handleList)
	mux.HandleFunc("GET /health", s.handleHealth)

	return mux
}

// handleIndex serves the landing page with links.
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
    <title>alley53 - Local DNS Server</title>
    <style>
        body { font-family: system-ui, sans-serif; max-width: 600px; margin: 40px auto; padding: 0 20px; }
        h1 { color: #333; }
        a { color: #0066cc; }
        code { background: #f4f4f4; padding: 2px 6px; border-radius: 3px; }
        .section { margin: 20px 0; }
        ul { line-height: 1.8; }
    </style>
</head>
<body>
    <h1>alley53</h1>
    <p>Local DNS server for <code>*.exe.cloud</code> development.</p>

    <div class="section">
        <h2>Web UI</h2>
        <ul>
            <li><a href="/ui">Manage DNS Records</a> - View, add, and delete records</li>
        </ul>
    </div>

    <div class="section">
        <h2>REST API</h2>
        <ul>
            <li><a href="/records">GET /records</a> - List all records</li>
            <li><code>POST /upsert</code> - Create/update record <code>{"name": "box", "ip": "127.21.0.5"}</code></li>
            <li><code>DELETE /record?name=box</code> - Delete a record</li>
            <li><a href="/health">GET /health</a> - Health check</li>
        </ul>
    </div>
</body>
</html>`)
}

// handleUI serves the human-friendly record management page.
func (s *server) handleUI(w http.ResponseWriter, r *http.Request) {
	records := s.allRecords()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
    <title>alley53 - DNS Records</title>
    <style>
        body { font-family: system-ui, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; }
        h1 { color: #333; }
        table { width: 100%; border-collapse: collapse; margin: 20px 0; }
        th, td { text-align: left; padding: 10px; border-bottom: 1px solid #ddd; }
        th { background: #f8f8f8; }
        .add-form { background: #f8f8f8; padding: 20px; border-radius: 8px; margin: 20px 0; }
        input[type="text"] { padding: 8px; margin-right: 10px; border: 1px solid #ccc; border-radius: 4px; }
        button { padding: 8px 16px; background: #0066cc; color: white; border: none; border-radius: 4px; cursor: pointer; }
        button:hover { background: #0055aa; }
        .delete-btn { background: #cc3333; }
        .delete-btn:hover { background: #aa2222; }
        a { color: #0066cc; }
        .empty { color: #666; font-style: italic; }
    </style>
</head>
<body>
    <h1>DNS Records</h1>
    <p><a href="/">&larr; Back to index</a></p>

    <div class="add-form">
        <h3>Add Record</h3>
        <form method="POST" action="/ui/add">
            <input type="text" name="name" placeholder="boxname" required>
            <input type="text" name="ip" placeholder="127.21.0.X" required>
            <button type="submit">Add</button>
        </form>
    </div>

    <table>
        <tr><th>Name</th><th>IP</th><th>FQDN</th><th></th></tr>`)

	if len(records) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="empty">No records configured.</td></tr>`)
	}
	for _, rec := range records {
		fmt.Fprintf(w, `
        <tr>
            <td>%s</td>
            <td>%s</td>
            <td>%s.exe.cloud</td>
            <td>
                <form method="POST" action="/ui/delete" style="display:inline">
                    <input type="hidden" name="name" value="%s">
                    <button type="submit" class="delete-btn">Delete</button>
                </form>
            </td>
        </tr>`, rec.Name, rec.IP, rec.Name, rec.Name)
	}

	fmt.Fprint(w, `
    </table>
</body>
</html>`)
}

// handleUIAdd handles the web form for adding a record.
func (s *server) handleUIAdd(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	ipStr := r.FormValue("ip")

	if name == "" || ipStr == "" {
		http.Error(w, "name and ip are required", http.StatusBadRequest)
		return
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		http.Error(w, fmt.Sprintf("invalid IP: %s", ipStr), http.StatusBadRequest)
		return
	}

	s.records.Store(name, ip)
	slog.InfoContext(r.Context(), "upserted DNS record", "name", name, "ip", ipStr)
	http.Redirect(w, r, "/ui", http.StatusSeeOther)
}

// handleUIDelete handles the web form for deleting a record.
func (s *server) handleUIDelete(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	s.records.Delete(name)
	slog.InfoContext(r.Context(), "deleted DNS record", "name", name)
	http.Redirect(w, r, "/ui", http.StatusSeeOther)
}

// UpsertRequest is the request body for upserting a DNS record.
type UpsertRequest struct {
	Name string `json:"name"` // box name (e.g., "mybox")
	IP   string `json:"ip"`   // IP address (e.g., "127.21.0.5")
}

// handleUpsert creates or updates a DNS record (REST API).
func (s *server) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var req UpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	ip := net.ParseIP(req.IP)
	if ip == nil {
		http.Error(w, fmt.Sprintf("invalid IP: %s", req.IP), http.StatusBadRequest)
		return
	}

	s.records.Store(req.Name, ip)
	slog.InfoContext(r.Context(), "upserted DNS record", "name", req.Name, "ip", req.IP)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDelete removes a DNS record (REST API).
func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}

	s.records.Delete(name)
	slog.InfoContext(r.Context(), "deleted DNS record", "name", name)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleList returns all DNS records (REST API).
func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	records := s.allRecords()

	result := make(map[string]string, len(records))
	for _, rec := range records {
		result[rec.Name] = rec.IP
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleHealth returns a simple health check response.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Platform abstraction
// ---------------------------------------------------------------------------

// platform defines the OS-specific operations for setting up local DNS.
type platform interface {
	Setup() error
	Teardown() error
}

func getPlatform() (platform, error) {
	switch runtime.GOOS {
	case "darwin":
		return darwinPlatform{}, nil
	case "linux":
		fmt.Println("Hi! This has never been run on linux before. If it works, great! Please delete this message. Otherwise...I apologize. Please fix it?")
		return linuxPlatform{}, nil
	default:
		return nil, fmt.Errorf("alley53 only works on macOS and Linux, not %s", runtime.GOOS)
	}
}

// runCmd runs a command directly (alley53 must be run as root).
func runCmd(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// macOS (darwin) implementation
// ---------------------------------------------------------------------------

type darwinPlatform struct{}

func (darwinPlatform) Setup() error {
	// Add loopback IP aliases one at a time (macOS requires individual aliases)
	for n := 0; n <= numShards(); n++ {
		ip := fmt.Sprintf("127.21.0.%d", n)
		if !darwinHasLoopbackAlias(ip) {
			if err := runCmd("ifconfig", "lo0", "alias", ip); err != nil {
				return fmt.Errorf("failed to add loopback alias %s: %w", ip, err)
			}
		}
		fmt.Printf("\r%d of %d", n, numShards())
	}
	fmt.Println()

	// Create resolver directory and file
	if err := os.MkdirAll("/etc/resolver", 0o755); err != nil {
		return fmt.Errorf("failed to create /etc/resolver: %w", err)
	}
	content := fmt.Sprintf("nameserver %s\nport %d\n", dnsIP, dnsPort)
	if err := os.WriteFile("/etc/resolver/exe.cloud", []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to create resolver file: %w", err)
	}

	return nil
}

func (darwinPlatform) Teardown() error {
	// Remove resolver file
	if err := os.Remove("/etc/resolver/exe.cloud"); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove resolver file", "error", err)
	}

	// Remove loopback IP aliases
	for n := 0; n <= numShards(); n++ {
		ip := fmt.Sprintf("127.21.0.%d", n)
		if darwinHasLoopbackAlias(ip) {
			if err := runCmd("ifconfig", "lo0", "-alias", ip); err != nil {
				slog.Warn("failed to remove loopback alias", "ip", ip, "error", err)
			}
		}
		fmt.Printf("\r%d of %d", n, numShards())
	}
	fmt.Println()

	return nil
}

func darwinHasLoopbackAlias(ip string) bool {
	out, err := exec.Command("ifconfig", "lo0").Output()
	if err != nil {
		return false
	}
	// Check for "inet IP " or "inet IP\n"
	return strings.Contains(string(out), "inet "+ip+" ") || strings.Contains(string(out), "inet "+ip+"\n")
}

// ---------------------------------------------------------------------------
// Linux implementation
// ---------------------------------------------------------------------------

type linuxPlatform struct{}

func (linuxPlatform) Setup() error {
	// Add entire /24 subnet to loopback in one command
	if !linuxHasLoopbackSubnet() {
		if err := runCmd("ip", "addr", "add", "127.21.0.0/24", "dev", "lo"); err != nil {
			return fmt.Errorf("failed to add loopback subnet: %w", err)
		}
	}
	slog.Info("loopback subnet configured", "subnet", "127.21.0.0/24")

	// Use iptables DNAT to redirect port 53 queries on 127.21.0.0/24 to our port.
	// This allows programs to query any 127.21.0.X:53 and have it forwarded.
	if err := runCmd("iptables", "-t", "nat", "-A", "OUTPUT",
		"-d", "127.21.0.0/24", "-p", "udp", "--dport", "53",
		"-j", "DNAT", "--to-destination", dnsListenAddr()); err != nil {
		return fmt.Errorf("failed to add iptables rule: %w", err)
	}
	slog.Info("iptables DNAT rule configured")

	return nil
}

func (linuxPlatform) Teardown() error {
	// Remove iptables rule (ignore error if rule doesn't exist)
	runCmd("iptables", "-t", "nat", "-D", "OUTPUT",
		"-d", "127.21.0.0/24", "-p", "udp", "--dport", "53",
		"-j", "DNAT", "--to-destination", dnsListenAddr())

	// Remove loopback subnet
	if linuxHasLoopbackSubnet() {
		if err := runCmd("ip", "addr", "del", "127.21.0.0/24", "dev", "lo"); err != nil {
			slog.Warn("failed to remove loopback subnet", "error", err)
		}
	}

	return nil
}

func linuxHasLoopbackSubnet() bool {
	out, err := exec.Command("ip", "addr", "show", "lo").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "inet 127.21.0.0/24")
}
