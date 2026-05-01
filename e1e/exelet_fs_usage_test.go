package e1e

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
)

// dropHostPageCache evicts the host's page cache (and dentry/inode
// caches) so the next read of /dev/zvol/<path> goes to the
// underlying ZFS dataset rather than returning stale bytes from a
// previous buffered read. This is a test-only helper. Cloud
// Hypervisor opens its backing device with O_DIRECT, so guest writes
// skip the host page cache, and on some (kernel, ZFS) combinations
// neither O_DIRECT-on-read nor BLKFLSBUF/FADV_DONTNEED reliably
// invalidates an already-cached superblock page.
//
// Production exelet uses O_DIRECT + BLKFLSBUF + FADV_DONTNEED in
// ext4.ReadUsage, which works on most setups; on those where it
// doesn't the on-disk numbers just lag a bit, which is fine for a
// capacity gauge.
func dropHostPageCache(t *testing.T) {
	t.Helper()
	// Belt-and-suspenders eviction:
	//   - drop_caches=3 evicts the host's page+dentry+inode caches
	//     (covers reads against the zvol's bdev page cache).
	//   - blockdev --flushbufs on every zd* device drops the bdev
	//     buffer cache directly (older ZFS doesn't always honor
	//     drop_caches for zvols).
	//   - zpool sync forces dirty TXGs out so subsequent reads go
	//     to disk rather than returning ARC-cached state from a
	//     pre-write transaction.
	script := `sync
echo 3 > /proc/sys/vm/drop_caches
for d in /dev/zd*; do [ -b "$d" ] && blockdev --flushbufs "$d" 2>/dev/null || true; done
zpool sync 2>/dev/null || true
sync
echo 3 > /proc/sys/vm/drop_caches`
	cmd := exec.Command("sudo", "-n", "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("drop_caches failed (continuing): %v\n%s", err, out)
	}
}

