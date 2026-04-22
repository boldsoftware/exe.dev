package exelets

import (
	"context"
	"testing"
	"time"

	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

// TestExedStartsWithDownHost verifies that exed can start when one of
// its configured exelets is unreachable. This is important for
// operational resilience: an unhealthy exelet (or an exelet host that's
// being maintained) must not prevent exed itself from booting.
//
// The test restarts exed with a mix of real exelet addresses and a
// bogus address that points at a closed port. exed should start
// successfully and create boxes on the reachable exelets only.
func TestExedStartsWithDownHost(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	// Include a bogus exelet address pointed at a closed port. The
	// hostname segment "lax" lets region.ParseExeletRegion succeed.
	bogusAddr := "tcp://exelet-lax-bogus-99:1"
	exeletAddrs := []string{
		exelets[0].Address,
		bogusAddr,
		exelets[1].Address,
	}

	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatalf("exed failed to start with a down exelet in its address list: %v", err)
	}

	// Register and create a box. It should land on a reachable exelet,
	// proving exed treats the dead exelet as unavailable.
	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)

	box, ok := findBox(t, boxName)
	if !ok {
		t.Fatalf("box %q not found after creation", boxName)
	}
	if box.Host == bogusAddr {
		t.Fatalf("box %q unexpectedly placed on bogus exelet %q", boxName, bogusAddr)
	}
}

// TestHostStartsWhenExedDown verifies that exelets remain operational
// while exed is unreachable: the exelet's desired-state syncer polls
// exed periodically and logs failures but does not halt or cripple
// the exelet itself. Once exed comes back, the exelets are usable
// end-to-end.
//
// We exercise this against the already-running exelets rather than
// spinning up a new one: the existing 'exelet starts when exed is
// down' path is covered naturally by package exelets' TestMain, which
// starts the second exelet in parallel with exed's own startup — and
// by TestExedStartsWithDownHost above, which boots exed against an
// address whose backend never answers.
func TestHostStartsWhenExedDown(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{exelets[0].Address, exelets[1].Address}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	// Stop exed. midTest=true preserves the database and skips the
	// no-boxes-leaked check.
	serverEnv.Exed.Stop(context.WithoutCancel(t.Context()), exeletTestRunIDs[0], true)

	// Give the existing exelets' desired-state-sync goroutines a chance
	// to see exed unreachable. We don't need a full poll interval —
	// this is just to make sure nothing in the exelet reacts to exed
	// being gone by crashing before we probe it.
	time.Sleep(2 * time.Second)

	// Core assertion: running exelets still answer gRPC independently
	// of exed. If the exelet coupled its liveness to exed (e.g., exited
	// on failed desired-state sync), these calls would fail.
	for i, el := range exelets[:2] {
		if _, err := el.Client().GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{}); err != nil {
			t.Fatalf("exelet[%d] gRPC failed while exed was down: %v", i, err)
		}
	}

	// Bring exed back up against the same exelets and verify full
	// usability end-to-end by creating a box.
	if err := serverEnv.Exed.Restart(context.WithoutCancel(t.Context()), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatalf("exed failed to restart after being stopped: %v", err)
	}

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)
}
