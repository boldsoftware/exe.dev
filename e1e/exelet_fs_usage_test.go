package e1e

import (
	"encoding/json"
	"os/exec"
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
	cmd := exec.Command("sudo", "-n", "sh", "-c", "sync && echo 3 > /proc/sys/vm/drop_caches")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("drop_caches failed (continuing): %v\n%s", err, out)
	}
}

// TestVMFilesystemUsage exercises the gated, opt-in ext4 superblock
// probe that the exelet performs against the guest's zvol.
//
// In the e1e environment the stage is "test", which sets
// stage.CollectExt4Usage=true; that propagates into the exelet config
// (CollectExt4Usage=true) and the resource manager allows the probe
// for every VM regardless of group ID. We verify three behaviours
// end-to-end:
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
	if noFlag.FsTotalBytes != 0 || noFlag.FsFreeBytes != 0 || noFlag.FsAvailableBytes != 0 {
		t.Errorf("fs_*_bytes leaked through without collect_filesystem_usage: total=%d free=%d avail=%d",
			noFlag.FsTotalBytes, noFlag.FsFreeBytes, noFlag.FsAvailableBytes)
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
	if u := getNoFlag.GetUsage(); u.FsTotalBytes != 0 || u.FsFreeBytes != 0 || u.FsAvailableBytes != 0 {
		t.Errorf("GetVMUsage with collect=false leaked fs_*_bytes: total=%d free=%d avail=%d",
			u.FsTotalBytes, u.FsFreeBytes, u.FsAvailableBytes)
	}

	// (3) End-to-end check that the *user-facing* `top` command
	//     returns the same data and that ext4 usage tracks guest-side
	//     file create/delete.
	//
	//     We use `top -n 1 --json`: a single, scripted snapshot. n=1
	//     means net rates are zero, but we don't assert on them.
	topSnapshot := func(t *testing.T) topJSONForVM {
		t.Helper()
		// Evict any cached pages so the exelet's next read of
		// /dev/zvol/<id> sees fresh data after a guest write.
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
		for _, vm := range parsed.VMs {
			if vm.Name == boxName {
				return vm
			}
		}
		t.Fatalf("box %s not in `top -n 1 --json` output: %s", boxName, out)
		return topJSONForVM{}
	}

	topBaseline := topSnapshot(t)
	t.Logf("top baseline: fs_total=%d fs_free=%d fs_avail=%d",
		topBaseline.FsTotalBytes, topBaseline.FsFreeBytes, topBaseline.FsAvailableBytes)
	if topBaseline.FsTotalBytes == 0 || topBaseline.FsAvailableBytes == 0 {
		t.Fatalf("top baseline missing ext4 fields: %+v", topBaseline)
	}
	if topBaseline.FsTotalBytes != withFlag.FsTotalBytes {
		t.Errorf("top FsTotalBytes (%d) != exelet FsTotalBytes (%d)",
			topBaseline.FsTotalBytes, withFlag.FsTotalBytes)
	}

	// Write a 128 MiB file inside the guest. The fstab uses the ext4
	// `defaults` mount options, so journal commits happen at most every
	// 5 seconds. We do an explicit sync to flush the journal, then
	// retry until the superblock catches up.
	const writeBytes = 128 * 1024 * 1024
	box := sshToBox(t, boxName, keyFile)
	defer box.Disconnect()
	// Allocate 128 MiB on the guest fs. ext4 keeps the on-disk
	// superblock's s_free_blocks_count lazily; sync alone doesn't
	// force ext4_commit_super to rewrite it. fsfreeze does (it
	// flushes the per-group counters into the superblock and then
	// commits to disk before returning).
	// Allocate 128 MiB on the guest fs. ext4 keeps the free-blocks
	// count in per-cpu in-memory counters and only folds them into
	// the on-disk primary superblock when ext4_commit_super() runs:
	// at unmount, on FIFREEZE/FITHAW (fsfreeze), or on the periodic
	// s_sb_upd_work workqueue. Plain `sync(1)`/`syncfs(2)` flush
	// data + journal but do NOT call ext4_commit_super. tune2fs
	// also doesn't help: it operates on the block device directly
	// (bypassing the kernel) and would just rewrite the stale
	// in-memory super.
	//
	// fsfreeze is online-safe (briefly quiesces writes, typically
	// sub-millisecond on an idle filesystem) and is the only fully
	// deterministic user-space trigger. We use it here only to
	// make the test deterministic; production never freezes the
	// guest — it lives with whatever the periodic flush has
	// produced (seconds-to-minutes lag), which is fine for a
	// capacity gauge.
	box.SendLine("sudo true")
	box.WantPrompt()
	box.SendLine("fallocate -l 128M /home/exedev/fsusage.bin && echo WRITE-DONE")
	box.Want("WRITE-DONE")
	box.WantPrompt()

	// freezeGuest forces ext4_commit_super inside the guest by
	// running fsfreeze. Calling it on every poll iteration is the
	// only way we have to make the on-disk superblock catch up
	// with the in-memory free-blocks counter on a recent kernel
	// (sync(1) doesn't trigger commit_super); re-triggering also
	// gives us multiple chances to invalidate the host's stale
	// bdev/ARC cache, which on CI's older ZFS doesn't always honor
	// O_DIRECT on the first read after a guest write.
	freezeGuest := func(t *testing.T) {
		t.Helper()
		box.SendLine("sudo sh -c 'sync && fsfreeze -f / && fsfreeze -u /' && echo FREEZE-DONE")
		box.Want("FREEZE-DONE")
		box.WantPrompt()
	}

	waitForAvailDelta := func(t *testing.T, base uint64, wantNegative bool) topJSONForVM {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		var last topJSONForVM
		for time.Now().Before(deadline) {
			freezeGuest(t)
			last = topSnapshot(t)
			delta := int64(last.FsAvailableBytes) - int64(base)
			// Want at least half the write to be reflected. Guest may
			// allocate journal/metadata blocks too, so allow generous slack.
			if wantNegative && delta <= -int64(writeBytes/2) {
				return last
			}
			if !wantNegative && delta >= -int64(writeBytes/4) {
				return last
			}
			time.Sleep(2 * time.Second)
		}
		return last
	}

	afterWrite := waitForAvailDelta(t, topBaseline.FsAvailableBytes, true)
	writeDelta := int64(topBaseline.FsAvailableBytes) - int64(afterWrite.FsAvailableBytes)
	t.Logf("after write: fs_avail=%d (delta from baseline: %+d bytes)",
		afterWrite.FsAvailableBytes, -writeDelta)
	if writeDelta < int64(writeBytes/2) {
		t.Errorf("after writing %d bytes inside guest, FsAvailableBytes only dropped by %d bytes; superblock didn't catch up. baseline=%d after=%d",
			writeBytes, writeDelta, topBaseline.FsAvailableBytes, afterWrite.FsAvailableBytes)
	}

	// Remove the file and wait for the available-bytes to recover.
	box.SendLine("rm -f /home/exedev/fsusage.bin && echo RM-DONE")
	box.Want("RM-DONE")
	box.WantPrompt()
	afterRm := waitForAvailDelta(t, topBaseline.FsAvailableBytes, false)
	rmDelta := int64(topBaseline.FsAvailableBytes) - int64(afterRm.FsAvailableBytes)
	t.Logf("after rm: fs_avail=%d (delta from baseline: %+d bytes)",
		afterRm.FsAvailableBytes, -rmDelta)
	if rmDelta > int64(writeBytes/4) {
		t.Errorf("after removing the file, FsAvailableBytes is still %d bytes below baseline; expected near-recovery. baseline=%d after_rm=%d",
			rmDelta, topBaseline.FsAvailableBytes, afterRm.FsAvailableBytes)
	}

	if afterRm.Name != boxName {
		t.Errorf("top JSON returned wrong VM name: got %q want %q", afterRm.Name, boxName)
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
