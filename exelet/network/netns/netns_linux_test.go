//go:build linux

package netns

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	api "exe.dev/pkg/api/exe/compute/v1"

	"github.com/vishvananda/netlink"
)

func skipUnlessRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
}

func TestNewManager(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestNewManagerBadScheme(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	_, err := NewManager("nat:///tmp", log)
	if err == nil {
		t.Fatal("expected error for wrong scheme")
	}
}

func TestNaming(t *testing.T) {
	id := "vm000003-orbit-falcon"
	// All names should be <= 15 chars (IFNAMSIZ).
	for _, name := range []string{
		tapName(id), nsName(id), brName(id),
		vethBrName(id), vethGwName(id), vethXHost(id), vethXNS(id),
	} {
		if len(name) > 15 {
			t.Errorf("%q is %d chars, exceeds IFNAMSIZ (15)", name, len(name))
		}
	}

	// Verify vmid extraction and naming.
	if got := tapName(id); got != "tap-vm000003" {
		t.Errorf("tapName(%q) = %q, want tap-vm000003", id, got)
	}
	if got := NsName(id); got != "exe-vm000003" {
		t.Errorf("NsName(%q) = %q, want exe-vm000003", id, got)
	}
	if got := BridgeName(id); got != "br-vm000003" {
		t.Errorf("BridgeName(%q) = %q, want br-vm000003", id, got)
	}
}

func TestAllocateExtIP(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	ip1, err := m.allocateExtIP("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1 != "10.99.0.2" {
		t.Fatalf("expected 10.99.0.2, got %s", ip1)
	}

	ip2, err := m.allocateExtIP("vm-2")
	if err != nil {
		t.Fatal(err)
	}
	if ip2 != "10.99.0.3" {
		t.Fatalf("expected 10.99.0.3, got %s", ip2)
	}

	// Re-allocating same ID returns same IP.
	ip1again, err := m.allocateExtIP("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1again != ip1 {
		t.Fatalf("re-alloc returned %s, want %s", ip1again, ip1)
	}

	// Release and re-allocate.
	m.releaseExtIP("vm-1")
	ip3, err := m.allocateExtIP("vm-3")
	if err != nil {
		t.Fatal(err)
	}
	if ip3 != "10.99.0.4" {
		t.Fatalf("expected 10.99.0.4, got %s", ip3)
	}
}

func TestGetInstanceByExtIP(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	m.allocateExtIP("vm-abc")

	id, ok := m.GetInstanceByExtIP("10.99.0.2")
	if !ok || id != "vm-abc" {
		t.Fatalf("got (%q, %v), want (vm-abc, true)", id, ok)
	}

	_, ok = m.GetInstanceByExtIP("10.99.0.99")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestAllocateExtIPRollover(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	// Advance cursor to near the end of the octet3=255 range so the
	// full wrap happens after a small number of allocations.
	m.mu.Lock()
	m.nextOctet3 = 255
	m.nextOctet4 = 252
	m.mu.Unlock()

	// Allocate the last few IPs before wrap.
	var preWrapIPs []string
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("vm-pre-%d", i)
		ip, err := m.allocateExtIP(id)
		if err != nil {
			t.Fatalf("pre-wrap alloc %d failed: %v", i, err)
		}
		preWrapIPs = append(preWrapIPs, ip)
	}
	if preWrapIPs[0] != "10.99.255.252" {
		t.Fatalf("expected 10.99.255.252, got %s", preWrapIPs[0])
	}

	// After wrap the cursor resets; next alloc should succeed and land
	// back in the 10.99.0.x range (skipping .0 and .1 which is the gateway).
	ip, err := m.allocateExtIP("vm-post-wrap")
	if err != nil {
		t.Fatalf("post-wrap alloc failed: %v", err)
	}
	if ip != "10.99.0.2" {
		t.Fatalf("expected 10.99.0.2 after wrap, got %s", ip)
	}

	// Release a pre-wrap IP and verify it can be re-allocated.
	m.releaseExtIP("vm-pre-0")
	ip2, err := m.allocateExtIP("vm-reuse")
	if err != nil {
		t.Fatal(err)
	}
	// The cursor is past the released IP, so it will continue forward
	// and only find the released IP after wrapping again OR on a
	// subsequent scan. Either way, it must be a valid unique IP.
	for _, existing := range preWrapIPs[1:] {
		if ip2 == existing {
			t.Fatalf("re-allocated an IP still in use: %s", ip2)
		}
	}
}

func TestAllocateExtIPExhaustion(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	// Pre-fill the used map with every IP except one, simulating a
	// nearly-full pool without actually calling allocateExtIP 65K times.
	m.mu.Lock()
	var freeIP string
	var freeID string
	n := 0
	for o3 := 0; o3 <= 255; o3++ {
		start := 1
		if o3 == 0 {
			start = 2 // skip .0 and .1 (gateway)
		}
		for o4 := start; o4 <= 255; o4++ {
			ip := fmt.Sprintf("10.99.%d.%d", o3, o4)
			id := fmt.Sprintf("vm-fill-%d", n)
			if ip == "10.99.42.42" {
				freeIP = ip
				freeID = id
				n++
				continue // leave one hole
			}
			m.extIPs[id] = ip
			n++
		}
	}
	m.mu.Unlock()

	// Allocate — should find the single free IP.
	gotIP, err := m.allocateExtIP("vm-last")
	if err != nil {
		t.Fatalf("expected to find the one free IP, got error: %v", err)
	}
	if gotIP != freeIP {
		t.Fatalf("expected %s, got %s", freeIP, gotIP)
	}

	// Now truly full — must fail.
	_, err = m.allocateExtIP("vm-overflow")
	if err == nil {
		t.Fatal("expected error when pool is exhausted")
	}

	// Release one and verify recovery.
	m.releaseExtIP(freeID)
	// freeID's IP was never stored (we skipped it), so release a real one.
	m.releaseExtIP("vm-fill-0")
	recoveredIP, err := m.allocateExtIP("vm-recovered")
	if err != nil {
		t.Fatalf("expected allocation after release, got: %v", err)
	}
	if recoveredIP == "" {
		t.Fatal("recovered empty IP")
	}
}

func TestAllocateExtIPOctetBoundary(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	// Advance the cursor to just before the octet4 rollover (octet3=0, octet4=254).
	m.mu.Lock()
	m.nextOctet3 = 0
	m.nextOctet4 = 254
	m.mu.Unlock()

	ip1, err := m.allocateExtIP("vm-boundary-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1 != "10.99.0.254" {
		t.Fatalf("expected 10.99.0.254, got %s", ip1)
	}

	ip2, err := m.allocateExtIP("vm-boundary-2")
	if err != nil {
		t.Fatal(err)
	}
	if ip2 != "10.99.0.255" {
		t.Fatalf("expected 10.99.0.255, got %s", ip2)
	}

	// octet4 wraps to 0, which increments octet3 and sets octet4=1.
	ip3, err := m.allocateExtIP("vm-boundary-3")
	if err != nil {
		t.Fatal(err)
	}
	if ip3 != "10.99.1.1" {
		t.Fatalf("expected 10.99.1.1 after octet boundary, got %s", ip3)
	}
}

// TestCreateDeleteInterface is an integration test that actually creates
// network namespaces and interfaces. Requires root.
func TestCreateDeleteInterface(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}

	// Start sets up the shared bridge.
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() {
		// Clean up shared bridge.
		delLinkByName(SharedBridge)
	})

	id := "vm000001-test-instance"

	iface, err := m.CreateInterface(ctx, id)
	if err != nil {
		t.Fatal("CreateInterface:", err)
	}

	// Verify returned interface.
	if iface.IP.IPV4 != VMIP+"/16" {
		t.Errorf("IP = %q, want %q", iface.IP.IPV4, VMIP+"/16")
	}
	if iface.IP.GatewayV4 != VMGateway {
		t.Errorf("Gateway = %q, want %q", iface.IP.GatewayV4, VMGateway)
	}
	if iface.MACAddress == "" {
		t.Error("expected non-empty MAC")
	}

	// Verify TAP exists in root ns.
	tap := tapName(id)
	if _, err := netlink.LinkByName(tap); err != nil {
		t.Errorf("TAP %s not found: %v", tap, err)
	}

	// Verify per-VM bridge exists.
	br := brName(id)
	if _, err := netlink.LinkByName(br); err != nil {
		t.Errorf("bridge %s not found: %v", br, err)
	}

	// Verify netns exists.
	ns := nsName(id)
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal("ip netns list:", err)
	}
	if !strings.Contains(string(out), ns) {
		t.Errorf("netns %s not in list: %s", ns, out)
	}

	// Verify connectivity inside netns: gateway veth has IP.
	out, err = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "addr", "show").CombinedOutput()
	if err != nil {
		t.Fatal("ip addr in netns:", err)
	}
	if !strings.Contains(string(out), VMGateway) {
		t.Errorf("gateway IP %s not found in netns: %s", VMGateway, out)
	}

	// Verify iptables rules inside netns.
	out, err = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "iptables", "-t", "nat", "-L", "-n").CombinedOutput()
	if err != nil {
		t.Fatal("iptables in netns:", err)
	}
	if !strings.Contains(string(out), "SNAT") {
		t.Error("SNAT rule not found in netns")
	}
	if !strings.Contains(string(out), MetadataIP) {
		t.Error("metadata DNAT rule not found in netns")
	}

	// Verify ext IP tracking.
	extIP, ok := m.getExtIP(id)
	if !ok {
		t.Fatal("ext IP not tracked")
	}
	lookedUp, ok := m.GetInstanceByExtIP(extIP)
	if !ok || lookedUp != id {
		t.Fatalf("GetInstanceByExtIP(%s) = (%q, %v), want (%q, true)", extIP, lookedUp, ok, id)
	}

	// Delete.
	if err := m.DeleteInterface(ctx, id, ""); err != nil {
		t.Fatal("DeleteInterface:", err)
	}

	// Verify cleanup.
	if _, err := netlink.LinkByName(tap); err == nil {
		t.Errorf("TAP %s still exists after delete", tap)
	}
	if _, err := netlink.LinkByName(br); err == nil {
		t.Errorf("bridge %s still exists after delete", br)
	}
	vBr := vethBrName(id)
	vXH := vethXHost(id)
	if _, err := netlink.LinkByName(vBr); err == nil {
		t.Errorf("veth %s still exists after delete", vBr)
	}
	if _, err := netlink.LinkByName(vXH); err == nil {
		t.Errorf("veth %s still exists after delete", vXH)
	}
	out, err = exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ns) {
		t.Errorf("netns %s still exists after delete", ns)
	}
	_, ok = m.getExtIP(id)
	if ok {
		t.Error("ext IP still tracked after delete")
	}
}

