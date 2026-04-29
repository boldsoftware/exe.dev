package execore

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exedebug"
	"exe.dev/exemenu"
)

// dropPageCacheTimeout caps how long we'll wait for the SSH session and
// the in-VM drop_caches write. The whole thing is best-effort.
const dropPageCacheTimeout = 30 * time.Second

// isSSHHandshakeError reports whether err looks like a server-side
// failure during the SSH handshake (auth, key exchange, or sshd
// closing the connection mid-handshake) — i.e. something a
// different-user retry might fix. We deliberately avoid matching
// network-class failures ("SSH dial failed", "i/o timeout",
// "connection refused"). The wrapper-level "ssh: handshake failed:"
// always wraps a more specific cause that we then classify here.
//
// We've observed two flavors in the wild on legacy VMs (created
// before af35594b, where /exe.dev/etc/ssh/authorized_keys is owned by
// the login user instead of root):
//
//  1. "ssh: handshake failed: ssh: unable to authenticate, attempted
//     methods [none publickey], no supported methods remain"
//     — the textbook OpenSSH StrictModes rejection of root.
//  2. "ssh: handshake failed: EOF" — sshd closed the transport
//     mid-handshake without an SSH_MSG_DISCONNECT, e.g. when
//     StrictModes rejects the file or auth otherwise terminates the
//     connection abruptly. x/crypto/ssh surfaces the truncated read
//     as a plain io.EOF.
//
// x/crypto/ssh doesn't expose a sentinel for either, so we match on
// error text. Network-class errors come back as "SSH dial failed" or
// "failed to set deadline" (see sshpool2/pool.go) and don't pass.
func isSSHHandshakeError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "handshake failed: EOF")
}

// dropPageCacheCommandRoot is run inside the VM when we're SSH'd in as
// root: no sudo needed. It captures /proc/meminfo before and after,
// separated by a marker, so the caller can compute MemFree (and
// friends) deltas from a single SSH session.
const dropPageCacheCommandRoot = "cat /proc/meminfo; echo --DROP--; echo 3 > /proc/sys/vm/drop_caches; cat /proc/meminfo"

// dropPageCacheCommandSudo is the fallback for VMs where root pubkey
// auth doesn't work. VMs created before commit af35594b ("exe-init:
// keep authorized_keys owned by root for multi-user SSH") have
// /exe.dev/etc/ssh/authorized_keys chowned to the login user, and
// OpenSSH StrictModes refuses to read it for root, producing
// "ssh: unable to authenticate, attempted methods [none publickey]".
// We log into the regular login user and sudo to the kernel write.
const dropPageCacheCommandSudo = "cat /proc/meminfo; echo --DROP--; sudo sh -c 'echo 3 > /proc/sys/vm/drop_caches'; cat /proc/meminfo"

// dropPageCacheResult summarizes a single drop. Bytes are computed from
// /proc/meminfo's MemFree, MemAvailable, and Cached (all reported in kB)
// and converted to bytes for symmetry with cgroup figures.
type dropPageCacheResult struct {
	MemFreeBeforeBytes      int64
	MemFreeAfterBytes       int64
	MemFreeDeltaBytes       int64 // after - before
	MemAvailableBeforeBytes int64
	MemAvailableAfterBytes  int64
	CachedBeforeBytes       int64
	CachedAfterBytes        int64
	RawOutput               []byte
}

