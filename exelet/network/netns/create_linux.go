//go:build linux

package netns

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// IFA_F_NOPREFIXROUTE prevents automatic route creation when adding an address.
const ifaFNoPrefixRoute = 0x200

func (m *Manager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	tap := tapName(id)
	ns := nsName(id)
	br := brName(id)
	vBr := vethBrName(id) // root ns, on per-VM bridge
	vGw := vethGwName(id) // netns, VM gateway (10.42.0.1)
	vXH := vethXHost(id)  // root ns, on shared bridge
	vXN := vethXNS(id)    // netns, outbound

	extIP, err := m.allocateExtIP(id)
	if err != nil {
		return nil, fmt.Errorf("allocate ext IP: %w", err)
	}

	// Track what was created for rollback.
	type state struct {
		ns, br, tap, innerVeth, outerVeth, nsCfg bool
	}
	var s state

	cleanup := func() {
		if s.outerVeth {
			delLinkByName(vXH)
		}
		if s.innerVeth {
			delLinkByName(vBr)
		}
		if s.tap {
			delLinkByName(tap)
		}
		if s.br {
			delLinkByName(br)
		}
		if s.ns {
			_ = exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run()
		}
		m.releaseExtIP(id)
	}

	// 1. Clean up any stale interfaces from a previous instance with the
	// same ID (e.g. failed delete, migrate-back). Delete root-ns veth peers
	// first — this cascade-deletes their netns-side peers — then remove
	// the stale namespace so we start completely clean.
	delLinkByName(vBr)
	delLinkByName(vXH)
	delLinkByName(tap)
	delLinkByName(br)
	if nsExists(ns) {
		_ = exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run()
	}
	if err := exec.CommandContext(ctx, "ip", "netns", "add", ns).Run(); err != nil {
		cleanup()
		return nil, fmt.Errorf("create netns %s: %w", ns, err)
	}
	s.ns = true
	_ = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up").Run()

	nsHandle, err := uns.GetFromName(ns)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("get netns handle %s: %w", ns, err)
	}
	defer nsHandle.Close()

	// 2. Per-VM bridge with gateway IP (noprefixroute to avoid route table bloat).
	// The gateway IP lets the SSH proxy reach the VM via SO_BINDTODEVICE.
	brLink, err := ensureBridge(br)
	if err != nil {
		cleanup()
		return nil, err
	}
	s.br = true

	brAddr, err := netlink.ParseAddr(GatewayCIDR)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("parse bridge addr: %w", err)
	}
	brAddr.Flags |= ifaFNoPrefixRoute
	if err := netlink.AddrReplace(brLink, brAddr); err != nil {
		cleanup()
		return nil, fmt.Errorf("assign bridge addr: %w", err)
	}

	// 3. TAP device on per-VM bridge.
	tapLink, err := ensureTap(tap, brLink)
	if err != nil {
		cleanup()
		return nil, err
	}
	s.tap = true

	// Defense-in-depth: isolate the TAP at L2 even though the per-VM bridge
	// only has the TAP and the inner veth (no other peers to talk to today).
	if err := netlink.LinkSetIsolated(tapLink, true); err != nil {
		cleanup()
		return nil, fmt.Errorf("isolate tap %s: %w", tap, err)
	}

	// 4. Inner veth pair: vBr (root ns, on per-VM bridge) <-> vGw (netns, gateway).
	if err := ensureVethPair(vBr, vGw); err != nil {
		cleanup()
		return nil, fmt.Errorf("create inner veth: %w", err)
	}
	s.innerVeth = true

	if err := attachToBridge(vBr, brLink); err != nil {
		cleanup()
		return nil, err
	}
	if err := moveToNS(vGw, nsHandle); err != nil {
		cleanup()
		return nil, err
	}

	// 5. Outer veth pair: vXH (root ns, on shared bridge) <-> vXN (netns, outbound).
	if err := ensureVethPair(vXH, vXN); err != nil {
		cleanup()
		return nil, fmt.Errorf("create outer veth: %w", err)
	}
	s.outerVeth = true

	// Select a shared bridge with available capacity.
	sharedBridgeName, needsNewBridge := m.selectBridgeAndIncrement()
	if needsNewBridge {
		m.bridgeCreateMu.Lock()
		sharedBridgeName, needsNewBridge = m.selectBridgeAndIncrement()
		if needsNewBridge {
			newBridgeName := m.reserveNextBridge()
			if err := m.createSecondaryBridge(ctx, newBridgeName); err != nil {
				m.bridgeCreateMu.Unlock()
				cleanup()
				return nil, fmt.Errorf("create secondary shared bridge: %w", err)
			}
			sharedBridgeName = m.addBridgeAndSelect(newBridgeName)
		}
		m.bridgeCreateMu.Unlock()
	}

	sharedBr, err := netlink.LinkByName(sharedBridgeName)
	if err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, fmt.Errorf("get shared bridge %s: %w", sharedBridgeName, err)
	}
	if err := attachToBridge(vXH, sharedBr); err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, err
	}
	if err := moveToNS(vXN, nsHandle); err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, err
	}
	m.setVMBridge(id, sharedBridgeName)

	// 6. Configure networking inside the namespace.
	if err := m.configureNS(ctx, ns, vGw, vXN, extIP); err != nil {
		cleanup()
		return nil, fmt.Errorf("configure netns: %w", err)
	}
	s.nsCfg = true

	// 7. Build response.
	mac, err := randomMAC()
	if err != nil {
		cleanup()
		return nil, err
	}

	iface := &api.NetworkInterface{
		Name:       tap,
		DeviceName: DeviceName,
		Type:       api.NetworkInterface_TYPE_TAP,
		MACAddress: mac,
		IP: &api.IPAddress{
			IPV4:      VMIP + "/16",
			GatewayV4: VMGateway,
		},
		Nameservers: m.nameservers,
		Network:     VMSubnet,
		NTPServer:   m.ntpServer,
		Router:      m.router,
	}

	m.log.InfoContext(ctx, "created netns network interface",
		"instance", id, "tap", tap, "netns", ns,
		"bridge", br, "shared_bridge", sharedBridgeName, "ext_ip", extIP,
	)

	return iface, nil
}