// TestTwoVMs verifies that two VMs get the same internal IP but different
// ext IPs, and that their namespaces are isolated.
func TestTwoVMs(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id1 := "vm000002-alpha"
	id2 := "vm000003-beta"

	iface1, err := m.CreateInterface(ctx, id1)
	if err != nil {
		t.Fatal("CreateInterface vm1:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id1, "") })

	iface2, err := m.CreateInterface(ctx, id2)
	if err != nil {
		t.Fatal("CreateInterface vm2:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id2, "") })

	// Both get same VM IP.
	if iface1.IP.IPV4 != iface2.IP.IPV4 {
		t.Errorf("VM IPs differ: %s vs %s", iface1.IP.IPV4, iface2.IP.IPV4)
	}
	if iface1.IP.IPV4 != VMIP+"/16" {
		t.Errorf("VM IP = %q, want %q", iface1.IP.IPV4, VMIP+"/16")
	}

	// Different ext IPs.
	ext1, _ := m.getExtIP(id1)
	ext2, _ := m.getExtIP(id2)
	if ext1 == ext2 {
		t.Errorf("ext IPs should differ: both %s", ext1)
	}

	// Both namespaces exist and are distinct.
	ns1 := nsName(id1)
	ns2 := nsName(id2)
	if ns1 == ns2 {
		t.Fatal("namespace names collide")
	}

	// Verify each namespace has the gateway IP.
	for _, ns := range []string{ns1, ns2} {
		out, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "addr").CombinedOutput()
		if err != nil {
			t.Fatalf("ip addr in %s: %v", ns, err)
		}
		if !strings.Contains(string(out), VMGateway) {
			t.Errorf("ns %s missing gateway %s", ns, VMGateway)
		}
	}

	// Verify metadata lookup by ext IP.
	got1, ok := m.GetInstanceByExtIP(ext1)
	if !ok || got1 != id1 {
		t.Errorf("lookup ext1: got (%q, %v)", got1, ok)
	}
	got2, ok := m.GetInstanceByExtIP(ext2)
	if !ok || got2 != id2 {
		t.Errorf("lookup ext2: got (%q, %v)", got2, ok)
	}
}

// TestRecoverExtIPs verifies that after an exelet restart (simulated by
// clearing the in-memory map), RecoverExtIPs rebuilds the map from kernel state.
func TestRecoverExtIPs(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id1 := "vm000004-recover-one"
	id2 := "vm000005-recover-two"

	_, err = m.CreateInterface(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id1, "") })

	_, err = m.CreateInterface(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id2, "") })

	// Remember the ext IPs before "restart".
	ext1Before, _ := m.getExtIP(id1)
	ext2Before, _ := m.getExtIP(id2)
	if ext1Before == "" || ext2Before == "" {
		t.Fatal("ext IPs not allocated")
	}

	// Simulate exelet restart: create a fresh manager with empty state.
	m2, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}

	// Before recovery: map is empty.
	_, ok := m2.GetInstanceByExtIP(ext1Before)
	if ok {
		t.Fatal("fresh manager should have empty ext IP map")
	}

	// Recover.
	if err := m2.RecoverExtIPs(ctx, []string{id1, id2}); err != nil {
		t.Fatal("RecoverExtIPs:", err)
	}

	// After recovery: same IPs recovered.
	ext1After, ok := m2.getExtIP(id1)
	if !ok || ext1After != ext1Before {
		t.Errorf("id1 ext IP: got %q, want %q", ext1After, ext1Before)
	}
	ext2After, ok := m2.getExtIP(id2)
	if !ok || ext2After != ext2Before {
		t.Errorf("id2 ext IP: got %q, want %q", ext2After, ext2Before)
	}

	// Metadata lookup works.
	gotID, ok := m2.GetInstanceByExtIP(ext1Before)
	if !ok || gotID != id1 {
		t.Errorf("GetInstanceByExtIP(%s) = (%q, %v), want (%q, true)", ext1Before, gotID, ok, id1)
	}

	// New allocations don't collide with recovered IPs.
	newIP, err := m2.allocateExtIP("vm000006-brand-new")
	if err != nil {
		t.Fatal(err)
	}
	if newIP == ext1Before || newIP == ext2Before {
		t.Errorf("new allocation %s collides with recovered IP", newIP)
	}
}

