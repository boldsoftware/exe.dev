package e1e

import (
	"testing"

	api "exe.dev/pkg/api/exe/resource/v1"
)

func TestMachineUsage(t *testing.T) {
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// This messes with exelet availability, so not marked parallel.

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

	boxName := newBox(t, pty)
	defer pty.deleteBox(boxName)

	waitForSSH(t, boxName, keyFile)

	exeletClient := Env.servers.Exelets[0].Client()

	ctx := Env.context(t)
	usage, err := exeletClient.GetMachineUsage(ctx, &api.GetMachineUsageRequest{})
	if err != nil {
		t.Fatalf("GetMachineUsage failed: %v", err)
	}

	if !usage.GetAvailable() {
		t.Error("exelet is not marked available")
	}

	setUsage := &api.SetMachineUsageRequest{
		Available: false,
		Usage: &api.MachineUsage{
			LoadAverage: 100,
		},
	}
	if _, err := exeletClient.SetMachineUsage(ctx, setUsage); err != nil {
		t.Fatalf("SetMachineUsage failed: %v", err)
	}

	usage, err = exeletClient.GetMachineUsage(ctx, &api.GetMachineUsageRequest{})
	if err != nil {
		t.Fatalf("GetMachineUsage failed: %v", err)
	}

	if usage.GetAvailable() {
		t.Error("exelet marked available after setting it unavailable")
	}
	if got := usage.GetUsage().LoadAverage; got != 100 {
		t.Errorf("exelet returned load %v after setting, expected %v", got, float32(100))
	}

	setUsage = &api.SetMachineUsageRequest{
		Available: true,
		Usage:     nil,
	}
	if _, err := exeletClient.SetMachineUsage(ctx, setUsage); err != nil {
		t.Fatalf("SetMachineUsage failed: %v", err)
	}
}
