//go:build linux

package nat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
)

// MIGRATION: Remove after this commit has been deployed to prod.
// This migrates the legacy "br-exe" bridge to the new "br-exe-0" naming scheme.
func (n *NAT) migrateLegacyBridge(ctx context.Context) error {
	legacyName := n.bridgeBaseName
	newName := n.primaryBridgeName()

	// Check if legacy bridge exists
	legacyBridge, err := netlink.LinkByName(legacyName)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			return nil // No legacy bridge, nothing to migrate
		}
		return err
	}

	// Check for conflict - new bridge should not exist
	_, err = netlink.LinkByName(newName)
	if err == nil {
		return fmt.Errorf("both legacy bridge %s and new bridge %s exist; manual intervention required", legacyName, newName)
	}
	if _, ok := err.(netlink.LinkNotFoundError); !ok {
		return err
	}

	n.log.InfoContext(ctx, "migrating legacy bridge", "from", legacyName, "to", newName)

	// Clean up old iptables rules referencing the legacy bridge name
	n.cleanupLegacyIPTablesRules(ctx, legacyName)

	// Rename bridge
	if err := netlink.LinkSetName(legacyBridge, newName); err != nil {
		return fmt.Errorf("failed to rename bridge %s to %s: %w", legacyName, newName, err)
	}

	return nil
}

// MIGRATION: Remove after this commit has been deployed to prod.
func (n *NAT) cleanupLegacyIPTablesRules(ctx context.Context, legacyBridge string) {
	// Delete FORWARD rules (ignore errors - rules may not exist)
	forwardArgs1 := []string{
		"-D", "FORWARD",
		"-i", legacyBridge,
		"!", "-o", legacyBridge,
		"-j", "ACCEPT",
	}
	if err := exec.CommandContext(ctx, "iptables", forwardArgs1...).Run(); err != nil {
		n.log.DebugContext(ctx, "failed to delete legacy forward rule (may not exist)", "bridge", legacyBridge, "error", err)
	}

	forwardArgs2 := []string{
		"-D", "FORWARD",
		"-o", legacyBridge,
		"-m", "conntrack",
		"--ctstate", "RELATED,ESTABLISHED",
		"-j", "ACCEPT",
	}
	if err := exec.CommandContext(ctx, "iptables", forwardArgs2...).Run(); err != nil {
		n.log.DebugContext(ctx, "failed to delete legacy forward conntrack rule (may not exist)", "bridge", legacyBridge, "error", err)
	}

	// Delete PREROUTING DNAT rule for metadata service
	dnatArgs := []string{
		"-t", "nat",
		"-D", "PREROUTING",
		"-i", legacyBridge,
		"-d", MetadataIP,
		"-p", "tcp",
		"--dport", "80",
		"-j", "DNAT",
	}
	if err := exec.CommandContext(ctx, "iptables", dnatArgs...).Run(); err != nil {
		n.log.DebugContext(ctx, "failed to delete legacy DNAT rule (may not exist)", "bridge", legacyBridge, "error", err)
	}
}

