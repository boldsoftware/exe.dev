package e1e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCgroupMemoryPlacement verifies that cloud-hypervisor is placed directly
// into the VM's cgroup scope via CLONE_INTO_CGROUP at exec time. Without this
// placement, guest RAM page faults are charged to whichever cgroup CH was in
// when it first touched pages — typically the exelet's cgroup — and stay
// there forever (cgroup v2 does not reparent charges when a process is moved).
//
// The assertion is on a structured log event the VMM emits from
// applyCgroupPlacement immediately after opening the target cgroup fd and
// setting SysProcAttr.UseCgroupFD. That event only fires on the success path:
// if the CgroupPathFunc is nil, the provider returns "", or the open fails,
// the VMM returns before emitting it. So finding a record with id=<our VM>
// and path=.../vm-<id>.scope is direct proof the placement happened.
//
// Scope-file / memory.current alternatives don't work here: e1e runs with
// --enable-hugepages, so guest RAM bypasses memory.current; and cgroup.procs
// eventually contains the pid in both the working and broken cases (the RM
// moves the pid later).
func TestCgroupMemoryPlacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux")
	}

	logDir := os.Getenv("E1E_LOG_DIR")
	if logDir == "" {
		t.Skip("requires E1E_LOG_DIR to scrape exelet logs")
	}
	logPath := filepath.Join(logDir, "exelet.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Skipf("exelet log %s not present: %v", logPath, err)
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

	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)
	expectedSuffix := "/vm-" + instanceID + ".scope"

	deadline := time.Now().Add(3 * time.Minute)
	var lastMatches []string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context cancelled while scraping logs: %v", err)
		}
		matched, path, allMatches, err := findPlacementEvent(logPath, instanceID)
		if err != nil {
			t.Fatalf("read exelet log %s: %v", logPath, err)
		}
		lastMatches = allMatches
		if matched {
			if !strings.HasSuffix(path, expectedSuffix) {
				t.Fatalf("cgroup placement event found for instance %s but path %q does not end with %q — VM was placed in the wrong cgroup",
					instanceID, path, expectedSuffix)
			}
			t.Logf("CLONE_INTO_CGROUP placement confirmed: id=%s path=%s", instanceID, path)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	t.Fatalf("no CLONE_INTO_CGROUP placement event for instance %s in %s after 3 min — "+
		"likely means the VMM fell back to spawning in the exelet's cgroup, so guest RAM "+
		"is being charged to the exelet instead of the VM's scope.\nLast matching records: %v",
		instanceID, logPath, lastMatches)
}

// findPlacementEvent scans exelet.log for a JSON record whose msg is the
// cgroup-placement event and whose id matches instanceID. Returns the path
// field from that record so the caller can verify it names the VM scope.
func findPlacementEvent(logPath, instanceID string) (bool, string, []string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return false, "", nil, err
	}
	defer f.Close()

	var allMatches []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), "placing cloud-hypervisor into cgroup at exec") {
			continue
		}
		// Capture every placement record, even ones for other instances,
		// so a failure dump can show what the exelet did log (helps tell
		// "never logged at all" from "logged for the wrong id").
		allMatches = append(allMatches, string(line))
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if id, _ := rec["id"].(string); id != instanceID {
			continue
		}
		path, _ := rec["path"].(string)
		return true, path, allMatches, nil
	}
	return false, "", allMatches, sc.Err()
}