// configureNS sets up routing, NAT, and firewall rules inside a per-VM namespace.
func (m *Manager) configureNS(ctx context.Context, ns, vGw, vXN, extIP string) error {
	run := func(prog string, args ...string) error {
		all := append([]string{"netns", "exec", ns, prog}, args...)
		out, err := exec.CommandContext(ctx, "ip", all...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %v: %w (%s)", prog, args, err, out)
		}
		return nil
	}
	ipt := func(args ...string) error { return run("iptables", args...) }

	// Inner veth: VM gateway.
	if err := run("ip", "addr", "add", GatewayCIDR, "dev", vGw); err != nil {
		return err
	}
	if err := run("ip", "link", "set", vGw, "up"); err != nil {
		return err
	}

	// Outer veth: outbound.
	if err := run("ip", "addr", "add", extIP+"/16", "dev", vXN); err != nil {
		return err
	}
	if err := run("ip", "link", "set", vXN, "up"); err != nil {
		return err
	}

	// Default route.
	if err := run("ip", "route", "add", "default", "via", SharedBridgeGateway, "dev", vXN); err != nil {
		return err
	}

	// IP forwarding.
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}

	// SNAT outbound VM traffic to this namespace's ext IP. Using SNAT
	// instead of MASQUERADE avoids a per-packet interface address lookup
	// — the ext IP is static for the VM's lifetime. This is the inner
	// NAT hop (10.42.0.42 → 10.99.x.x); the outer hop on the shared
	// bridge (10.99.x.x → host IP) remains MASQUERADE since the host's
	// outbound IP may change.
	if err := ipt("-t", "nat", "-A", "POSTROUTING", "-s", VMSubnet, "-o", vXN, "-j", "SNAT", "--to-source", extIP); err != nil {
		return err
	}

	// Forwarding.
	if err := ipt("-A", "FORWARD", "-i", vGw, "-o", vXN, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := ipt("-A", "FORWARD", "-i", vXN, "-o", vGw, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return err
	}

	// Block carrier-grade NAT.
	if err := ipt("-I", "FORWARD", "-i", vGw, "-d", CarrierNATCIDR, "-j", "DROP"); err != nil {
		return err
	}

	// Block inter-VM traffic. All VMs share the same outbound bridge
	// (10.99.0.0/16). Without this rule, a VM could send packets to
	// another VM's ext IP on the shared bridge. The gateway IP
	// (10.99.0.1) must be exempted — it's needed for outbound routing
	// and the metadata service.
	// -I inserts at position 1, so we insert DROP first, then ACCEPT.
	// Final chain order: ACCEPT 10.99.0.1 → DROP 10.99.0.0/16 → ...
	if err := ipt("-I", "FORWARD", "-i", vGw, "-d", SharedBridgeNetwork, "-j", "DROP"); err != nil {
		return err
	}
	if err := ipt("-I", "FORWARD", "-i", vGw, "-d", SharedBridgeGateway, "-j", "ACCEPT"); err != nil {
		return err
	}

	// Metadata DNAT.
	if err := ipt("-t", "nat", "-A", "PREROUTING", "-i", vGw, "-d", MetadataIP, "-p", "tcp", "--dport", "80", "-j", "DNAT", "--to-destination", SharedBridgeGateway+":80"); err != nil {
		return err
	}
	if err := ipt("-t", "nat", "-A", "PREROUTING", "-i", vGw, "-d", MetadataIP, "-p", "tcp", "--dport", "443", "-j", "DNAT", "--to-destination", SharedBridgeGateway+":2443"); err != nil {
		return err
	}

	// Gateway firewall.
	if err := ipt("-A", "INPUT", "-i", vGw, "-d", VMGateway, "-p", "tcp", "--syn", "-j", "DROP"); err != nil {
		return err
	}

	// Connection limit.
	_ = ipt("-I", "FORWARD", "-s", VMIP, "-m", "connlimit",
		"--connlimit-above", fmt.Sprintf("%d", m.connLimit), "--connlimit-mask", "32", "-j", "DROP")

	// Bandwidth limit.
	if !m.disableBandwidth {
		if err := m.applyBandwidthInNS(ctx, ns, vXN); err != nil {
			return err
		}
	}

	return nil
}