// getExtIP is a test helper to check ext IP state.
func (m *Manager) getExtIP(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ip, ok := m.extIPs[id]
	return ip, ok
}

// TestReconcileLeasesDeletesOrphans verifies that ReconcileLeases deletes
// namespaces and kernel resources for instances that no longer exist.
func TestReconcileLeasesDeletesOrphans(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	// Create two VMs.
	id1 := "vm000010-kept"
	id2 := "vm000011-orphan"

	_, err = m.CreateInterface(ctx, id1)
	if err != nil {
		t.Fatal("CreateInterface id1:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id1, "") })

	_, err = m.CreateInterface(ctx, id2)
	if err != nil {
		t.Fatal("CreateInterface id2:", err)
	}
	// NOT cleaning up id2 via DeleteInterface — reconcile should handle it.

	// Verify both namespaces exist.
	for _, ns := range []string{nsName(id1), nsName(id2)} {
		out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), ns) {
			t.Fatalf("expected netns %s to exist", ns)
		}
	}

	// Reconcile with only id1 as valid — id2 should be cleaned up.
	cleaned, err := m.ReconcileLeases(ctx, []*api.Instance{{ID: id1}})
	if err != nil {
		t.Fatal("ReconcileLeases:", err)
	}
	if len(cleaned) != 1 {
		t.Fatalf("expected 1 cleaned namespace, got %d: %v", len(cleaned), cleaned)
	}
	if cleaned[0] != nsName(id2) {
		t.Fatalf("expected %s to be cleaned, got %s", nsName(id2), cleaned[0])
	}

	// Verify orphan's namespace is gone.
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), nsName(id2)) {
		t.Errorf("orphaned netns %s still exists after reconcile", nsName(id2))
	}

	// Verify orphan's root-ns devices are gone.
	for _, dev := range []string{brName(id2), tapName(id2), vethBrName(id2), vethXHost(id2)} {
		if _, err := netlink.LinkByName(dev); err == nil {
			t.Errorf("orphaned device %s still exists after reconcile", dev)
		}
	}

	// Verify the kept VM is still intact.
	if _, err := netlink.LinkByName(tapName(id1)); err != nil {
		t.Errorf("kept VM TAP %s was incorrectly deleted", tapName(id1))
	}
	out, err = exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), nsName(id1)) {
		t.Errorf("kept VM netns %s was incorrectly deleted", nsName(id1))
	}
}

