package e1e

import (
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	api "exe.dev/pkg/api/exe/resource/v1"
)

// TestMemdScrape verifies the in-guest memd vsock server responds to
// "GET memstat\n" with parseable JSON listing MemTotal > 1 GiB.
func TestMemdScrape(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	if len(Env.servers.Exelets) == 0 {
		t.Fatal("no exelets")
	}
	exelet := Env.servers.Exelets[0]
	// FetchMemdSample handles both local and remote (SSH-tunnelled) sockets.

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)
	waitForSSH(t, boxName, keyFile)

	ctx := Env.context(t)
	exeletClient := exelet.Client()
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)

	// memd may take a moment after boot. Retry up to 60s.
	deadline := time.Now().Add(60 * time.Second)
	var sample *testinfra.MemdSample
	var lastErr error
	for time.Now().Before(deadline) {
		sample, lastErr = testinfra.FetchMemdSample(ctx, exelet, instanceID)
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("FetchMemdSample: %v", lastErr)
	}

	if sample.Version == 0 {
		t.Errorf("missing version: %+v", sample)
	}
	memTotalKB := sample.Meminfo["MemTotal"]
	// Guests provisioned with 1 GiB report ~970–990 MiB to userspace once
	// the kernel reserves DMA / firmware regions; require ≥ 800 MiB.
	if memTotalKB < 800*1024 {
		t.Errorf("MemTotal=%d kB, want >=800 MiB", memTotalKB)
	}
	if sample.Meminfo["MemAvailable"] == 0 {
		t.Errorf("MemAvailable=0")
	}
	t.Logf("memd sample: ver=%d uptime=%.1f MemTotal=%d kB MemAvailable=%d kB Cached=%d kB errors=%v",
		sample.Version, sample.UptimeSec, sample.Meminfo["MemTotal"],
		sample.Meminfo["MemAvailable"], sample.Meminfo["Cached"], sample.Errors)
}

// TestMemwatchScrapesPopulateGRPC verifies the resource manager's
// guestmetrics pool publishes scraped values into the VMUsage proto.
// We warm the guest page cache with `dd` and observe Cached rise.
func TestMemwatchScrapesPopulateGRPC(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)
	waitForSSH(t, boxName, keyFile)

	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()

	getGuest := func() *api.GuestMemoryStats {
		stream, err := exeletClient.ListVMUsage(ctx, &api.ListVMUsageRequest{})
		if err != nil {
			return nil
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				return nil
			}
			u := resp.GetUsage()
			if u != nil && u.GetName() == boxName {
				return u.GetGuestMemory()
			}
		}
	}

	// Wait for first guest scrape to land. Pool ticks every second so
	// 60s is generous.
	deadline := time.Now().Add(90 * time.Second)
	var baseline *api.GuestMemoryStats
	for time.Now().Before(deadline) {
		baseline = getGuest()
		if baseline != nil && baseline.GetMemTotalBytes() > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if baseline == nil || baseline.GetMemTotalBytes() == 0 {
		t.Fatalf("no GuestMemoryStats published for %s", boxName)
	}
	t.Logf("baseline: total=%d avail=%d cached=%d reclaim=%d",
		baseline.GetMemTotalBytes(), baseline.GetMemAvailableBytes(),
		baseline.GetCachedBytes(), baseline.GetReclaimableBytes())

	// Warm the guest page cache. Write a 64 MiB temp file and read it
	// back; this is portable across guest images (no assumption about
	// /dev/vda being present and readable). Use /var/tmp rather than
	// /tmp: /tmp is tmpfs (lives in RAM, fights for the same pages we
	// want cached) and is also subject to systemd-tmpfiles cleanup.
	out, err := boxSSHCommand(t, boxName, keyFile,
		"sh", "-c",
		"set -x; dd if=/dev/zero of=/var/tmp/memwatch-warmup bs=1M count=64; sync; "+
			"dd if=/var/tmp/memwatch-warmup of=/dev/null bs=1M").CombinedOutput()
	// Note: do not rm /var/tmp/memwatch-warmup here. Unlinking the file
	// frees its page cache pages, which is exactly what we are trying
	// to observe.
	if err != nil {
		t.Fatalf("dd: %v\n%s", err, out)
	}

	// Wait for the next scrape to capture the warmed cache.
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		g := getGuest()
		if g != nil && g.GetCachedBytes() >= baseline.GetCachedBytes()+8*1024*1024 {
			t.Logf("warmed: cached=%d (delta=+%d)", g.GetCachedBytes(), g.GetCachedBytes()-baseline.GetCachedBytes())
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	g := getGuest()
	t.Fatalf("Cached did not grow after dd: baseline=%d, latest=%v", baseline.GetCachedBytes(), g)
}