func (n *NAT) configureBridge(ctx context.Context) error {
	// MIGRATION: Remove this call when migrateLegacyBridge is removed.
	if err := n.migrateLegacyBridge(ctx); err != nil {
		return fmt.Errorf("error migrating legacy bridge: %w", err)
	}

	primaryBridge := n.primaryBridgeName()

	// check for bridge and create if missing
	bridge, err := netlink.LinkByName(primaryBridge)
	if err != nil {
		// if not a LinkNotFoundError return the err
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return err
		}

		n.log.DebugContext(ctx, "creating bridge", "name", primaryBridge)

		attrs := netlink.NewLinkAttrs()
		attrs.Name = primaryBridge
		br := &netlink.Bridge{LinkAttrs: attrs}
		if err := netlink.LinkAdd(br); err != nil {
			return err
		}
		// Increase FDB hash_max to prevent "exchange full" errors at scale
		if err := setBridgeHashMax(primaryBridge, DefaultBridgeHashMax); err != nil {
			n.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", primaryBridge, "error", err)
		}
		bridge = br
	}

	// assign dhcp server IP
	serverIP, err := n.dhcpServer.ServerIP()
	if err != nil {
		return err
	}

	_, ipnet, err := net.ParseCIDR(n.network)
	if err != nil {
		return err
	}

	size, _ := ipnet.Mask.Size()
	bridgeAddr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", serverIP, size))
	if err != nil {
		return err
	}
	if err := netlink.AddrReplace(bridge, bridgeAddr); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(bridge); err != nil {
		return err
	}

	// Apply fq_codel to bridge for fair queuing between VMs
	if err := n.applyBridgeFqCodel(ctx, primaryBridge); err != nil {
		n.log.WarnContext(ctx, "failed to apply fq_codel to bridge", "bridge", primaryBridge, "error", err)
	}

	// Add DNAT rule to redirect metadata service traffic (169.254.169.254:80)
	// to the bridge IP. This allows multiple exelets to run in parallel, each
	// with their own bridge IP, while VMs see the standard metadata IP.
	if err := n.applyMetadataDNAT(ctx, primaryBridge, serverIP.String()); err != nil {
		return err
	}

	// configure forwarding
	if err := n.applyIPTablesForwarding(ctx, primaryBridge); err != nil {
		return err
	}

	// configure NAT masquerade
	if err := n.applyIPTablesMasquerade(ctx, primaryBridge, n.network); err != nil {
		return err
	}

	// block guest access to carrier-grade NAT range
	if err := n.applyCarrierNATBlock(ctx, primaryBridge); err != nil {
		return err
	}

	// block guest access to gateway (bridge IP) except for metadata service
	if err := n.applyGatewayBlock(ctx, primaryBridge, serverIP.String()); err != nil {
		return err
	}

	return nil
}