// TestReconcileLeasesNoOrphans verifies that reconcile is a no-op when there
// are no orphaned namespaces.
func TestReconcileLeasesNoOrphans(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id := "vm000012-only"
	_, err = m.CreateInterface(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id, "") })

	cleaned, err := m.ReconcileLeases(ctx, []*api.Instance{{ID: id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 0 {
		t.Fatalf("expected 0 cleaned, got %d: %v", len(cleaned), cleaned)
	}
}

// TestBridgeSplitting verifies that the manager tracks bridge port counts
// and creates secondary bridges when the primary is full.
func TestBridgeSplitting(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}

	// Override max ports per bridge to a small value for testing.
	m.maxPortsPerBridge = 2

	// Verify initial state.
	if len(m.bridges) != 1 {
		t.Fatalf("expected 1 bridge, got %d", len(m.bridges))
	}
	if m.bridges[0].name != SharedBridge {
		t.Fatalf("expected primary bridge %s, got %s", SharedBridge, m.bridges[0].name)
	}

	// First two selections should go to the primary bridge.
	name1, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name1 != SharedBridge {
		t.Fatalf("first select: got (%s, %v), want (%s, false)", name1, needsNew, SharedBridge)
	}
	name2, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name2 != SharedBridge {
		t.Fatalf("second select: got (%s, %v), want (%s, false)", name2, needsNew, SharedBridge)
	}

	// Third selection should need a new bridge (primary is full at 2).
	_, needsNew = m.selectBridgeAndIncrement()
	if !needsNew {
		t.Fatal("third select: expected needsNewBridge=true")
	}

	// Reserve and add a secondary bridge.
	nextName := m.reserveNextBridge()
	if nextName != "br-exe-1" {
		t.Fatalf("expected next bridge br-exe-1, got %s", nextName)
	}
	selected := m.addBridgeAndSelect(nextName)
	if selected != "br-exe-1" {
		t.Fatalf("expected selected br-exe-1, got %s", selected)
	}

	// Next selection should go to the secondary bridge.
	name3, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name3 != "br-exe-1" {
		t.Fatalf("fourth select: got (%s, %v), want (br-exe-1, false)", name3, needsNew)
	}

	// Decrement a port on primary, next selection should use primary again.
	m.decrementBridgePort(SharedBridge)
	name4, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name4 != SharedBridge {
		t.Fatalf("fifth select: got (%s, %v), want (%s, false)", name4, needsNew, SharedBridge)
	}
}

// TestVMBridgeTracking verifies the VM-to-bridge mapping.
func TestVMBridgeTracking(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}

	m.setVMBridge("vm-1", SharedBridge)
	m.setVMBridge("vm-2", "br-exe-1")

	if got := m.getVMBridge("vm-1"); got != SharedBridge {
		t.Errorf("vm-1 bridge: got %s, want %s", got, SharedBridge)
	}
	if got := m.getVMBridge("vm-2"); got != "br-exe-1" {
		t.Errorf("vm-2 bridge: got %s, want br-exe-1", got)
	}

	m.removeVMBridge("vm-1")
	if got := m.getVMBridge("vm-1"); got != "" {
		t.Errorf("vm-1 bridge after remove: got %s, want empty", got)
	}
}

