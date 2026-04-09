//go:build linux

package netns

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ConnEntry represents a single conntrack or ss connection.
type ConnEntry struct {
	Proto   string
	State   string
	Src     string
	Dst     string
	Sport   string
	Dport   string
	NATSrc  string // post-NAT source (if SNAT/masquerade)
	NATDst  string // post-NAT dest (if DNAT)
	NATSprt string
	NATDprt string
}

func (c ConnEntry) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-5s %-12s %s:%s → %s:%s", c.Proto, c.State, c.Src, c.Sport, c.Dst, c.Dport)
	if c.NATDst != "" || c.NATSrc != "" {
		natSrc := c.NATSrc
		natSprt := c.NATSprt
		if natSrc == "" {
			natSrc = c.Src
			natSprt = c.Sport
		}
		natDst := c.NATDst
		natDprt := c.NATDprt
		if natDst == "" {
			natDst = c.Dst
			natDprt = c.Dport
		}
		fmt.Fprintf(&b, "  NAT→ %s:%s → %s:%s", natSrc, natSprt, natDst, natDprt)
	}
	return b.String()
}

// LiveStream streams conntrack events from the instance's network namespace.
// It first prints a snapshot of existing connections, then streams new events.
// Blocks until ctx is cancelled.
func LiveStream(ctx context.Context, w io.Writer, instanceID string) error {
	ns := NsName(instanceID)
	vid := vmID(instanceID)

	// Verify the namespace exists.
	if _, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		return fmt.Errorf("netns %s not found: %w", ns, err)
	}

	fmt.Fprintf(w, "Live connections for %s (netns %s)\n", vid, ns)
	fmt.Fprintf(w, "VM: %s → gateway %s → ext %s → internet\n", VMIP, VMGateway, "10.99.x.x")
	fmt.Fprintf(w, "Press Ctrl-C to stop\n\n")

	// Try conntrack -E (real-time event stream). This is the best option
	// but requires conntrack to be installed.
	if err := streamConntrackEvents(ctx, w, ns); err != nil {
		// Fall back to polling conntrack -L.
		fmt.Fprintf(w, "conntrack -E unavailable (%v), falling back to polling\n\n", err)
		return pollConnections(ctx, w, ns)
	}
	return nil
}

// LiveStreamByVMID is like LiveStream but takes a vmid (e.g. "vm000003") instead of full instance ID.
func LiveStreamByVMID(ctx context.Context, w io.Writer, vid string) error {
	ns := "exe-" + vid

	if _, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		return fmt.Errorf("netns %s not found: %w", ns, err)
	}

	fmt.Fprintf(w, "Live connections for %s (netns %s)\n", vid, ns)
	fmt.Fprintf(w, "VM: %s → gateway %s → ext %s → internet\n", VMIP, VMGateway, "10.99.x.x")
	fmt.Fprintf(w, "Press Ctrl-C to stop\n\n")

	if err := streamConntrackEvents(ctx, w, ns); err != nil {
		fmt.Fprintf(w, "conntrack -E unavailable (%v), falling back to polling\n\n", err)
		return pollConnections(ctx, w, ns)
	}
	return nil
}

// streamConntrackEvents runs `conntrack -E` inside the namespace and streams
// parsed events to w. Returns an error immediately if conntrack is not available.
func streamConntrackEvents(ctx context.Context, w io.Writer, ns string) error {
	// First, print existing connections as a snapshot.
	snap, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"conntrack", "-L", "-o", "extended", "2>/dev/null").CombinedOutput()
	if err != nil {
		// conntrack not installed or no permissions.
		return fmt.Errorf("conntrack -L: %w", err)
	}

	existing := parseConntrackOutput(string(snap))
	if len(existing) > 0 {
		fmt.Fprintf(w, "── Existing connections (%d) ──\n", len(existing))
		for _, c := range existing {
			fmt.Fprintf(w, "  %s\n", c)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "── Streaming events ──")

	// Start the event stream.
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"conntrack", "-E", "-o", "extended")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("conntrack -E: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		event, entry := parseConntrackEvent(line)
		if event == "" {
			// Unparseable, print raw.
			fmt.Fprintf(w, "  %s  %s\n", timestamp(), line)
			continue
		}
		fmt.Fprintf(w, "  %s  %-8s %s\n", timestamp(), event, entry)
	}

	// Wait for process to exit (cancelled context kills it).
	_ = cmd.Wait()
	return ctx.Err()
}

