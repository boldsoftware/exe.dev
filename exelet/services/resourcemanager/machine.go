package resourcemanager

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
	"golang.org/x/sys/unix"
)

// machineUsage returns the overall machine usage.
//
// This only returns an error if fetching all information fails.
// If some succeeds, we log errors and return what we have.
//
// This caches the results for up to a minute.
func (m *ResourceManager) machineUsage(ctx context.Context) (*api.GetMachineUsageResponse, error) {
	m.machineUsageMu.Lock()
	defer m.machineUsageMu.Unlock()

	available := m.machineAvailable

	var usage *api.MachineUsage

	// If we've been told the usage to return, use it.
	if m.machineSetUsage != nil {
		usage = m.machineSetUsage
	}

	// Otherwise, if we've cached data in the last minute, use that.
	if usage == nil {
		if !m.machineUsageCacheTime.IsZero() && time.Since(m.machineUsageCacheTime) < time.Minute {
			usage = m.machineUsageCache
		}
	}

	// Otherwise, read from the system, and cache the result.
	if usage == nil {
		var err error
		usage, err = m.readUsage(ctx)
		if err != nil {
			m.machineUsageCacheTime = time.Time{}
			return nil, err
		}

		m.machineUsageCache = usage
		m.machineUsageCacheTime = time.Now()
	}

	ret := &api.GetMachineUsageResponse{
		Available: available,
		Usage:     usage,
	}
	return ret, nil
}

// readUsage reads the current machine usage.
func (m *ResourceManager) readUsage(ctx context.Context) (*api.MachineUsage, error) {
	var ret api.MachineUsage

	found := false

	load, err := readLoadAverage(ctx)
	if err != nil {
		slog.WarnContext(ctx, "failed to read load average", "error", err)
	} else {
		ret.LoadAverage = load

		found = true
	}

	mem, err := readMemInfo(ctx)
	if err != nil {
		slog.WarnContext(ctx, "failed to read memory info", "error", err)
	} else {
		ret.MemTotal = mem.memTotal
		ret.MemFree = mem.memFree
		ret.MemAvailable = mem.memAvailable
		ret.SwapTotal = mem.swapTotal
		ret.SwapFree = mem.swapFree

		found = true
	}

	disk, err := m.readDiskInfo(ctx)
	if err != nil {
		slog.WarnContext(ctx, "failed to read disk info", "error", err)
	} else {
		ret.DiskTotal = disk.diskTotal
		ret.DiskFree = disk.diskFree

		found = true
	}

	net, err := readInterfaceStats(ctx)
	if err != nil {
		slog.WarnContext(ctx, "failed to read network info", "error", err)
	} else {
		ret.RxBytesRate = net.rxBytesRate
		ret.TxBytesRate = net.txBytesRate

		found = true
	}

	if !found {
		// We didn't find any valid information.
		// Return an error.
		return nil, err
	}

	return &ret, nil
}

// readLoadAverage returns the 15 minute load average of the host.
func readLoadAverage(ctx context.Context) (float32, error) {
	f, err := os.Open("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var ld1, ld5, ld15 float32
	if _, err := fmt.Fscan(f, &ld1, &ld5, &ld15); err != nil {
		return 0, err
	}
	return ld15, nil
}

// memInfo holds information about memory and swap.
// All values are in KiB.
type memInfo struct {
	memTotal     int64
	memFree      int64
	memAvailable int64
	swapTotal    int64
	swapFree     int64
}

// readMemInfo returns information about memory and swap.
func readMemInfo(ctx context.Context) (memInfo, error) {
	return readMemInfoFile(ctx, "/proc/meminfo")
}

// readMemInfoFile permits specifying the meminfo file, for tests.
func readMemInfoFile(ctx context.Context, filename string) (memInfo, error) {
	f, err := os.Open(filename)
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()

	set := func(val []byte, place *int64) error {
		num, err := strconv.ParseInt(string(val), 10, 64)
		if err != nil {
			return err
		}
		*place = num
		return nil
	}

	var info memInfo
	scanner := bufio.NewScanner(f)
	found := uint(0)
	for scanner.Scan() {
		fields := bytes.Fields(scanner.Bytes())
		if len(fields) != 3 {
			continue
		}
		switch {
		case bytes.Equal(fields[0], []byte("MemTotal:")):
			if err := set(fields[1], &info.memTotal); err != nil {
				return memInfo{}, err
			}
			found |= 1 << 0
		case bytes.Equal(fields[0], []byte("MemFree:")):
			if err := set(fields[1], &info.memFree); err != nil {
				return memInfo{}, err
			}
			found |= 1 << 1
		case bytes.Equal(fields[0], []byte("MemAvailable:")):
			if err := set(fields[1], &info.memAvailable); err != nil {
				return memInfo{}, err
			}
			found |= 1 << 2
		case bytes.Equal(fields[0], []byte("SwapTotal:")):
			if err := set(fields[1], &info.swapTotal); err != nil {
				return memInfo{}, err
			}
			found |= 1 << 3
		case bytes.Equal(fields[0], []byte("SwapFree:")):
			if err := set(fields[1], &info.swapFree); err != nil {
				return memInfo{}, err
			}
			found |= 1 << 4
		}
	}
	if err := scanner.Err(); err != nil {
		return memInfo{}, err
	}

	const wantFields = (1 << 5) - 1
	if found != wantFields {
		return memInfo{}, fmt.Errorf("found fields %#x in /proc/meminfo, want %#x", found, wantFields)
	}

	return info, nil
}

// diskInfo holds information about disk space.
// All values are in KiB.
type diskInfo struct {
	diskTotal int64
	diskFree  int64
}

// readDiskInfo returns information about disk space.
func (m *ResourceManager) readDiskInfo(ctx context.Context) (diskInfo, error) {
	return readDiskInfoDir(ctx, m.config.DataDir)
}

// readDiskInfoDir returns information about space on a named disk.
func readDiskInfoDir(ctx context.Context, dir string) (diskInfo, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return diskInfo{}, err
	}
	// Convert from blocks to KiB. Blocks/Bavail are in units of the
	// fundamental block size (Frsize on Linux, Bsize on Darwin).
	bsz := statfsBlockSize(&fs)
	total := int64(fs.Blocks) * bsz / 1024
	avail := int64(fs.Bavail) * bsz / 1024
	di := diskInfo{
		diskTotal: int64(total),
		diskFree:  int64(avail),
	}
	return di, nil
}