// dropPageCacheOnBox SSHes into the VM and asks the guest kernel to
// drop pagecache, dentries and inodes. It tries the fast path first
// (SSH as root, no sudo), and falls back to SSH-as-login-user + sudo
// for VMs whose authorized_keys file isn't root-owned (see
// dropPageCacheCommandSudo). It also captures /proc/meminfo before and
// after, parses out MemFree, and emits a canonical log line.
// Best-effort: callers should generally treat failures as non-fatal.
func (s *Server) dropPageCacheOnBox(ctx context.Context, box *exedb.Box) (*dropPageCacheResult, error) {
	if box.SSHPort == nil || len(box.SSHClientPrivateKey) == 0 {
		return nil, fmt.Errorf("box %q does not have SSH configured", box.Name)
	}
	cctx, cancel := context.WithTimeout(ctx, dropPageCacheTimeout)
	defer cancel()
	// Try root first; if SSH auth fails (typically because the box's
	// authorized_keys isn't root-owned and StrictModes rejects it), retry
	// as the login user with sudo. Only auth-class failures fall back —
	// other errors (timeout, network, command failure) are returned as is
	// so we don't paper over real problems.
	authMethod := "root"
	out, err := runCommandOnBoxAsUser(cctx, s.sshPool, box, "root", dropPageCacheCommandRoot, nil)
	var rootErr error
	if err != nil && isSSHHandshakeError(err) {
		rootErr = err
		authMethod = "sudo"
		out, err = runCommandOnBox(cctx, s.sshPool, box, dropPageCacheCommandSudo)
		if err != nil {
			// Surface both the root attempt and the sudo attempt so
			// operators can tell which auth path was actually broken.
			err = fmt.Errorf("root: %w; sudo: %w", rootErr, err)
		}
	}
	if err != nil {
		return &dropPageCacheResult{RawOutput: out}, err
	}
	res := parseDropPageCacheOutput(out)
	s.slog().InfoContext(ctx, "drop-page-cache",
		"box", box.Name,
		"ctrhost", box.Ctrhost,
		"auth", authMethod,
		"memfree_before_bytes", res.MemFreeBeforeBytes,
		"memfree_after_bytes", res.MemFreeAfterBytes,
		"memfree_delta_bytes", res.MemFreeDeltaBytes,
		"memavailable_before_bytes", res.MemAvailableBeforeBytes,
		"memavailable_after_bytes", res.MemAvailableAfterBytes,
		"cached_before_bytes", res.CachedBeforeBytes,
		"cached_after_bytes", res.CachedAfterBytes,
	)
	return res, nil
}

// parseDropPageCacheOutput pulls MemFree/MemAvailable/Cached out of the
// before/after /proc/meminfo blocks emitted by dropPageCacheCommand.
// Missing fields are reported as zero.
func parseDropPageCacheOutput(out []byte) *dropPageCacheResult {
	res := &dropPageCacheResult{RawOutput: out}
	before, after, _ := bytes.Cut(out, []byte("--DROP--"))
	beforeFree, beforeAvail, beforeCached := scanMeminfo(before)
	afterFree, afterAvail, afterCached := scanMeminfo(after)
	res.MemFreeBeforeBytes = beforeFree
	res.MemFreeAfterBytes = afterFree
	res.MemFreeDeltaBytes = afterFree - beforeFree
	res.MemAvailableBeforeBytes = beforeAvail
	res.MemAvailableAfterBytes = afterAvail
	res.CachedBeforeBytes = beforeCached
	res.CachedAfterBytes = afterCached
	return res
}

// scanMeminfo extracts MemFree, MemAvailable, and Cached (in bytes) from a
// /proc/meminfo blob. /proc/meminfo lines look like "MemFree:   12345 kB".
func scanMeminfo(data []byte) (memFree, memAvail, cached int64) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemFree:"):
			memFree = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvail = parseMeminfoKB(line)
		case strings.HasPrefix(line, "Cached:"):
			cached = parseMeminfoKB(line)
		}
	}
	return memFree, memAvail, cached
}

// parseMeminfoKB parses a /proc/meminfo line and returns the value in
// bytes. /proc/meminfo always reports kB; we multiply by 1024.
func parseMeminfoKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

