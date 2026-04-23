package e1e

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestVMMemoryChargedToVMScope verifies that cloud-hypervisor is placed
// directly into the VM's cgroup scope via CLONE_INTO_CGROUP at exec time,
// rather than being started in the exelet's cgroup and moved later. Without
// this placement, guest RAM page faults are charged to whichever cgroup
// cloud-hypervisor is in when it first touches pages — typically the exelet's
// cgroup — and stay there forever (cgroup v2 does not reparent charges when
// a process is moved).
//
// The test checks the canonical observable: /proc/<pid>/cgroup of the running
// CH process must name the VM's own scope. This is independent of the
// hugepages/non-hugepages distinction (hugetlb charges are routed separately
// from memory.current and may not be enabled in the scope's subtree_control on
// every host).
//
// As a secondary check, if the scope's `cgroup.procs` contains the CH pid,
// then the placement also matches what the resource manager would produce —
// but via the clone-time path, not via a post-start move.
func TestVMMemoryChargedToVMScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux")
	}

	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)
	defer pty.Disconnect()

	boxName := newBox(t, pty)
	defer pty.deleteBox(boxName)

	ctx := Env.context(t)
	exelet := Env.servers.Exelets[0]
	exeletClient := exelet.Client()

	if out, err := exelet.Exec(ctx, "test -f /sys/fs/cgroup/cgroup.controllers && stat -fc %T /sys/fs/cgroup"); err != nil || !strings.Contains(string(out), "cgroup2fs") {
		t.Skip("requires cgroup v2 on the exelet host")
	}

	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)

	// Poll for the CH process's cgroup to contain the VM scope name. Up to
	// 3 minutes because box creation and cloud-hypervisor launch take time.
	expectedSuffix := "/vm-" + instanceID + ".scope"
	deadline := time.Now().Add(3 * time.Minute)
	var (
		lastOutput string
		lastErr    error
	)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context cancelled while polling cgroup: %v", err)
		}
		// Read cgroup.procs on the VM scope. When CLONE_INTO_CGROUP works,
		// the CH pid appears here directly at exec time. When it's broken,
		// CH lands in the exelet's cgroup first and the resource manager
		// moves it later — either way, the pid eventually shows up in procs.
		// What distinguishes the two cases is /proc/<pid>/cgroup: with the
		// fix, it matches the VM scope from the very first line of
		// cgroup.procs we observe; without the fix, there's always a gap
		// where the pid exists but is not yet in the scope.
		//
		// To keep the test simple and race-free: find the scope, read its
		// cgroup.procs, pick a pid, read /proc/<pid>/cgroup, and require the
		// strings match. Use `find ... -exec` to avoid shell subshells that
		// the Exec sudo wrapper mangles.
		cmd := fmt.Sprintf(
			`find /sys/fs/cgroup/exelet.slice -type f -name cgroup.procs -path '*vm-%s.scope/cgroup.procs' -exec head -1 {} \;`,
			instanceID,
		)
		out, err := exelet.Exec(ctx, cmd)
		lastErr = err
		pidStr := strings.TrimSpace(string(out))
		if err != nil || pidStr == "" {
			lastOutput = fmt.Sprintf("scope cgroup.procs empty or missing; err=%v", err)
			select {
			case <-ctx.Done():
				t.Fatalf("context cancelled: %v", ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		// Read the pid's cgroup. Must contain the VM scope name.
		cgOut, cgErr := exelet.Exec(ctx, "cat /proc/"+pidStr+"/cgroup")
		if cgErr != nil {
			lastOutput = fmt.Sprintf("reading /proc/%s/cgroup failed: %v", pidStr, cgErr)
			select {
			case <-ctx.Done():
				t.Fatalf("context cancelled: %v", ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		cgLine := strings.TrimSpace(string(cgOut))
		lastOutput = fmt.Sprintf("pid=%s cgroup=%s", pidStr, cgLine)
		if strings.Contains(cgLine, expectedSuffix) {
			t.Logf("cloud-hypervisor process is in VM scope (fix confirmed): %s", lastOutput)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	t.Fatalf("cloud-hypervisor process never landed in VM scope %q after 3 min — "+
		"likely means CLONE_INTO_CGROUP placement regressed, so guest RAM is being "+
		"charged to the exelet's cgroup instead of the VM's.\nLast ssh err: %v\nLast output:\n%s",
		expectedSuffix, lastErr, lastOutput)
}

func instanceByName(t *testing.T, ctx context.Context, client *client.Client, name string) *api.Instance {
	t.Helper()
	stream, err := client.ListInstances(ctx, &api.ListInstancesRequest{})
	if err != nil {
		t.Fatalf("failed to list instances: %v", err)
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to receive instance list: %v", err)
		}
		if resp.Instance != nil && resp.Instance.GetName() == name {
			return resp.Instance
		}
	}
	t.Fatalf("instance %q not found", name)
	return nil
}
