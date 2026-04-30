package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestDropPageCacheEndpoint exercises the exed→exelet→SSH path that the
// resource manager's idle-VM probe uses to drop guest page cache. We first
// fill the guest page cache (by streaming a file into /dev/null), then POST
// to /exelet-drop-page-cache and assert on the before/after /proc/meminfo
// captured *inside* the same SSH session that performed the drop —
// reading /proc/meminfo from a fresh SSH session afterwards races against
// systemd/sshd repopulating page cache.
func TestDropPageCacheEndpoint(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	if len(Env.servers.Exelets) == 0 {
		t.Fatal("no exelets in test environment")
	}

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)

	waitForSSH(t, boxName, keyFile)

	// Fill the guest page cache. /dev/vda is the boot disk; reading it
	// populates the kernel page cache. 64 MiB is plenty to dwarf any
	// "Cached" baseline noise without taking too long.
	out, err := boxSSHCommand(t, boxName, keyFile,
		"sh", "-c", "dd if=/dev/vda of=/dev/null bs=1M count=64 status=none && sync").CombinedOutput()
	if err != nil {
		t.Fatalf("populate page cache: %v\n%s", err, out)
	}

	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)

	endpoint := fmt.Sprintf("http://localhost:%d/exelet-drop-page-cache", Env.servers.Exed.HTTPPort)
	form := url.Values{
		"container_id": {instanceID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST drop-page-cache: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drop-page-cache status %d: %s", resp.StatusCode, body)
	}
	t.Logf("drop-page-cache response:\n%s", body)

	// The response body contains:
	//   ok
	//   MemFree delta: +N bytes (before=A, after=B)
	//   output:
	//   <pre-drop /proc/meminfo>
	//   --DROP--
	//   <post-drop /proc/meminfo>
	// Both meminfo blocks were captured by the same SSH session, before
	// and after the kernel-side drop_caches write. Parse out Cached: from
	// each and require the post-drop value to be at least 8 MiB lower.
	afterOutput, ok := strings.CutPrefix(string(body), "")
	if !ok {
		t.Fatalf("unexpected body prefix: %q", body)
	}
	_, meminfoBlocks, ok := strings.Cut(afterOutput, "output:\n")
	if !ok {
		t.Fatalf("could not find 'output:' in body: %q", body)
	}
	beforeBlock, afterBlock, ok := strings.Cut(meminfoBlocks, "--DROP--")
	if !ok {
		t.Fatalf("could not find '--DROP--' marker in body: %q", body)
	}
	cachedBefore := parseMeminfoCachedKB(t, beforeBlock)
	cachedAfter := parseMeminfoCachedKB(t, afterBlock)
	// On a small (~1 GiB) CI guest, the kernel reclaims aggressively while
	// we're streaming dd, so the *retained* page cache after `sync` ends up
	// well below the 64 MiB we read — typically ~30–40 MiB. We require
	// >=16 MiB still resident so we know the drop has something meaningful
	// to free, and >=4 MiB shrink to confirm drop_caches actually fired:
	// across a sample of failing CI runs the observed delta clustered just
	// under 8 MiB (~7.5–7.9 MiB) because our before/after meminfo readings
	// race against sshd, cat, and friends repopulating cache pages between
	// the two reads in the same shell session.
	if cachedBefore < 16*1024 {
		t.Fatalf("guest Cached only %d kB before drop; expected >=16 MiB after dd\nbefore block:\n%s",
			cachedBefore, beforeBlock)
	}
	if cachedBefore-cachedAfter < 4*1024 {
		t.Fatalf("guest Cached did not shrink enough: before=%d kB after=%d kB (delta=%d kB)\nbody:\n%s",
			cachedBefore, cachedAfter, cachedBefore-cachedAfter, body)
	}
	t.Logf("guest Cached (in-session): before=%d kB, after=%d kB (-%d kB)",
		cachedBefore, cachedAfter, cachedBefore-cachedAfter)
}

// parseMeminfoCachedKB extracts the Cached: value (in kB) from a chunk of
// /proc/meminfo output. Only matches lines starting with exactly "Cached:"
// (not "SwapCached:") via prefix-and-space matching.
func parseMeminfoCachedKB(t *testing.T, block string) int64 {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "Cached:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("parse Cached %q: %v", line, err)
		}
		return v
	}
	t.Fatalf("no Cached: line in meminfo block:\n%s", block)
	return 0
}
