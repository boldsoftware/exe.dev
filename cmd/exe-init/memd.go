// memd: in-guest single-shot memory-stat server.
//
// Wire protocol: client opens a connection, writes "GET memstat\n", server
// replies with one line of JSON terminated by "\n" and closes the
// connection. No persistent connections, no auth (the host owns the unix
// socket; trust boundary is unix-socket perms).
//
// All numeric fields use the kernel's native units: meminfo is reported in
// kB (matching /proc/meminfo), vmstat counters are raw page/event counts,
// PSI averages are a percentage with 2 decimal places (matching
// /proc/pressure/memory). Missing optional inputs (e.g. PSI on old
// kernels without CONFIG_PSI) are reported in `errors` rather than
// failing the response.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// MemdProtocolVersion is the wire format version. Bumped on incompatible
// changes (field semantics or removals; additive fields don't bump).
const MemdProtocolVersion = 1

// MemdRequest is the single-line request the client sends.
const MemdRequest = "GET memstat\n"

// MemdSample is the JSON document memd returns.
type MemdSample struct {
	Version int `json:"version"`
	// Auth is reserved for future use. Always empty in v0; the host trusts
	// the unix-socket perms on the CH hybrid-vsock socket.
	Auth       string    `json:"auth,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	UptimeSec  float64   `json:"uptime_sec"`

	// Meminfo: kB units, mirrors /proc/meminfo line names. Only fields the
	// host actually consumes; we don't ship the whole file.
	Meminfo map[string]uint64 `json:"meminfo"`

	// Vmstat: raw counts. Only fields used by the host policy:
	// workingset_refault_file, workingset_refault_anon, pgmajfault.
	Vmstat map[string]uint64 `json:"vmstat"`

	// PSI for the memory cgroup. Each line is averaged over 10/60/300s.
	// We expose `some` and `full` with their avg10/avg60/avg300 percent
	// values (matching /proc/pressure/memory). Empty when missing.
	PSI map[string]MemdPSILine `json:"psi"`

	// Errors collects non-fatal read failures (typically missing files on
	// kernels without PSI support). Clients should treat the rest of the
	// sample as valid.
	Errors []string `json:"errors,omitempty"`
}

// MemdPSILine is one line of /proc/pressure/memory.
type MemdPSILine struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	// Total is the cumulative microseconds the cgroup has been stalled.
	// Useful for delta calculations on the host.
	Total uint64 `json:"total"`
}

// memdMeminfoFields is the curated set of /proc/meminfo lines we expose.
// Anything else can be added later without bumping the protocol version.
var memdMeminfoFields = []string{
	"MemTotal", "MemFree", "MemAvailable",
	"Buffers", "Cached", "SwapCached",
	"Active", "Inactive",
	"Active(anon)", "Inactive(anon)",
	"Active(file)", "Inactive(file)",
	"Unevictable", "Mlocked",
	"SwapTotal", "SwapFree",
	"Dirty", "Writeback",
	"AnonPages", "Mapped", "Shmem",
	"KReclaimable", "Slab", "SReclaimable", "SUnreclaim",
	"PageTables", "NFS_Unstable", "Bounce",
	"AnonHugePages", "ShmemHugePages", "FilePmdMapped",
	"HugePages_Total", "HugePages_Free", "HugePages_Rsvd", "HugePages_Surp",
	"Hugepagesize", "Hugetlb",
}

var memdVmstatFields = []string{
	"workingset_refault_file",
	"workingset_refault_anon",
	"workingset_activate_file",
	"workingset_activate_anon",
	"pgmajfault",
	"pgfault",
	"pswpin", "pswpout",
	"nr_dirty", "nr_writeback",
}

// memdMeminfoFieldSet is built once for O(1) lookup during parse.
var memdMeminfoFieldSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(memdMeminfoFields))
	for _, k := range memdMeminfoFields {
		m[k] = struct{}{}
	}
	return m
}()

var memdVmstatFieldSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(memdVmstatFields))
	for _, k := range memdVmstatFields {
		m[k] = struct{}{}
	}
	return m
}()

// memdProcRoot is overridable by tests ("" → /proc).
var memdProcRoot = ""

func memdProcPath(p string) string {
	root := memdProcRoot
	if root == "" {
		root = "/proc"
	}
	return root + p
}

// collectMemdSample reads the curated /proc files and assembles a sample.
// Returns a partial sample with errs populated for non-fatal failures.
func collectMemdSample(now time.Time) MemdSample {
	s := MemdSample{
		Version:    MemdProtocolVersion,
		CapturedAt: now.UTC(),
		Meminfo:    map[string]uint64{},
		Vmstat:     map[string]uint64{},
		PSI:        map[string]MemdPSILine{},
	}

	if b, err := os.ReadFile(memdProcPath("/uptime")); err == nil {
		parts := strings.Fields(string(b))
		if len(parts) > 0 {
			if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
				s.UptimeSec = v
			}
		}
	} else {
		s.Errors = append(s.Errors, fmt.Sprintf("uptime: %v", err))
	}

	if b, err := os.ReadFile(memdProcPath("/meminfo")); err == nil {
		parseMemdMeminfo(string(b), s.Meminfo)
	} else {
		s.Errors = append(s.Errors, fmt.Sprintf("meminfo: %v", err))
	}

	if b, err := os.ReadFile(memdProcPath("/vmstat")); err == nil {
		parseMemdVmstat(string(b), s.Vmstat)
	} else {
		s.Errors = append(s.Errors, fmt.Sprintf("vmstat: %v", err))
	}

	if b, err := os.ReadFile(memdProcPath("/pressure/memory")); err == nil {
		parseMemdPSI(string(b), s.PSI)
	} else {
		// PSI is optional (kernels without CONFIG_PSI). Don't fail.
		s.Errors = append(s.Errors, fmt.Sprintf("pressure/memory: %v", err))
	}

	return s
}

// parseMemdMeminfo extracts curated keys. Each line:
//
//	"MemTotal:        16291300 kB"
func parseMemdMeminfo(text string, out map[string]uint64) {
	for _, line := range strings.Split(text, "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		if _, ok := memdMeminfoFieldSet[key]; !ok {
			continue
		}
		rest := strings.TrimSpace(line[colon+1:])
		rest = strings.TrimSuffix(rest, " kB")
		v, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			continue
		}
		out[key] = v
	}
}

// parseMemdVmstat parses lines of "key value\n".
func parseMemdVmstat(text string, out map[string]uint64) {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		if _, ok := memdVmstatFieldSet[parts[0]]; !ok {
			continue
		}
		v, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		out[parts[0]] = v
	}
}

// parseMemdPSI parses /proc/pressure/memory:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
func parseMemdPSI(text string, out map[string]MemdPSILine) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		kind := parts[0]
		if kind != "some" && kind != "full" {
			continue
		}
		var pl MemdPSILine
		for _, kv := range parts[1:] {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			switch kv[:eq] {
			case "avg10":
				pl.Avg10, _ = strconv.ParseFloat(kv[eq+1:], 64)
			case "avg60":
				pl.Avg60, _ = strconv.ParseFloat(kv[eq+1:], 64)
			case "avg300":
				pl.Avg300, _ = strconv.ParseFloat(kv[eq+1:], 64)
			case "total":
				pl.Total, _ = strconv.ParseUint(kv[eq+1:], 10, 64)
			}
		}
		out[kind] = pl
	}
}

// memdHandshakeTimeout bounds how long we wait for the request line. Kept
// short — a healthy client sends 12 bytes immediately after dialing.
const memdHandshakeTimeout = 5 * time.Second

// memdResponseTimeout bounds the entire serve including read+write. 5s is
// matched to the host's per-poll-cycle deadline.
const memdResponseTimeout = 5 * time.Second

// serveMemdConn handles a single client. Single-shot: read one line, write
// one JSON line, close.
func serveMemdConn(c io.ReadWriteCloser, now func() time.Time, setDeadline func(time.Time) error) error {
	defer c.Close()
	if setDeadline != nil {
		_ = setDeadline(time.Now().Add(memdHandshakeTimeout))
	}
	br := bufio.NewReader(io.LimitReader(c, 64))
	line, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	if line != MemdRequest {
		return fmt.Errorf("unexpected request: %q", line)
	}
	if setDeadline != nil {
		_ = setDeadline(time.Now().Add(memdResponseTimeout))
	}
	sample := collectMemdSample(now())
	enc := json.NewEncoder(c)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&sample); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return nil
}

// errMemdShutdown is returned by run loops on graceful shutdown. Internal.
var errMemdShutdown = errors.New("memd: shutdown")
