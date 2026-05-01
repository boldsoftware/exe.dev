package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemwatchFreezeWakesOnActivity boots a VM, waits for it to reach
// Frozen tier (configured with very short thresholds via env), then runs
// a CPU burn inside the guest and asserts the VM wakes back to Active.
//
// Requires exelet env overrides:
//
//	EXELET_MEMWATCH_FREEZE_IDLE_WINDOW=2s
//	EXELET_MEMWATCH_FREEZE_MIN_UPTIME=0
//	EXELET_MEMWATCH_FROZEN_CADENCE=24h
//
// These are set in the e1e test harness's exelet launch config.
func TestMemwatchFreezeWakesOnActivity(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	if len(Env.servers.Exelets) == 0 {
		t.Fatal("no exelets")
	}
	exelet := Env.servers.Exelets[0]

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)
	waitForSSH(t, boxName, keyFile)

	ctx := Env.context(t)
	exeletClient := exelet.Client()
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)

	type debugEntry struct {
		ID         string  `json:"ID"`
		VMTier     string  `json:"VMTier"`
		LastCPUPct float64 `json:"LastCPUPct"`
	}
	type debugResp struct {
		Entries []debugEntry `json:"entries"`
	}

	getEntry := func() debugEntry {
		url := fmt.Sprintf("%s/debug/vms/guest-metrics.json", exelet.HTTPAddress)
		resp, err := http.Get(url)
		if err != nil {
			return debugEntry{}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var dr debugResp
		if err := json.Unmarshal(body, &dr); err != nil {
			return debugEntry{}
		}
		for _, e := range dr.Entries {
			if e.ID == instanceID {
				return e
			}
		}
		return debugEntry{}
	}
	getVMTier := func() string { return getEntry().VMTier }

	// Wait for the VM to reach Frozen (with IdleWindow=2s this should
	// happen within ~30s of boot + initial scrape).
	require.Eventually(t, func() bool {
		return getVMTier() == "frozen"
	}, 90*time.Second, 500*time.Millisecond, "VM did not reach Frozen tier")

	t.Log("VM is frozen, starting CPU burn")

	// Launch a sustained CPU burner in the guest. A one-shot dd | sha256sum
	// finishes in ~1s and can complete between two 5s RM polls without ever
	// being seen as busy on the wake side. nohup + disown keeps a busy loop
	// pinned to one vCPU until the VM is destroyed at test cleanup.
	// Run the burner in its own goroutine. The ssh client may wait for
	// the remote shell to close, so we never block the test on it: the
	// goroutine logs whatever shows up if/when it returns. The remote
	// command starts a long-running busy loop in its own session and
	// then exits the foreground shell (`exit 0`) so most ssh builds drop
	// the channel promptly. We log the launch attempt for debugging.
	t.Log("launching cpu burn")
	go func() {
		cmd := boxSSHCommand(t, boxName, keyFile,
			"sh", "-c",
			// setsid + nohup + redirect everything so ssh's channel can close;
			// then verify the burn is actually running before exiting.
			"setsid nohup sh -c 'dd if=/dev/zero of=/dev/null' </dev/null >/dev/null 2>&1 & setsid nohup sh -c 'dd if=/dev/zero of=/dev/null' </dev/null >/dev/null 2>&1 & sleep 1; pgrep -af dd | head -5; cat /proc/loadavg; exit 0",
		)
		out, err := cmd.CombinedOutput()
		t.Logf("cpu burn ssh returned: err=%v out=%q", err, string(out))
	}()

	// Assert the VM wakes within 60s (a few RM poll cycles + scrape latency).
	woke := assert.Eventually(t, func() bool {
		return getVMTier() == "active"
	}, 60*time.Second, 1*time.Second, "VM did not wake to Active after CPU burn")
	if !woke {
		e := getEntry()
		t.Logf("final entry: tier=%s lastCPUPct=%.2f", e.VMTier, e.LastCPUPct)
	}

	t.Log("VM woke to active")
}