// ApplyConnectionLimit ensures the connlimit rule exists inside the VM's
// network namespace. The rule is created at namespace setup time, but this
// method re-applies it on startup in case the namespace was recreated
// without it.
func (m *Manager) ApplyConnectionLimit(ctx context.Context, inst *api.Instance) error {
	ns := nsName(inst.ID)
	ruleArgs := []string{
		"-s", VMIP,
		"-m", "connlimit",
		"--connlimit-above", fmt.Sprintf("%d", m.connLimit),
		"--connlimit-mask", "32",
		"-j", "DROP",
	}

	// Check if the rule already exists.
	checkArgs := append([]string{"netns", "exec", ns, "iptables", "-C", "FORWARD"}, ruleArgs...)
	if exec.CommandContext(ctx, "ip", checkArgs...).Run() == nil {
		return nil
	}

	m.log.DebugContext(ctx, "re-applying connlimit rule in namespace", "ns", ns, "limit", m.connLimit)
	insertArgs := append([]string{"netns", "exec", ns, "iptables", "-I", "FORWARD"}, ruleArgs...)
	if out, err := exec.CommandContext(ctx, "ip", insertArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("connlimit rule in %s: %w (%s)", ns, err, out)
	}
	return nil
}

// ApplySourceIPFilter is a no-op for the netns manager: each VM lives in
// its own network namespace and has a unique ext IP on the shared bridge,
// so there is no cross-VM source-IP impersonation surface to protect.
func (m *Manager) ApplySourceIPFilter(ctx context.Context, inst *api.Instance) error {
	return nil
}

