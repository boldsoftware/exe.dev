package exelets

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestTwoHosts(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	// Don't use t.Context here, the restarted exed should be
	// around for other tests.
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

	// Check the list of boxes to see where they wound up.
	url := fmt.Sprintf("http://localhost:%d/debug/boxes?format=json", serverEnv.Exed.HTTPPort)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s returned status %d", url, resp.StatusCode)
	}

	var boxes []struct {
		Host string `json:"host"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&boxes); err != nil {
		t.Fatalf("failed to JSON decode %s: %v", url, err)
	}

	resp.Body.Close()

	t.Logf("after creating two boxes: %v", boxes)

	if len(boxes) != 2 {
		t.Errorf("got %d boxes, want 2", len(boxes))
	}
	var exelets []string
	for _, box := range boxes {
		if !slices.Contains(exelets, box.Host) {
			exelets = append(exelets, box.Host)
		}
	}
	if len(exelets) != 1 {
		t.Errorf("boxes found on %d exelets, want 1", len(exelets))
	}

	deleteBox(t, box1, keyFile)
	deleteBox(t, box2, keyFile)
}
