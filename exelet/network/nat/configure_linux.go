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

func (n *NAT) configureBridge(ctx context.Context) error {
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
