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
	disconnect(t, pty)

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
	disconnect(t, pty)
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
	pty, _, err := testinfra.MakePTY("", "ssh localhost", true)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := serverEnv.SSHToExeDev(t.Context(), pty, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)
	box2 := makeBox(t, pty, keyFile, email)
	disconnect(t, pty)
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
	defer disconnect(t, pty1)

	email2 := "testloadedhost2" + testinfra.FakeEmailSuffix
	pty2, _, keyFile2 := registerEmail(t, email2)
	defer disconnect(t, pty2)

	boxName1 := makeBox(t, pty1, keyFile1, email1)
	defer deleteBox(t, boxName1, keyFile1)

	boxName2 := makeBox(t, pty2, keyFile2, email2)
	defer deleteBox(t, boxName2, keyFile2)

	boxes := boxHosts(t)

	t.Logf("after creating two boxes: %v", boxes)

	// Tell the first exelet to report a load of 50.

	client := exelets[0].Client()
	usageResponse, err := client.GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	usage := usageResponse.Usage
	usage.LoadAverage = 50
	_, err = client.SetMachineUsage(t.Context(),
		&resourceapi.SetMachineUsageRequest{
			Available: true,
			Usage:     usage,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := client.SetMachineUsage(t.Context(),
			&resourceapi.SetMachineUsageRequest{
				Available: true,
			},
		)
		if err != nil {
			t.Errorf("failed to reset usage to default: %v", err)
		}
	}()

	// Tell exed to update its view of exelet usage.
	url := fmt.Sprintf("http://localhost:%d/update-exelet-usage-517c8a904", serverEnv.Exed.HTTPPort)
	if _, err = http.Get(url); err != nil {
		t.Fatalf("failed to tell exed to update exelet usage: %v", err)
	}

	// Make two new boxes.

	email3 := "testloadedhost3" + testinfra.FakeEmailSuffix
	pty3, _, keyFile3 := registerEmail(t, email3)
	defer disconnect(t, pty3)

	boxName3 := makeBox(t, pty3, keyFile3, email3)
	defer deleteBox(t, boxName3, keyFile3)

	email4 := "testloadedhost4" + testinfra.FakeEmailSuffix
	pty4, _, keyFile4 := registerEmail(t, email4)
	defer disconnect(t, pty4)

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