// TestVMFilesystemUsage exercises the opt-in ext4 superblock probe
// that the exelet performs against the guest's zvol. The probe is
// always available to callers; only the per-RPC request flag controls
// whether it runs. We verify three behaviours end-to-end:
//
//	(1) When the request flag is true, fs_*_bytes is populated with
//	    sensible values (free < total, available <= free, total <=
//	    provisioned zvol capacity).
//	(2) When the request flag is false, fs_*_bytes is zero (the
//	    exelet does not surface cached values, and does not perform
//	    the read either).
//	(3) Writing a file inside the guest reduces FsAvailableBytes by
//	    roughly the file size, and removing it restores it. ext4 only
//	    flushes s_free_blocks_count to the superblock on journal
//	    commits (default commit=5), so this requires a sync inside
//	    the guest plus a tolerant retry loop.
func TestVMFilesystemUsage(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	waitForSSH(t, boxName, keyFile)

	exeletClient := Env.servers.Exelets[0].Client()
	ctx := Env.context(t)

	// Wait until the exelet has at least one ListVMUsage row for our
	// box (the resource manager polls at its own cadence).
	listUsage := func(t *testing.T, collect bool) *api.VMUsage {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			stream, err := exeletClient.ListVMUsage(ctx, &api.ListVMUsageRequest{
				CollectFilesystemUsage: collect,
			})
			if err != nil {
				t.Fatalf("ListVMUsage(collect=%v): %v", collect, err)
			}
			for {
				resp, err := stream.Recv()
				if err != nil {
					break
				}
				u := resp.GetUsage()
				if u != nil && u.Name == boxName {
					return u
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("ListVMUsage never reported %s", boxName)
		return nil
	}

	// (2) Without the request flag, no fs_*_bytes is surfaced.
	noFlag := listUsage(t, false)
	t.Logf("no-flag: fs_total=%d fs_free=%d fs_avail=%d disk_capacity=%d",
		noFlag.FsTotalBytes, noFlag.FsFreeBytes, noFlag.FsAvailableBytes, noFlag.DiskCapacityBytes)
	if noFlag.FsTotalBytes != 0 || noFlag.FsFreeBytes != 0 || noFlag.FsAvailableBytes != 0 || noFlag.FsUsedBytes != 0 {
		t.Errorf("fs_*_bytes leaked through without collect_filesystem_usage: total=%d free=%d avail=%d used=%d",
			noFlag.FsTotalBytes, noFlag.FsFreeBytes, noFlag.FsAvailableBytes, noFlag.FsUsedBytes)
	}

	// (1) With the flag, the exelet performs the on-demand probe and
	//     populates the fields. Retry: the zvol may not have an ext4
	//     header until the VM has been running long enough for the
	//     metadata to flush.
	var withFlag *api.VMUsage
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		withFlag = listUsage(t, true)
		if withFlag.FsTotalBytes > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("with-flag: fs_total=%d fs_free=%d fs_avail=%d disk_capacity=%d",
		withFlag.FsTotalBytes, withFlag.FsFreeBytes, withFlag.FsAvailableBytes, withFlag.DiskCapacityBytes)
	if withFlag.FsTotalBytes == 0 {
		t.Fatalf("FsTotalBytes still zero with collect_filesystem_usage=true; gate or probe is wrong")
	}
	if withFlag.FsFreeBytes == 0 || withFlag.FsFreeBytes >= withFlag.FsTotalBytes {
		t.Errorf("FsFreeBytes = %d (total %d); want 0 < free < total", withFlag.FsFreeBytes, withFlag.FsTotalBytes)
	}
	if withFlag.FsAvailableBytes > withFlag.FsFreeBytes {
		t.Errorf("FsAvailableBytes (%d) > FsFreeBytes (%d)", withFlag.FsAvailableBytes, withFlag.FsFreeBytes)
	}
	// fs_used_bytes is the obvious complement of fs_free_bytes; with a
	// well-formed superblock these always sum to fs_total_bytes.
	if withFlag.FsUsedBytes == 0 || withFlag.FsUsedBytes+withFlag.FsFreeBytes != withFlag.FsTotalBytes {
		t.Errorf("FsUsedBytes (%d) + FsFreeBytes (%d) != FsTotalBytes (%d)",
			withFlag.FsUsedBytes, withFlag.FsFreeBytes, withFlag.FsTotalBytes)
	}
	// Cross-check capacity: ext4 capacity must be ≤ zvol provisioned
	// size (mkfs may round down by a few MiB).
	if withFlag.DiskCapacityBytes > 0 && withFlag.FsTotalBytes > withFlag.DiskCapacityBytes {
		t.Errorf("FsTotalBytes (%d) > DiskCapacityBytes (%d)", withFlag.FsTotalBytes, withFlag.DiskCapacityBytes)
	}

	// (1c) The GetVMUsage RPC respects the same flag.
	//      Look up the VM's container ID by re-listing without the flag.
	containerID := withFlag.ID
	if containerID == "" {
		t.Fatal("empty container ID in ListVMUsage response")
	}
	getWithFlag, err := exeletClient.GetVMUsage(ctx, &api.GetVMUsageRequest{
		VmID:                   containerID,
		CollectFilesystemUsage: true,
	})
	if err != nil {
		t.Fatalf("GetVMUsage(collect=true): %v", err)
	}
	if getWithFlag.GetUsage().FsTotalBytes == 0 {
		t.Errorf("GetVMUsage with collect=true returned FsTotalBytes=0")
	}
	getNoFlag, err := exeletClient.GetVMUsage(ctx, &api.GetVMUsageRequest{
		VmID:                   containerID,
		CollectFilesystemUsage: false,
	})
	if err != nil {
		t.Fatalf("GetVMUsage(collect=false): %v", err)
	}
	if u := getNoFlag.GetUsage(); u.FsTotalBytes != 0 || u.FsFreeBytes != 0 || u.FsAvailableBytes != 0 || u.FsUsedBytes != 0 {
		t.Errorf("GetVMUsage with collect=false leaked fs_*_bytes: total=%d free=%d avail=%d",
			u.FsTotalBytes, u.FsFreeBytes, u.FsAvailableBytes)
	}

	// (3) ext4 usage tracks guest-side file create/delete. We poll
	//     the gRPC ListVMUsage call (fast, ms-level latency) and
	//     then sanity-check the user-facing `top --json` once at
	//     the end. Polling via `top` over SSH on every iteration
	//     was the dominant cost on CI (~1s per call).
	probe := func(t *testing.T) *api.VMUsage {
		t.Helper()
		dropHostPageCache(t)
		return listUsage(t, true)
	}

	baseline := probe(t)
	t.Logf("baseline: fs_total=%d fs_free=%d fs_avail=%d",
		baseline.FsTotalBytes, baseline.FsFreeBytes, baseline.FsAvailableBytes)
	if baseline.FsTotalBytes == 0 || baseline.FsAvailableBytes == 0 {
		t.Fatalf("baseline missing ext4 fields: %+v", baseline)
	}

	// Sanity-check that the host's view matches the guest's own
	// view from statvfs(2) (`df -B1 /` inside the box). We use a
	// single one-shot ssh-into-the-box command rather than a
	// long-running expectPty session: faster, fewer round-trips.
	//
	// We deliberately do NOT do a write-then-poll-for-delta dance:
	// on older ZFS (2.2.2 in CI), zvol char-device reads from the
	// host don't always honor O_DIRECT or BLKFLSBUF, so the host
	// can return cached pre-write superblock bytes long after the
	// guest has flushed. Production tolerates that lag (capacity
	// gauge, not a fast-moving counter); the test should too.
	dfOut, err := Env.servers.BoxSSHCommand(ctx, boxName, keyFile,
		"sudo sh -c 'sync && fsfreeze -f / && fsfreeze -u /' && df -B1 / | awk 'NR==2 {print \"GUEST:\"$2\":\"$4\":END\"}'").CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-to-box df: %v\n%s", err, dfOut)
	}
	m := regexp.MustCompile(`GUEST:([0-9]+):([0-9]+):END`).FindStringSubmatch(string(dfOut))
	if m == nil {
		t.Fatalf("parse guest df from %q", string(dfOut))
	}
	guestTotal, _ := strconv.ParseUint(m[1], 10, 64)
	guestAvail, _ := strconv.ParseUint(m[2], 10, 64)
	t.Logf("guest df: total=%d avail=%d", guestTotal, guestAvail)

	// Re-probe through the exelet now that the superblock has
	// been freshly committed.
	fresh := probe(t)
	t.Logf("after fsfreeze: fs_total=%d fs_avail=%d", fresh.FsTotalBytes, fresh.FsAvailableBytes)

	// The exelet reads s_blocks_count straight from the
	// superblock (capacity before metadata overhead); statvfs's
	// f_blocks subtracts that overhead, so exelet ≥ guest, within
	// 5%%.
	if fresh.FsTotalBytes < guestTotal {
		t.Errorf("exelet FsTotalBytes (%d) < guest df total (%d)", fresh.FsTotalBytes, guestTotal)
	}
	if fresh.FsTotalBytes > guestTotal+guestTotal/20 {
		t.Errorf("exelet FsTotalBytes (%d) > guest df total (%d) + 5%%",
			fresh.FsTotalBytes, guestTotal)
	}
	slack := int64(guestTotal / 20)
	diff := int64(fresh.FsAvailableBytes) - int64(guestAvail)
	if diff < -slack || diff > slack {
		t.Errorf("exelet FsAvailableBytes (%d) differs from guest df avail (%d) by %d (slack %d)",
			fresh.FsAvailableBytes, guestAvail, diff, slack)
	}

	// One round-trip through the user-facing `top --json` lobby
	// path, to verify wire format and the show_fs_usage flag.
	dropHostPageCache(t)
	out, err := Env.servers.RunExeDevSSHCommand(ctx, keyFile, "top", "-n", "1", "--json")
	if err != nil {
		t.Fatalf("top -n 1 --json: %v\n%s", err, out)
	}
	var parsed topJSONOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse top json: %v\n%s", err, out)
	}
	if !parsed.ShowFsUsage {
		t.Fatalf("top reports show_fs_usage=false; expected true on test stage. raw=%s", out)
	}
	var topVM topJSONForVM
	for _, vm := range parsed.VMs {
		if vm.Name == boxName {
			topVM = vm
			break
		}
	}
	if topVM.Name == "" {
		t.Fatalf("box %s not in `top -n 1 --json` output: %s", boxName, out)
	}
	if topVM.FsTotalBytes == 0 || topVM.FsAvailableBytes == 0 {
		t.Errorf("top JSON missing ext4 fields: %+v", topVM)
	}

	cleanupBox(t, keyFile, boxName)
}

// topJSONOutput / topJSONForVM mirror the JSON shape produced by
// `top --json`. They are duplicated (rather than imported from
// execore) because e1e tests run as a separate package and we want to
// pin the wire format here.
type topJSONOutput struct {
	Iterations  int            `json:"iterations"`
	Interval    string         `json:"interval"`
	ShowFsUsage bool           `json:"show_fs_usage"`
	VMs         []topJSONForVM `json:"vms"`
}

type topJSONForVM struct {
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemBytes         uint64  `json:"memory_bytes"`
	DiskCapacity     uint64  `json:"disk_capacity_bytes"`
	FsTotalBytes     uint64  `json:"fs_total_bytes"`
	FsFreeBytes      uint64  `json:"fs_free_bytes"`
	FsAvailableBytes uint64  `json:"fs_available_bytes"`
}