// TestApplyConnectionLimit verifies that ApplyConnectionLimit re-applies the
// connlimit rule inside a VM's namespace, and is idempotent when the rule
// already exists.
func TestApplyConnectionLimit(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id := "vm000020-connlimit"
	_, err = m.CreateInterface(ctx, id)
	if err != nil {
		t.Fatal("CreateInterface:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id, "") })

	ns := nsName(id)

	// Helper: check if the connlimit rule exists in the namespace.
	hasConnlimitRule := func() bool {
		return exec.CommandContext(ctx, "ip", "netns", "exec", ns,
			"iptables", "-C", "FORWARD",
			"-s", VMIP, "-m", "connlimit",
			"--connlimit-above", fmt.Sprintf("%d", m.connLimit),
			"--connlimit-mask", "32", "-j", "DROP",
		).Run() == nil
	}

	// Verify the rule exists after creation.
	if !hasConnlimitRule() {
		t.Fatal("connlimit rule missing after CreateInterface")
	}

	// ApplyConnectionLimit should be idempotent — no error when rule exists.
	if err := m.ApplyConnectionLimit(ctx, &api.Instance{ID: id}); err != nil {
		t.Fatal("ApplyConnectionLimit (idempotent):", err)
	}
	if !hasConnlimitRule() {
		t.Fatal("connlimit rule missing after idempotent apply")
	}

	// Delete the rule manually to simulate a namespace that lost it.
	delArgs := []string{
		"netns", "exec", ns, "iptables", "-D", "FORWARD",
		"-s", VMIP, "-m", "connlimit",
		"--connlimit-above", fmt.Sprintf("%d", m.connLimit),
		"--connlimit-mask", "32", "-j", "DROP",
	}
	if out, err := exec.CommandContext(ctx, "ip", delArgs...).CombinedOutput(); err != nil {
		t.Fatalf("failed to delete connlimit rule: %v (%s)", err, out)
	}
	if hasConnlimitRule() {
		t.Fatal("connlimit rule still present after manual delete")
	}

	// ApplyConnectionLimit should re-apply the missing rule.
	if err := m.ApplyConnectionLimit(ctx, &api.Instance{ID: id}); err != nil {
		t.Fatal("ApplyConnectionLimit (re-apply):", err)
	}
	if !hasConnlimitRule() {
		t.Fatal("connlimit rule not restored by ApplyConnectionLimit")
	}
}