// pollConnections falls back to periodic `conntrack -L` + `ss` polling.
func pollConnections(ctx context.Context, w io.Writer, ns string) error {
	seen := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try conntrack -L first, fall back to ss.
		var entries []ConnEntry
		snap, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
			"conntrack", "-L", "-o", "extended").CombinedOutput()
		if err == nil {
			entries = parseConntrackOutput(string(snap))
		} else {
			entries = pollSS(ctx, ns)
		}

		for _, c := range entries {
			key := c.Proto + ":" + c.Src + ":" + c.Sport + "→" + c.Dst + ":" + c.Dport
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			fmt.Fprintf(w, "  %s  NEW      %s\n", timestamp(), c)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pollSS uses `ss` as a last resort when conntrack is unavailable.
func pollSS(ctx context.Context, ns string) []ConnEntry {
	out, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"ss", "-tunp", "-o", "state", "established").CombinedOutput()
	if err != nil {
		return nil
	}

	var entries []ConnEntry
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := fields[0]
		if proto != "tcp" && proto != "udp" {
			continue
		}
		local := fields[3]
		peer := fields[4]
		lHost, lPort := splitHostPort(local)
		pHost, pPort := splitHostPort(peer)
		entries = append(entries, ConnEntry{
			Proto: proto,
			State: "ESTABLISHED",
			Src:   lHost,
			Sport: lPort,
			Dst:   pHost,
			Dport: pPort,
		})
	}
	return entries
}

// parseConntrackOutput parses `conntrack -L -o extended` output.
func parseConntrackOutput(output string) []ConnEntry {
	var entries []ConnEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "conntrack") {
			continue
		}
		entry := parseConntrackLine(line)
		if entry.Src != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseConntrackLine parses a single conntrack line like:
// ipv4     2 tcp      6 300 ESTABLISHED src=10.42.0.42 dst=1.2.3.4 sport=12345 dport=443 src=1.2.3.4 dst=10.99.0.2 sport=443 dport=12345 [ASSURED] mark=0 use=1
func parseConntrackLine(line string) ConnEntry {
	var entry ConnEntry
	fields := strings.Fields(line)

	// Find the protocol.
	for _, f := range fields {
		switch f {
		case "tcp", "udp", "icmp":
			entry.Proto = f
		}
	}

	// Find state (ESTABLISHED, SYN_SENT, etc.).
	for _, f := range fields {
		switch f {
		case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT",
			"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE",
			"LISTEN", "UNREPLIED", "ASSURED":
			if entry.State == "" {
				entry.State = f
			}
		}
	}
	if entry.State == "" {
		entry.State = "-"
	}

	// Parse key=value pairs. Conntrack lines have two direction tuples;
	// the first is the original direction (src/dst from VM's perspective),
	// the second is the reply direction (which reveals NAT translations).
	kvPairs := make([]map[string]string, 0, 2)
	current := make(map[string]string)
	seenSrc := false
	for _, f := range fields {
		if !strings.Contains(f, "=") {
			continue
		}
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		if k == "src" {
			if seenSrc {
				// Second src= means we're in the reply tuple.
				kvPairs = append(kvPairs, current)
				current = make(map[string]string)
			}
			seenSrc = true
		}
		current[k] = v
	}
	kvPairs = append(kvPairs, current)

	if len(kvPairs) >= 1 {
		entry.Src = kvPairs[0]["src"]
		entry.Dst = kvPairs[0]["dst"]
		entry.Sport = kvPairs[0]["sport"]
		entry.Dport = kvPairs[0]["dport"]
	}
	if len(kvPairs) >= 2 {
		// In the reply tuple, src/dst are swapped. If the reply src
		// differs from the original dst, there's DNAT. If the reply dst
		// differs from the original src, there's SNAT.
		replySrc := kvPairs[1]["src"]
		replyDst := kvPairs[1]["dst"]
		replySport := kvPairs[1]["sport"]
		replyDport := kvPairs[1]["dport"]

		if replySrc != entry.Dst {
			entry.NATDst = replySrc
			entry.NATDprt = replySport
		}
		if replyDst != entry.Src {
			entry.NATSrc = replyDst
			entry.NATSprt = replyDport
		}
	}

	return entry
}

// parseConntrackEvent parses a `conntrack -E` event line like:
// [NEW] tcp      6 120 SYN_SENT src=10.42.0.42 dst=1.2.3.4 ...
func parseConntrackEvent(line string) (event string, entry ConnEntry) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ConnEntry{}
	}

	// Extract event type from brackets.
	if line[0] == '[' {
		idx := strings.Index(line, "]")
		if idx > 0 {
			event = strings.TrimSpace(line[1:idx])
			line = strings.TrimSpace(line[idx+1:])
		}
	}

	entry = parseConntrackLine(line)
	return event, entry
}

func splitHostPort(s string) (host, port string) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

func timestamp() string {
	return time.Now().Format("15:04:05.000")
}