// ApplyBandwidthLimit applies bandwidth limiting to a VM's outbound veth.
func (m *Manager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	if m.disableBandwidth {
		return nil
	}
	ns := nsName(id)
	vXN := vethXNS(id)
	return m.applyBandwidthInNS(ctx, ns, vXN)
}

// applyBandwidthInNS applies HTB bandwidth shaping on the outbound veth egress.
func (m *Manager) applyBandwidthInNS(ctx context.Context, ns, dev string) error {
	run := func(args ...string) error {
		all := append([]string{"netns", "exec", ns}, args...)
		out, err := exec.CommandContext(ctx, "ip", all...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %w (%s)", args, err, out)
		}
		return nil
	}

	_ = run("tc", "qdisc", "del", "dev", dev, "root")

	if err := run("tc", "qdisc", "add", "dev", dev, "root", "handle", "1:", "htb", "default", "10"); err != nil {
		return fmt.Errorf("add htb qdisc: %w", err)
	}

	if err := run("tc", "class", "add", "dev", dev, "parent", "1:", "classid", "1:10", "htb",
		"rate", m.bandwidthRate,
		"ceil", m.bandwidthCeil,
		"burst", m.bandwidthBurst,
		"cburst", m.bandwidthBurst,
	); err != nil {
		return fmt.Errorf("add htb class: %w", err)
	}

	if err := run("tc", "qdisc", "add", "dev", dev, "parent", "1:10", "handle", "10:", "fq_codel"); err != nil {
		return fmt.Errorf("add fq_codel: %w", err)
	}

	return nil
}

// --- netlink helpers ---

func nsExists(name string) bool {
	h, err := uns.GetFromName(name)
	if err != nil {
		return false
	}
	h.Close()
	return true
}

func ensureBridge(name string) (netlink.Link, error) {
	if link, err := netlink.LinkByName(name); err == nil {
		return link, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	br := &netlink.Bridge{LinkAttrs: attrs}
	if err := netlink.LinkAdd(br); err != nil {
		return nil, fmt.Errorf("create bridge %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(br); err != nil {
		return nil, fmt.Errorf("up bridge %s: %w", name, err)
	}
	return br, nil
}

func ensureTap(name string, master netlink.Link) (netlink.Link, error) {
	if link, err := netlink.LinkByName(name); err == nil {
		return link, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.MasterIndex = master.Attrs().Index
	tap := &netlink.Tuntap{LinkAttrs: attrs, Mode: netlink.TUNTAP_MODE_TAP}
	if err := netlink.LinkAdd(tap); err != nil {
		return nil, fmt.Errorf("create tap %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return nil, fmt.Errorf("up tap %s: %w", name, err)
	}
	return tap, nil
}

func ensureVethPair(a, b string) error {
	if _, err := netlink.LinkByName(a); err == nil {
		return nil
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: a},
		PeerName:  b,
	}
	return netlink.LinkAdd(veth)
}

func attachToBridge(vethName string, bridge netlink.Link) error {
	link, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("get %s: %w", vethName, err)
	}
	if err := netlink.LinkSetMaster(link, bridge); err != nil {
		return fmt.Errorf("attach %s to bridge: %w", vethName, err)
	}
	return netlink.LinkSetUp(link)
}

func moveToNS(name string, ns uns.NsHandle) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("get %s for ns move: %w", name, err)
	}
	return netlink.LinkSetNsFd(link, int(ns))
}

func delLinkByName(name string) {
	if link, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(link)
	}
}

// delLinkByNameErr deletes a network link, returning an error if the link
// exists but cannot be deleted. Returns nil if the link does not exist.
func delLinkByNameErr(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil // link doesn't exist, nothing to do
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete link %s: %w", name, err)
	}
	return nil
}
