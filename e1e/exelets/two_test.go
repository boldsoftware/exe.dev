package exelets

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"exe.dev/e1e/testinfra"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

func TestTwoHosts(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()

	cmd := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "true")
	cmd.Stdout = t.Output()
	cmd.Stderr = t.Output()
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run true: %v", err)
	}

	deleteBox(t, boxName, keyFile)
}

func TestUserOnSingleHost(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	// Start exed with a single exelet.
	exeletAddrs := []string{exelets[0].Address}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	// Create a box on that exelet.
	pty, _, keyFile, email := register(t)
	box1 := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, box1, keyFile)

	// Restart exed with two exelets.
	exeletAddrs = []string{
		exelets[0].Address,
		exelets[1].Address,
	}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], true); err != nil {
		t.Fatal(err)
	}

	// Create another box.
	pty, _ = testinfra.MakeTestPTY(t, "", "ssh localhost", true)
	cmd, err := serverEnv.SSHToExeDev(t.Context(), pty.PTY(), keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)
	box2 := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, box2, keyFile)

	// Check the list of boxes to see where they wound up.
	boxes := boxHosts(t)

	t.Logf("after creating two boxes: %v", boxes)

	cnt := 0
	for _, boxList := range boxes {
		cnt += len(boxList)
	}
	if cnt != 2 {
		t.Errorf("got %d boxes, want 2", cnt)
	}

	if len(boxes) != 1 {
		t.Errorf("boxes found on %d exelets, want 1", len(boxes))
	}
}

func TestLoadedHost(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	email1 := "testloadedhost1" + testinfra.FakeEmailSuffix
	pty1, _, keyFile1 := registerEmail(t, email1)
	defer pty1.Disconnect()

	email2 := "testloadedhost2" + testinfra.FakeEmailSuffix
	pty2, _, keyFile2 := registerEmail(t, email2)
	defer pty2.Disconnect()

	boxName1 := makeBox(t, pty1, keyFile1, email1)
	defer deleteBox(t, boxName1, keyFile1)

	boxName2 := makeBox(t, pty2, keyFile2, email2)
	defer deleteBox(t, boxName2, keyFile2)

	boxes := boxHosts(t)

	t.Logf("after creating two boxes: %v", boxes)

	// Override usage on both exelets so that exelet[0] appears heavily
	// loaded and exelet[1] appears idle.  We must also set DiskFree and
	// MemAvailable to large values because CI VMs have small disks/memory
	// that would otherwise trigger the "extreme usage" check in
	// exeletUsageCmp, causing both exelets to compare as equal regardless
	// of LoadAverage.
	for i, el := range exelets[:2] {
		resp, err := el.Client().GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		usage := resp.Usage
		if i == 0 {
			usage.LoadAverage = 50
		} else {
			usage.LoadAverage = 0
		}
		usage.MemAvailable = 64 << 20 // 64 GiB in KiB — well above extreme threshold
		usage.DiskFree = 100 << 20    // 100 GiB in KiB — well above extreme threshold
		_, err = el.Client().SetMachineUsage(t.Context(),
			&resourceapi.SetMachineUsageRequest{
				Available: true,
				Usage:     usage,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, el := range exelets[:2] {
			_, err := el.Client().SetMachineUsage(t.Context(),
				&resourceapi.SetMachineUsageRequest{
					Available: true,
				},
			)
			if err != nil {
				t.Errorf("failed to reset usage to default: %v", err)
			}
		}
	}()

	// Tell exed to update its view of exelet usage.
	url := fmt.Sprintf("http://localhost:%d/update-exelet-usage-517c8a904", serverEnv.Exed.HTTPPort)
	if _, err := http.Get(url); err != nil {
		t.Fatalf("failed to tell exed to update exelet usage: %v", err)
	}

	// Verify the usage update took effect on both exelets.
	for i, el := range exelets[:2] {
		resp, err := el.Client().GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{})
		if err != nil {
			t.Logf("exelet[%d] GetMachineUsage error: %v", i, err)
		} else if resp.Usage != nil {
			t.Logf("exelet[%d] addr=%s load=%.1f mem=%d swap=%d disk=%d",
				i, el.Address, resp.Usage.LoadAverage,
				resp.Usage.MemAvailable, resp.Usage.SwapFree, resp.Usage.DiskFree)
		}
	}

	// Make two new boxes.

	email3 := "testloadedhost3" + testinfra.FakeEmailSuffix
	pty3, _, keyFile3 := registerEmail(t, email3)
	defer pty3.Disconnect()

	boxName3 := makeBox(t, pty3, keyFile3, email3)
	defer deleteBox(t, boxName3, keyFile3)

	boxes = boxHosts(t)
	t.Logf("after box3: %v", boxes)

	// Re-trigger usage update before box4 to rule out stale state.
	if _, err := http.Get(url); err != nil {
		t.Fatalf("failed second update-exelet-usage: %v", err)
	}

	email4 := "testloadedhost4" + testinfra.FakeEmailSuffix
	pty4, _, keyFile4 := registerEmail(t, email4)
	defer pty4.Disconnect()

	boxName4 := makeBox(t, pty4, keyFile4, email4)
	defer deleteBox(t, boxName4, keyFile4)

	// Both new boxes should wind up on the second exelet.

	boxes = boxHosts(t)

	t.Logf("after creating four boxes: %v", boxes)

	if !slices.Contains(boxes[exeletAddrs[1]], boxName3) {
		t.Errorf("box3 is not on expected exelet %s", exeletAddrs[1])
	}
	if !slices.Contains(boxes[exeletAddrs[1]], boxName4) {
		t.Errorf("box4 is not on expected exelet %s", exeletAddrs[1])
	}
}
