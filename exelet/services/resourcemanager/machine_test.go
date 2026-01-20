package resourcemanager

import (
	"bytes"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// These tests read the actual file on the test system,
// so they only run on Linux. This gives us some confidence
// that the parsing is correct, which we would not get by
// using fake data.

func TestReadLoadAverage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test on %s", runtime.GOOS)
	}

	load, err := readLoadAverage(t.Context())
	if err != nil {
		t.Errorf("readLoadAverage failed: %v", err)
	} else {
		t.Logf("load average is %v", load)
	}
}

func TestReadMemInfo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test on %s", runtime.GOOS)
	}

	info, err := readMemInfo(t.Context())
	if err != nil {
		t.Errorf("readMemInfo failed: %v", err)
	} else {
		t.Logf("memory info is %#v", info)
	}
}

func TestReadDiskInfo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test on %s", runtime.GOOS)
	}

	t.Parallel()

	info, err := readDiskInfoDir(t.Context(), ".")
	if err != nil {
		t.Fatalf("readDiskInfo failed: %v", err)
	}
	t.Logf("disk info is %#v", info)

	args := []string{
		"-k",
		"--output=size,avail",
		".",
	}
	out, err := exec.Command("df", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("df failed: %v\n%s", err, out)
	}

	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("df output does not have 2 lines\n%s", out)
	}

	fields := bytes.Fields(lines[1])
	if len(fields) != 2 {
		t.Fatalf("df output second line does not have two fields\n%s", out)
	}

	blocks, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil {
		t.Errorf("could not parse blocks value %q: %v", fields[0], err)
	}
	avail, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil {
		t.Errorf("could not parse avail value %q: %v", fields[1], err)
	}

	if info.diskTotal != blocks {
		t.Errorf("df block count %d != diskInfo block count %d", blocks, info.diskTotal)
	}

	// Do a rough comparison of available disk,
	// as it may be changing.
	diff := info.diskFree - avail
	if diff < 0 {
		diff = -diff
	}
	if diff > 1<<20 {
		t.Errorf("df block avail %d too different from diskInfo block free %d", avail, info.diskFree)
	}
}

func TestGatewayInterface(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test on %s", runtime.GOOS)
	}

	gateway, err := gatewayInterface(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The test is just that the function didn't fail.
	t.Logf("gateway interface is %s", gateway)
}

func TestReadInterfaceStats(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test on %s", runtime.GOOS)
	}

	t.Parallel()

	info, err := readInterfaceStats(t.Context())
	if err != nil {
		if strings.Contains(err.Error(), "Please check if data collecting is enabled") || strings.Contains(err.Error(), "no sar report") {
			t.Skipf("skipping test because interface stats are not collected: %v", err)
		}

		t.Fatal(err)
	}
	// The test is just that the function didn't fail.
	t.Logf("gateway network stats: %#v", info)
}