// netInfo holds statistics for the network.
type netInfo struct {
	rxBytesRate float32 // bytes received per second last tem minutes
	txBytesRate float32 // bytes sent per second last ten minutes
}

// gatewayInterface returns the name of the gateway interface on Linux.
func gatewayInterface(ctx context.Context) (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := bytes.Fields(scanner.Bytes())
		if len(fields) > 1 && bytes.Equal(fields[1], []byte("00000000")) {
			return string(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning /proc/net/route for default interface: %v", err)
	}
	return "", errors.New("no gateway interface found")
}

// interfaceStats returns network transmission rates to the
// broader interface, as reported by the sar program.
func readInterfaceStats(ctx context.Context) (netInfo, error) {
	gateway, err := gatewayInterface(ctx)
	if err != nil {
		return netInfo{}, err
	}

	start := time.Now().Add(-20 * time.Minute).Format("15:04:05")
	info, missing, err := sarStats(ctx, gateway, start)
	if err == nil {
		return info, err
	}
	if !missing {
		return netInfo{}, err
	}

	// The stats roll over at midnight, so if the info is missing
	// collect all the available stats.

	info, _, err = sarStats(ctx, gateway, "")
	return info, err
}

// sarStats uses sar to read network transmission rates.
// If start is set it is used as the starting time.
// The bool result reports whether all data was absent.
func sarStats(ctx context.Context, gateway, start string) (netInfo, bool, error) {
	args := []string{
		"-n", "DEV",
		"--iface=" + gateway,
	}
	if start != "" {
		args = append(args, "-s", start)
	}
	out, err := exec.CommandContext(ctx, "sar", args...).CombinedOutput()
	if err != nil {
		return netInfo{}, false, fmt.Errorf("running sar %v failed: %v\n%s", args, err, out)
	}

	for line := range bytes.Lines(out) {
		if !bytes.HasPrefix(line, []byte("Average:")) {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if bytes.Equal(fields[1], []byte("IFACE")) {
			if !bytes.Equal(fields[4], []byte("rxkB/s")) || !bytes.Equal(fields[5], []byte("txkB/s")) {
				return netInfo{}, false, fmt.Errorf("misunderstanding of sar fields: %q", line)
			}
			continue
		}

		rxkb, err := strconv.ParseFloat(string(fields[4]), 32)
		if err != nil {
			return netInfo{}, false, fmt.Errorf("can't parse rxkb field %q: %v", fields[4], err)
		}
		txkb, err := strconv.ParseFloat(string(fields[5]), 32)
		if err != nil {
			return netInfo{}, false, fmt.Errorf("can't parse txkb field %q: %v", fields[5], err)
		}

		ret := netInfo{
			rxBytesRate: float32(rxkb * 1024),
			txBytesRate: float32(txkb * 1024),
		}

		return ret, false, err
	}

	return netInfo{}, true, fmt.Errorf("no sar report for gateway interface %s\n%s", gateway, out)
}

// setMachineUsage sets the machine usage for testing.
func (m *ResourceManager) setMachineUsage(ctx context.Context, req *api.SetMachineUsageRequest) error {
	m.machineUsageMu.Lock()
	defer m.machineUsageMu.Unlock()

	m.machineAvailable = req.GetAvailable()

	if usage := req.GetUsage(); usage != nil {
		m.machineSetUsage = usage
	} else {
		m.machineSetUsage = nil
	}

	m.machineUsageCacheTime = time.Time{}

	return nil
}
