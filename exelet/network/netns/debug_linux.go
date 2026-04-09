//go:build linux

package netns

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"
)

// DeviceInfo holds diagnostic information about a single network device.
type DeviceInfo struct {
	Name      string
	Location  string // "root" or netns name
	State     string // "UP", "DOWN", etc.
	Master    string // bridge name if attached
	Type      string // "tuntap", "veth", "bridge", etc.
	Addrs     []string
	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
	RxDropped uint64
	TxDropped uint64
	MTU       int
	MAC       string
	Error     string // non-empty if device couldn't be read
}

// DiagResult holds the full diagnostic output for one instance.
type DiagResult struct {
	InstanceID string
	NsName     string
	ExtIP      string
	Devices    []DeviceInfo
	NsIPTables string // iptables -L -n -v inside the netns
	NsRoutes   string // ip route inside the netns
}

// Diagnose collects full diagnostic info for a given instance ID.
func Diagnose(ctx context.Context, instanceID string) (*DiagResult, error) {
	ns := NsName(instanceID)
	vid := vmID(instanceID)

	result := &DiagResult{
		InstanceID: instanceID,
		NsName:     ns,
	}

	// Root-ns devices.
	for _, d := range []struct {
		name  string
		label string
	}{
		{"tap-" + vid, "tap"},
		{"br-" + vid, "per-vm-bridge"},
		{"vb-" + vid, "inner-veth-root"},
		{"vx-" + vid, "outer-veth-root"},
	} {
		info := readLinkInfo(d.name, "root")
		info.Type = d.label
		result.Devices = append(result.Devices, info)
	}

	// Netns devices + iptables + routes.
	nsDevices, iptOut, routeOut, extIP, err := probeNetns(ctx, ns, vid)
	if err != nil {
		// Netns might not exist; record what we can.
		result.Devices = append(result.Devices, DeviceInfo{
			Name:     ns,
			Location: "netns",
			Error:    err.Error(),
		})
	} else {
		result.Devices = append(result.Devices, nsDevices...)
		result.NsIPTables = iptOut
		result.NsRoutes = routeOut
		result.ExtIP = extIP
	}

	return result, nil
}

// FormatDiag writes a human-readable diagnostic report to w.
func FormatDiag(w io.Writer, d *DiagResult) {
	fmt.Fprintf(w, "Instance: %s\n", d.InstanceID)
	fmt.Fprintf(w, "Netns:    %s\n", d.NsName)
	fmt.Fprintf(w, "Ext IP:   %s\n", d.ExtIP)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "── Devices ──")
	for _, dev := range d.Devices {
		if dev.Error != "" {
			fmt.Fprintf(w, "  %-14s [%s] ERROR: %s\n", dev.Name, dev.Location, dev.Error)
			continue
		}
		fmt.Fprintf(w, "  %-14s [%s] %s  state=%s  mtu=%d  mac=%s\n",
			dev.Name, dev.Location, dev.Type, dev.State, dev.MTU, dev.MAC)
		if dev.Master != "" {
			fmt.Fprintf(w, "%19smaster=%s\n", "", dev.Master)
		}
		for _, a := range dev.Addrs {
			fmt.Fprintf(w, "%19saddr %s\n", "", a)
		}
		fmt.Fprintf(w, "%19srx: %d pkts  %d bytes  %d errors  %d dropped\n",
			"", dev.RxPackets, dev.RxBytes, dev.RxErrors, dev.RxDropped)
		fmt.Fprintf(w, "%19stx: %d pkts  %d bytes  %d errors  %d dropped\n",
			"", dev.TxPackets, dev.TxBytes, dev.TxErrors, dev.TxDropped)
	}

	if d.NsRoutes != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "── Routes (in %s) ──\n", d.NsName)
		for _, line := range strings.Split(strings.TrimSpace(d.NsRoutes), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	if d.NsIPTables != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "── IPTables (in %s) ──\n", d.NsName)
		for _, line := range strings.Split(strings.TrimSpace(d.NsIPTables), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// DiagnoseAll returns diagnostics for all netns instances found on the system.
func DiagnoseAll(ctx context.Context) ([]*DiagResult, error) {
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ip netns list: %w", err)
	}

	var results []*DiagResult
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.Fields(line)
		if len(name) == 0 {
			continue
		}
		ns := name[0]
		if !strings.HasPrefix(ns, "exe-") {
			continue
		}
		// Extract vmid from namespace name (exe-vm000003 → vm000003).
		vid := strings.TrimPrefix(ns, "exe-")
		diag := diagByVMID(ctx, vid)
		results = append(results, diag)
	}
	return results, nil
}

// DiagnoseByVMID collects diagnostics using just the vmid prefix (e.g. "vm000003").
func DiagnoseByVMID(ctx context.Context, vid string) *DiagResult {
	return diagByVMID(ctx, vid)
}