// createSecondaryBridge creates a new bridge and connects it to the primary bridge via veth pair.
// If the bridge already exists, it ensures it's properly configured.
func (n *NAT) createSecondaryBridge(ctx context.Context, bridgeName string) error {
	primaryBridge := n.primaryBridgeName()

	n.log.DebugContext(ctx, "creating secondary bridge", "name", bridgeName, "primary", primaryBridge)

	// Check if bridge already exists
	var br netlink.Link
	existingBr, err := netlink.LinkByName(bridgeName)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("failed to check bridge %s: %w", bridgeName, err)
		}
		// Bridge doesn't exist, create it
		attrs := netlink.NewLinkAttrs()
		attrs.Name = bridgeName
		newBr := &netlink.Bridge{LinkAttrs: attrs}
		if err := netlink.LinkAdd(newBr); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
		}
		// Increase FDB hash_max to prevent "exchange full" errors at scale
		if err := setBridgeHashMax(bridgeName, DefaultBridgeHashMax); err != nil {
			n.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", bridgeName, "error", err)
		}
		br = newBr
	} else {
		br = existingBr
		n.log.DebugContext(ctx, "secondary bridge already exists", "name", bridgeName)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", bridgeName, err)
	}

	// Apply fq_codel to secondary bridge for fair queuing between VMs
	if err := n.applyBridgeFqCodel(ctx, bridgeName); err != nil {
		n.log.WarnContext(ctx, "failed to apply fq_codel to bridge", "bridge", bridgeName, "error", err)
	}

	// Create veth pair to connect to primary bridge
	// veth names: veth-<bridge_suffix>-p (primary side), veth-<bridge_suffix>-s (secondary side)
	// Extract the suffix number from bridge name (e.g., "br-exe-1" -> "1")
	suffix := bridgeName[len(n.bridgeBaseName)+1:]
	vethPrimary := fmt.Sprintf("veth-%s-p", suffix)
	vethSecondary := fmt.Sprintf("veth-%s-s", suffix)

	// Check if veth pair already exists
	_, err = netlink.LinkByName(vethPrimary)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("failed to check veth %s: %w", vethPrimary, err)
		}
		// Create veth pair
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: vethPrimary},
			PeerName:  vethSecondary,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("failed to create veth pair: %w", err)
		}
	} else {
		n.log.DebugContext(ctx, "veth pair already exists", "primary", vethPrimary, "secondary", vethSecondary)
	}

	// Get the primary bridge interface
	primaryBridgeIface, err := netlink.LinkByName(primaryBridge)
	if err != nil {
		return fmt.Errorf("failed to get primary bridge %s: %w", primaryBridge, err)
	}

	// Attach vethPrimary to primary bridge
	vethPrimaryIface, err := netlink.LinkByName(vethPrimary)
	if err != nil {
		return fmt.Errorf("failed to get veth %s: %w", vethPrimary, err)
	}
	if err := netlink.LinkSetMaster(vethPrimaryIface, primaryBridgeIface); err != nil {
		return fmt.Errorf("failed to attach %s to %s: %w", vethPrimary, primaryBridge, err)
	}
	if err := netlink.LinkSetUp(vethPrimaryIface); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", vethPrimary, err)
	}

	// Attach vethSecondary to secondary bridge
	vethSecondaryIface, err := netlink.LinkByName(vethSecondary)
	if err != nil {
		return fmt.Errorf("failed to get veth %s: %w", vethSecondary, err)
	}
	if err := netlink.LinkSetMaster(vethSecondaryIface, br); err != nil {
		return fmt.Errorf("failed to attach %s to %s: %w", vethSecondary, bridgeName, err)
	}
	if err := netlink.LinkSetUp(vethSecondaryIface); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", vethSecondary, err)
	}

	// Apply iptables forwarding rules for the new bridge
	if err := n.applyIPTablesForwarding(ctx, bridgeName); err != nil {
		return fmt.Errorf("failed to apply forwarding rules for %s: %w", bridgeName, err)
	}

	// Apply metadata DNAT rule for the new bridge (redirects to primary bridge IP)
	serverIP, err := n.dhcpServer.ServerIP()
	if err != nil {
		return err
	}
	if err := n.applyMetadataDNAT(ctx, bridgeName, serverIP.String()); err != nil {
		return fmt.Errorf("failed to apply metadata DNAT for %s: %w", bridgeName, err)
	}

	// Block guest access to carrier-grade NAT range
	if err := n.applyCarrierNATBlock(ctx, bridgeName); err != nil {
		return fmt.Errorf("failed to apply carrier NAT block for %s: %w", bridgeName, err)
	}

	// Block guest access to gateway (bridge IP) except for metadata service
	if err := n.applyGatewayBlock(ctx, bridgeName, serverIP.String()); err != nil {
		return fmt.Errorf("failed to apply gateway block for %s: %w", bridgeName, err)
	}

	n.log.InfoContext(ctx, "created secondary bridge", "name", bridgeName, "veth_primary", vethPrimary, "veth_secondary", vethSecondary)

	return nil
}

func (n *NAT) createTapInterface(id, bridgeName string) (netlink.Link, error) {
	bridgeIface, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil, fmt.Errorf("unable to load bridge %s: %w", bridgeName, err)
	}

	link, err := netlink.LinkByName(id)
	if err != nil {
		// continue to create if it's a link not found
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return nil, err
		}
	}
	// if existing, return
	if link != nil {
		return link, nil
	}

	// create TAP
	attrs := netlink.NewLinkAttrs()
	attrs.Name = id
	attrs.MasterIndex = bridgeIface.Attrs().Index

	tap := &netlink.Tuntap{LinkAttrs: attrs}
	tap.Mode = netlink.TUNTAP_MODE_TAP

	if err := netlink.LinkAdd(tap); err != nil {
		return nil, err
	}

	if err := netlink.LinkSetIsolated(tap, true); err != nil {
		return nil, err
	}

	if err := netlink.LinkSetUp(tap); err != nil {
		return nil, err
	}

	return tap, nil
}

func (n *NAT) deleteTapInterface(id string) error {
	link, err := netlink.LinkByName(id)
	if err != nil {
		// continue to create if it's a link not found
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			return nil
		}
		return err
	}

	return netlink.LinkDel(link)
}