// handleSudoDropPageCacheCommand implements `sudo-exe drop-page-cache <vm>`.
// It SSHes into the VM and drops the guest kernel's page cache. Best-effort.
func (ss *SSHServer) handleSudoDropPageCacheCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: sudo-exe drop-page-cache <vmname>")
	}
	vmName := ss.normalizeBoxName(cc.Args[0])

	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxNamed, vmName)
	if err != nil {
		return cc.Errorf("failed to look up VM: %v", err)
	}

	res, err := ss.server.dropPageCacheOnBox(ctx, &box)
	var rawOut []byte
	if res != nil {
		rawOut = res.RawOutput
	}
	if cc.WantJSON() {
		resp := map[string]any{
			"vm_name": vmName,
			"output":  string(rawOut),
		}
		if res != nil {
			resp["memfree_before_bytes"] = res.MemFreeBeforeBytes
			resp["memfree_after_bytes"] = res.MemFreeAfterBytes
			resp["memfree_delta_bytes"] = res.MemFreeDeltaBytes
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		cc.WriteJSON(resp)
		return nil
	}
	if err != nil {
		return cc.Errorf("drop-page-cache for %s: %v\noutput: %s", vmName, err, bytes.TrimSpace(rawOut))
	}
	cc.Writeln("dropped page cache on %s (MemFree delta: %+d bytes / before=%d after=%d)",
		vmName, res.MemFreeDeltaBytes, res.MemFreeBeforeBytes, res.MemFreeAfterBytes)
	return nil
}

// handleDebugBoxDropPageCache is the /debug/vms/drop-page-cache POST handler
// used by the debug VM details page.
func (s *Server) handleDebugBoxDropPageCache(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	boxName := r.FormValue("box_name")
	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up box: %v", err), http.StatusInternalServerError)
		return
	}
	res, err := s.dropPageCacheOnBox(ctx, &box)
	var trunc []byte
	if res != nil {
		trunc = truncOutput(res.RawOutput, 8*1024)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "drop-page-cache failed for %s: %v\noutput:\n%s\n", boxName, err, trunc)
		return
	}
	fmt.Fprintf(w, "dropped page cache on %s\nMemFree delta: %+d bytes (before=%d, after=%d)\noutput:\n%s\n",
		boxName, res.MemFreeDeltaBytes, res.MemFreeBeforeBytes, res.MemFreeAfterBytes, trunc)
}

// truncOutput returns up to max bytes of out, with an elision marker if the
// input was longer.
func truncOutput(out []byte, max int) []byte {
	if len(out) <= max {
		return out
	}
	ret := make([]byte, 0, max+32)
	ret = append(ret, out[:max]...)
	ret = append(ret, []byte("\n... [truncated]")...)
	return ret
}

// handleExeletDropPageCache lets an exelet ask exed to SSH into one of its
// VMs and drop the guest kernel page cache. Used by the exelet resource
// manager's idle-VM cache-drop probe. Looks up the box by container_id (the
// id known to the exelet), then performs the same SSH-and-drop flow as the
// debug page.
//
// Authn/authz: caller must be on the Tailscale network (or in DebugDev).
// Anyone on the tailnet can request a drop on any VM — the only side
// effects are inside the guest, the operation is read-only-ish, and the
// extra cross-check we used to do (matching the box's recorded ctrhost
// against a host= query param) didn't actually add security in the
// presence of a compromised exelet.
func (s *Server) handleExeletDropPageCache(w http.ResponseWriter, r *http.Request) {
	if !exedebug.AllowLocalAccess(s.env, w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	containerID := r.FormValue("container_id")
	if containerID == "" {
		http.Error(w, "container_id is required", http.StatusBadRequest)
		return
	}
	cid := containerID
	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByContainerID, &cid)
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		http.Error(w, "box not found", http.StatusNotFound)
		return
	}
	res, err := s.dropPageCacheOnBox(ctx, &box)
	var trunc []byte
	if res != nil {
		trunc = truncOutput(res.RawOutput, 8*1024)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "drop-page-cache failed: %v\noutput:\n%s\n", err, trunc)
		return
	}
	fmt.Fprintf(w, "ok\nMemFree delta: %+d bytes (before=%d, after=%d)\noutput:\n%s\n",
		res.MemFreeDeltaBytes, res.MemFreeBeforeBytes, res.MemFreeAfterBytes, trunc)
}