// diagByVMID builds a DiagResult using just the vmid prefix (when full instance ID is unknown).
func diagByVMID(ctx context.Context, vid string) *DiagResult {
	ns := "exe-" + vid
	result := &DiagResult{
		InstanceID: vid,
		NsName:     ns,
	}

	for _, d := range []struct {
		name  string
		label string
	}{
		{"tap-" + vid, "tap"},
		{"br-" + vid, "per-vm-bridge"},
		{"vb-" + vid, "inner-veth-root"},
		{"vx-" + vid, "outer-veth-root"},
	} {
		info := readLinkInfo(d.name, "root")
		info.Type = d.label
		result.Devices = append(result.Devices, info)
	}

	nsDevices, iptOut, routeOut, extIP, err := probeNetns(ctx, ns, vid)
	if err != nil {
		result.Devices = append(result.Devices, DeviceInfo{
			Name:     ns,
			Location: "netns",
			Error:    err.Error(),
		})
	} else {
		result.Devices = append(result.Devices, nsDevices...)
		result.NsIPTables = iptOut
		result.NsRoutes = routeOut
		result.ExtIP = extIP
	}

	return result
}

func readLinkInfo(name, location string) DeviceInfo {
	info := DeviceInfo{Name: name, Location: location}

	link, err := netlink.LinkByName(name)
	if err != nil {
		info.Error = err.Error()
		return info
	}

	attrs := link.Attrs()
	info.State = attrs.OperState.String()
	if info.State == "unknown" {
		// Fallback: check flags.
		if attrs.Flags&0x1 != 0 { // IFF_UP
			info.State = "UP"
		} else {
			info.State = "DOWN"
		}
	}
	info.MTU = attrs.MTU
	info.MAC = attrs.HardwareAddr.String()

	if attrs.MasterIndex > 0 {
		if master, err := netlink.LinkByIndex(attrs.MasterIndex); err == nil {
			info.Master = master.Attrs().Name
		}
	}

	if stats := attrs.Statistics; stats != nil {
		info.RxBytes = stats.RxBytes
		info.TxBytes = stats.TxBytes
		info.RxPackets = stats.RxPackets
		info.TxPackets = stats.TxPackets
		info.RxErrors = stats.RxErrors
		info.TxErrors = stats.TxErrors
		info.RxDropped = stats.RxDropped
		info.TxDropped = stats.TxDropped
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err == nil {
		for _, a := range addrs {
			info.Addrs = append(info.Addrs, a.IPNet.String())
		}
	}

	return info
}

// probeNetns enters a network namespace and reads device info, iptables, and routes.
func probeNetns(ctx context.Context, nsName, vid string) (devices []DeviceInfo, iptables, routes, extIP string, err error) {
	type nsResult struct {
		devices  []DeviceInfo
		iptables string
		routes   string
		extIP    string
		err      error
	}

	ch := make(chan nsResult, 1)
	go func() {
		runtime.LockOSThread()
		// Don't unlock — the OS thread's netns is tainted.

		origNS, err := uns.Get()
		if err != nil {
			ch <- nsResult{err: fmt.Errorf("get current netns: %w", err)}
			return
		}
		defer origNS.Close()

		targetNS, err := uns.GetFromName(nsName)
		if err != nil {
			ch <- nsResult{err: fmt.Errorf("open netns %s: %w", nsName, err)}
			return
		}
		defer targetNS.Close()

		if err := uns.Set(targetNS); err != nil {
			ch <- nsResult{err: fmt.Errorf("enter netns %s: %w", nsName, err)}
			return
		}
		defer uns.Set(origNS) //nolint:errcheck

		var r nsResult

		// Read devices inside the namespace.
		for _, d := range []struct {
			name  string
			label string
		}{
			{"vg-" + vid, "inner-veth-ns (gateway)"},
			{"ve-" + vid, "outer-veth-ns (outbound)"},
		} {
			info := readLinkInfo(d.name, nsName)
			info.Type = d.label
			r.devices = append(r.devices, info)

			// Extract ext IP from the outbound veth.
			if strings.HasPrefix(d.name, "ve-") && len(info.Addrs) > 0 {
				// Strip the /16 mask.
				parts := strings.SplitN(info.Addrs[0], "/", 2)
				r.extIP = parts[0]
			}
		}

		ch <- r
	}()

	r := <-ch
	if r.err != nil {
		return nil, "", "", "", r.err
	}

	// Run iptables and ip-route via `ip netns exec` (they need their own process).
	iptOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"iptables", "-L", "-n", "-v", "--line-numbers").CombinedOutput()
	natOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"iptables", "-t", "nat", "-L", "-n", "-v", "--line-numbers").CombinedOutput()
	routeOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"ip", "route").CombinedOutput()

	var iptBuf strings.Builder
	iptBuf.WriteString("# filter\n")
	iptBuf.Write(iptOut)
	iptBuf.WriteString("\n# nat\n")
	iptBuf.Write(natOut)

	return r.devices, iptBuf.String(), string(routeOut), r.extIP, nil
}
