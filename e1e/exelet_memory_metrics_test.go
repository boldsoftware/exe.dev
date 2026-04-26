package e1e

import (
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
)

// TestVMMemoryMetrics verifies the cgroup memory.stat breakdown reported by
// the exelet (anon, file, kernel, shmem, slab, inactive_file) is populated
// end-to-end and behaves the way we expect.
//
// Background. The cgroup-level memory.current is unreliable as a proxy for a
// VM's actual memory use. cloud-hypervisor backs guest RAM with hugepages
// (when available), and hugepage usage is *not* counted in memory.current.
// So memory.current ends up dominated by the host page cache that
// accumulates from the VM's disk I/O — which is reclaimable and not really
// "used". This test demonstrates that:
//
//  1. The breakdown fields are populated (non-zero file or anon on a
//     running VM).
//  2. Doing disk I/O inside the VM grows the cgroup's file/inactive_file
//     counters (showing the page cache attribution is correct).
//  3. The breakdown is consistent: file >= inactive_file, and the sum of
//     parts is roughly memory.current.
func TestVMMemoryMetrics(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	waitForSSH(t, boxName, keyFile)

	exeletClient := Env.servers.Exelets[0].Client()
	ctx := Env.context(t)

	getUsage := func(t *testing.T) *api.VMUsage {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			stream, err := exeletClient.ListVMUsage(ctx, &api.ListVMUsageRequest{})
			if err != nil {
				t.Fatalf("ListVMUsage: %v", err)
			}
			for {
				resp, err := stream.Recv()
				if err != nil {
					break
				}
				u := resp.GetUsage()
				if u != nil && u.Name == boxName && u.MemoryBytes > 0 {
					return u
				}
			}
			time.Sleep(1 * time.Second)
		}
		t.Fatalf("ListVMUsage never reported non-zero usage for %s", boxName)
		return nil
	}

	logUsage := func(label string, u *api.VMUsage) {
		t.Logf("%s: total=%d anon=%d file=%d kernel=%d shmem=%d slab=%d inactive_file=%d swap=%d",
			label, u.MemoryBytes, u.MemoryAnonBytes, u.MemoryFileBytes,
			u.MemoryKernelBytes, u.MemoryShmemBytes, u.MemorySlabBytes,
			u.MemoryInactiveFileBytes, u.SwapBytes)
	}

	baseline := getUsage(t)
	logUsage("baseline", baseline)

	// (1) The breakdown should be populated end-to-end. Either anon or
	//     file (or both) must be non-zero on a running VM.
	if baseline.MemoryAnonBytes == 0 && baseline.MemoryFileBytes == 0 {
		t.Fatalf("both MemoryAnonBytes and MemoryFileBytes are zero; the breakdown is not being plumbed end-to-end")
	}

	// (2) Internal consistency: inactive_file <= file, and the sum of
	//     anon+file+kernel should be in the same ballpark as memory.current.
	if baseline.MemoryInactiveFileBytes > baseline.MemoryFileBytes {
		t.Errorf("inactive_file (%d) > file (%d), impossible", baseline.MemoryInactiveFileBytes, baseline.MemoryFileBytes)
	}
	sum := baseline.MemoryAnonBytes + baseline.MemoryFileBytes + baseline.MemoryKernelBytes
	if sum > baseline.MemoryBytes*2 {
		t.Errorf("anon+file+kernel (%d) is more than 2x memory.current (%d); something is wrong", sum, baseline.MemoryBytes)
	}

	// (3) Doing 256 MiB of disk I/O inside the guest should grow the
	//     host-side file/inactive_file counters by a similar amount, since
	//     reads/writes go through the host page cache backing the zvol.
	box := sshToBox(t, boxName, keyFile)
	defer box.Disconnect()

	box.SendLine("dd if=/dev/zero of=/tmp/iotest bs=1M count=256 conv=fsync 2>&1 | tail -1")
	box.Want("copied")
	box.WantPrompt()

	deadline := time.Now().Add(45 * time.Second)
	var after *api.VMUsage
	for time.Now().Before(deadline) {
		after = getUsage(t)
		if int64(after.MemoryFileBytes)-int64(baseline.MemoryFileBytes) >= 64*1024*1024 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	logUsage("after_io", after)
	t.Logf("deltas: anon=%+d file=%+d inactive_file=%+d total=%+d",
		int64(after.MemoryAnonBytes)-int64(baseline.MemoryAnonBytes),
		int64(after.MemoryFileBytes)-int64(baseline.MemoryFileBytes),
		int64(after.MemoryInactiveFileBytes)-int64(baseline.MemoryInactiveFileBytes),
		int64(after.MemoryBytes)-int64(baseline.MemoryBytes))

	fileDelta := int64(after.MemoryFileBytes) - int64(baseline.MemoryFileBytes)
	if fileDelta < 64*1024*1024 {
		t.Errorf("expected file (page cache) to grow by at least 64 MiB after 256 MiB of guest disk I/O; grew by %d bytes", fileDelta)
	}

	// (4) Drop guest page cache and remove the file. The host page cache
	//     should drop too once the kernel reclaims it.
	box.SendLine("sync && rm -f /tmp/iotest && sync")
	box.WantPrompt()

	cleanupBox(t, keyFile, boxName)
}