func (n *NAT) applyIPTablesForwarding(ctx context.Context, device string) error {
	args := []string{
		"-n",
		"-L",
		"FORWARD",
		"-v",
	}
	fc := exec.CommandContext(ctx, "iptables", args...)

	fOut, err := fc.CombinedOutput()
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(fOut)
	sc := bufio.NewScanner(buf)
	forwardRule := false
	for sc.Scan() {
		l := sc.Text()
		if strings.Contains(l, device) && strings.Contains(l, "ACCEPT") {
			forwardRule = true
			break
		}
	}

	if forwardRule {
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables forward rule", "device", device)
	// create
	cArgs := []string{
		"-A",
		"FORWARD",
		"-i",
		device,
		"!",
		"-o",
		device,
		"-j",
		"ACCEPT",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		return err
	}

	cArgs = []string{
		"-A",
		"FORWARD",
		"-o",
		device,
		"-m",
		"conntrack",
		"--ctstate",
		"RELATED,ESTABLISHED",
		"-j",
		"ACCEPT",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		return err
	}

	// configure kernel forwarding
	if err := writeSysctl("net.ipv4.conf.all.forwarding", "1"); err != nil {
		return fmt.Errorf("error setting ipv4 forwarding sysctl: %w", err)
	}

	if err := writeSysctl("net.ipv6.conf.all.forwarding", "1"); err != nil {
		return fmt.Errorf("error setting ipv6 forwarding sysctl: %w", err)
	}

	return nil
}

func (n *NAT) applyIPTablesMasquerade(ctx context.Context, device, network string) error {
	args := []string{
		"-n",
		"-L",
		"-t",
		"nat",
		"-v",
	}
	fc := exec.CommandContext(ctx, "iptables", args...)

	fOut, err := fc.CombinedOutput()
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(fOut)
	sc := bufio.NewScanner(buf)

	masqRule := false
	for sc.Scan() {
		l := sc.Text()
		if strings.Contains(l, network) && strings.Contains(l, "MASQUERADE") {
			masqRule = true
			break
		}
	}

	if masqRule {
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables masquerade rule", "device", device)
	// create
	cArgs := []string{
		"-t",
		"nat",
		"-A",
		"POSTROUTING",
		"-s",
		network,
		"-j",
		"MASQUERADE",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		return err
	}

	return nil
}

func (n *NAT) applyMetadataDNAT(ctx context.Context, bridgeName, bridgeIP string) error {
	// Check if DNAT rule already exists
	args := []string{
		"-t", "nat",
		"-n",
		"-L", "PREROUTING",
		"-v",
	}
	fc := exec.CommandContext(ctx, "iptables", args...)
	fOut, err := fc.CombinedOutput()
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(fOut)
	sc := bufio.NewScanner(buf)
	dnatRuleExists := false
	// Look for a rule that DNATs metadata IP to our bridge IP for this specific bridge
	for sc.Scan() {
		l := sc.Text()
		if strings.Contains(l, bridgeName) && strings.Contains(l, MetadataIP) && strings.Contains(l, "DNAT") {
			dnatRuleExists = true
			break
		}
	}

	if dnatRuleExists {
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables DNAT rule for metadata service", "bridge", bridgeName, "metadata_ip", MetadataIP, "bridge_ip", bridgeIP)

	// Add DNAT rule: packets to 169.254.169.254:80 get redirected to bridge IP:80
	// -i specifies incoming interface (our bridge), so only our VMs' traffic is affected
	cArgs := []string{
		"-t", "nat",
		"-A", "PREROUTING",
		"-i", bridgeName,
		"-d", MetadataIP,
		"-p", "tcp",
		"--dport", "80",
		"-j", "DNAT",
		"--to-destination", bridgeIP + ":80",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add metadata DNAT rule: %w", err)
	}

	return nil
}

// applyCarrierNATBlock adds an iptables rule to block guest traffic to the carrier-grade NAT range (100.64.0.0/10).
// This prevents guests from accessing host infrastructure on the CGNAT network while allowing exeletd
// (running as a host process) to connect, since host-originated traffic uses the OUTPUT chain, not FORWARD.
func (n *NAT) applyCarrierNATBlock(ctx context.Context, device string) error {
	// Check if rule already exists
	args := []string{
		"-n",
		"-L",
		"FORWARD",
		"-v",
	}
	fc := exec.CommandContext(ctx, "iptables", args...)

	fOut, err := fc.CombinedOutput()
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(fOut)
	sc := bufio.NewScanner(buf)
	ruleExists := false
	for sc.Scan() {
		l := sc.Text()
		if strings.Contains(l, device) && strings.Contains(l, "100.64.0.0/10") && strings.Contains(l, "DROP") {
			ruleExists = true
			break
		}
	}

	if ruleExists {
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables rule to block carrier NAT access from guests", "device", device, "cidr", CarrierNATCIDR)

	// Insert at beginning of FORWARD chain to ensure it's evaluated before ACCEPT rules
	cArgs := []string{
		"-I",
		"FORWARD",
		"-i",
		device,
		"-d",
		CarrierNATCIDR,
		"-j",
		"DROP",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add carrier NAT block rule: %w", err)
	}

	return nil
}

// applyGatewayBlock blocks VMs from initiating new TCP connections to the gateway (bridge) IP.
// This prevents VMs from accessing services running on the host (like SSH) via the gateway IP.
//
// For the metadata service, VMs must use 169.254.169.254, not the bridge IP directly.
// A DNAT rule rewrites 169.254.169.254:80 -> bridge_ip:80, and we use conntrack's
// --ctorigdst to allow only traffic that was originally destined for 169.254.169.254.
// Traffic sent directly to the bridge IP on port 80 is blocked.
//
// Established/related connections (like SSH proxy responses) are allowed through.
func (n *NAT) applyGatewayBlock(ctx context.Context, bridgeName, bridgeIP string) error {
	// Check if rule already exists by looking for our DROP rule
	args := []string{"-n", "-L", "INPUT", "-v"}
	fc := exec.CommandContext(ctx, "iptables", args...)

	fOut, err := fc.CombinedOutput()
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(fOut)
	sc := bufio.NewScanner(buf)
	ruleExists := false
	for sc.Scan() {
		l := sc.Text()
		// Look for our DROP rule for this bridge/IP combination
		if strings.Contains(l, bridgeName) && strings.Contains(l, bridgeIP) && strings.Contains(l, "DROP") {
			ruleExists = true
			break
		}
	}

	if ruleExists {
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables rules to block gateway access from guests", "bridge", bridgeName, "gateway_ip", bridgeIP)

	// Rule 1: Allow port 80 traffic that was originally destined for the metadata IP (169.254.169.254).
	// This traffic was DNATed to the bridge IP and should be allowed through to the metadata service.
	// We use conntrack's --ctorigdst to check the original destination before DNAT.
	allowArgs := []string{
		"-I", "INPUT",
		"-i", bridgeName,
		"-d", bridgeIP,
		"-p", "tcp",
		"--dport", "80",
		"-m", "conntrack",
		"--ctorigdst", MetadataIP,
		"-j", "ACCEPT",
	}
	if err := exec.CommandContext(ctx, "iptables", allowArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add metadata allow rule: %w", err)
	}

	// Rule 2: Block all other new TCP connections from VMs to gateway.
	// We use INPUT chain because this traffic is destined for the host itself.
	// The --syn flag matches only TCP SYN packets (new connection attempts),
	// allowing established connection responses (like SSH proxy traffic) through.
	// This rule comes after the ACCEPT rule above, so DNATed metadata traffic is allowed.
	blockArgs := []string{
		"-A", "INPUT",
		"-i", bridgeName,
		"-d", bridgeIP,
		"-p", "tcp",
		"--syn",
		"-j", "DROP",
	}
	if err := exec.CommandContext(ctx, "iptables", blockArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add gateway block rule: %w", err)
	}

	return nil
}

func writeSysctl(name, value string) error {
	ctlPath := fmt.Sprintf("/proc/sys/%s", strings.ReplaceAll(name, ".", "/"))
	f, err := os.OpenFile(ctlPath, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.WriteString(f, value); err != nil {
		return err
	}
	return nil
}

// setBridgeHashMax sets the FDB hash_max for a bridge to allow more MAC addresses.
// The default is 512 which can cause "exchange full" errors at scale.
func setBridgeHashMax(bridgeName string, hashMax int) error {
	path := fmt.Sprintf("/sys/class/net/%s/bridge/hash_max", bridgeName)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%d", hashMax); err != nil {
		return err
	}
	return nil
}

// applyConnLimit adds an iptables rule to limit concurrent connections from a VM IP.
func (n *NAT) applyConnLimit(ctx context.Context, ip string) error {
	ruleArgs := []string{
		"-s",
		ip,
		"-m",
		"connlimit",
		"--connlimit-above",
		fmt.Sprintf("%d", n.connLimit),
		"--connlimit-mask",
		"32",
		"-j",
		"DROP",
	}

	// Check if exact rule already exists using iptables -C
	checkArgs := append([]string{"-C", "FORWARD"}, ruleArgs...)
	if err := exec.CommandContext(ctx, "iptables", checkArgs...).Run(); err == nil {
		// Rule already exists
		return nil
	}

	n.log.DebugContext(ctx, "adding iptables connection limit rule", "ip", ip, "limit", n.connLimit)

	// Insert at beginning of FORWARD chain to ensure it's evaluated before ACCEPT rules
	insertArgs := append([]string{"-I", "FORWARD"}, ruleArgs...)
	if err := exec.CommandContext(ctx, "iptables", insertArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add connection limit rule: %w", err)
	}

	return nil
}

// removeConnLimit removes the connection limit iptables rule for a VM IP.
func (n *NAT) removeConnLimit(ctx context.Context, ip string) error {
	n.log.DebugContext(ctx, "removing iptables connection limit rule", "ip", ip, "limit", n.connLimit)

	cArgs := []string{
		"-D",
		"FORWARD",
		"-s",
		ip,
		"-m",
		"connlimit",
		"--connlimit-above",
		fmt.Sprintf("%d", n.connLimit),
		"--connlimit-mask",
		"32",
		"-j",
		"DROP",
	}

	if err := exec.CommandContext(ctx, "iptables", cArgs...).Run(); err != nil {
		// Don't fail if rule doesn't exist (may have been already removed)
		n.log.DebugContext(ctx, "failed to remove connection limit rule (may not exist)", "ip", ip, "error", err)
	}

	return nil
}

// applyBandwidthLimit limits upload bandwidth FROM the VM using an IFB device.
//
// From the host's perspective:
// - TAP ingress = traffic coming FROM the VM (uploads) ← LIMITED via IFB
// - TAP egress = traffic going TO the VM (downloads) ← unlimited
//
// We use an IFB (Intermediate Functional Block) device to redirect TAP ingress
// to a virtual device where we can apply HTB shaping. This queues excess traffic
// instead of dropping it, allowing TCP to adapt gracefully.
func (n *NAT) applyBandwidthLimit(ctx context.Context, tapName string) error {
	ifbName := getIfbName(tapName)
	n.log.DebugContext(ctx, "applying bandwidth limit", "tap", tapName, "ifb", ifbName, "rate", n.bandwidthRate)

	// Track what we've created for rollback on error
	var ifbCreated, ingressCreated bool

	cleanup := func() {
		if ingressCreated {
			_ = exec.CommandContext(ctx, "tc", "qdisc", "del", "dev", tapName, "ingress").Run()
		}
		if ifbCreated {
			_ = exec.CommandContext(ctx, "ip", "link", "del", ifbName).Run()
		}
	}

	// Create IFB device for this TAP
	// ip link add $IFB type ifb
	createIfbArgs := []string{"link", "add", ifbName, "type", "ifb"}
	if err := exec.CommandContext(ctx, "ip", createIfbArgs...).Run(); err != nil {
		return fmt.Errorf("failed to create ifb device: %w", err)
	}
	ifbCreated = true

	// Bring up IFB device
	// ip link set $IFB up
	upIfbArgs := []string{"link", "set", ifbName, "up"}
	if err := exec.CommandContext(ctx, "ip", upIfbArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to bring up ifb device: %w", err)
	}

	// Add ingress qdisc to TAP for redirect
	// tc qdisc add dev $TAP handle ffff: ingress
	ingressArgs := []string{
		"qdisc", "add", "dev", tapName,
		"handle", "ffff:", "ingress",
	}
	if err := exec.CommandContext(ctx, "tc", ingressArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to add ingress qdisc: %w", err)
	}
	ingressCreated = true

	// Redirect TAP ingress to IFB egress
	// tc filter add dev $TAP parent ffff: protocol all u32 match u32 0 0 action mirred egress redirect dev $IFB
	mirredArgs := []string{
		"filter", "add", "dev", tapName,
		"parent", "ffff:",
		"protocol", "all",
		"u32", "match", "u32", "0", "0",
		"action", "mirred", "egress", "redirect", "dev", ifbName,
	}
	if err := exec.CommandContext(ctx, "tc", mirredArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to add mirred redirect: %w", err)
	}

	// Apply HTB shaping on IFB egress
	// tc qdisc add dev $IFB root handle 1: htb default 10
	htbArgs := []string{
		"qdisc", "add", "dev", ifbName,
		"root", "handle", "1:", "htb", "default", "10",
	}
	if err := exec.CommandContext(ctx, "tc", htbArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to add htb qdisc to ifb: %w", err)
	}

	// Create HTB class with rate limit
	// tc class add dev $IFB parent 1: classid 1:10 htb rate 100mbit burst 256k cburst 256k
	classArgs := []string{
		"class", "add", "dev", ifbName,
		"parent", "1:", "classid", "1:10", "htb",
		"rate", n.bandwidthRate,
		"burst", n.bandwidthBurst,
		"cburst", n.bandwidthBurst,
	}
	if err := exec.CommandContext(ctx, "tc", classArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to add htb class to ifb: %w", err)
	}

	// Add fq_codel for fair queuing and low latency
	// tc qdisc add dev $IFB parent 1:10 handle 10: fq_codel
	fqArgs := []string{
		"qdisc", "add", "dev", ifbName,
		"parent", "1:10", "handle", "10:", "fq_codel",
	}
	if err := exec.CommandContext(ctx, "tc", fqArgs...).Run(); err != nil {
		cleanup()
		return fmt.Errorf("failed to add fq_codel to ifb: %w", err)
	}

	return nil
}

// removeBandwidthLimit removes the bandwidth limiting setup from a TAP device.
func (n *NAT) removeBandwidthLimit(ctx context.Context, tapName string) error {
	ifbName := getIfbName(tapName)
	n.log.DebugContext(ctx, "removing bandwidth limit", "tap", tapName, "ifb", ifbName)

	// Remove ingress qdisc from TAP (this also removes attached filters/mirred)
	// tc qdisc del dev $TAP ingress
	ingressArgs := []string{"qdisc", "del", "dev", tapName, "ingress"}
	if err := exec.CommandContext(ctx, "tc", ingressArgs...).Run(); err != nil {
		n.log.DebugContext(ctx, "failed to remove ingress qdisc (may not exist)", "tap", tapName, "error", err)
	}

	// Delete the IFB device
	// ip link del $IFB
	delIfbArgs := []string{"link", "del", ifbName}
	if err := exec.CommandContext(ctx, "ip", delIfbArgs...).Run(); err != nil {
		n.log.DebugContext(ctx, "failed to delete ifb device (may not exist)", "ifb", ifbName, "error", err)
	}

	return nil
}

// applyBridgeFqCodel applies fq_codel to a bridge for fair queuing between VMs.
func (n *NAT) applyBridgeFqCodel(ctx context.Context, bridgeName string) error {
	n.log.DebugContext(ctx, "applying fq_codel to bridge", "bridge", bridgeName)

	// tc qdisc replace dev $BRIDGE root fq_codel
	args := []string{"qdisc", "replace", "dev", bridgeName, "root", "fq_codel"}
	if err := exec.CommandContext(ctx, "tc", args...).Run(); err != nil {
		return fmt.Errorf("failed to apply fq_codel to bridge: %w", err)
	}

	return nil
}
