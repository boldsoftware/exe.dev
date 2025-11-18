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
	// check for bridge and create if missing
	bridge, err := netlink.LinkByName(n.bridgeName)
	if err != nil {
		// if not a LinkNotFoundError return the err
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return err
		}

		n.log.DebugContext(ctx, "creating bridge", "name", n.bridgeName)

		attrs := netlink.NewLinkAttrs()
		attrs.Name = n.bridgeName
		br := &netlink.Bridge{LinkAttrs: attrs}
		if err := netlink.LinkAdd(br); err != nil {
			return err
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

	// Add metadata service IP
	metadataAddr, err := netlink.ParseAddr(MetadataIP + "/32")
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(bridge, metadataAddr); err != nil {
		// Ignore if address already exists
		if !os.IsExist(err) {
			return err
		}
	}

	if err := netlink.LinkSetUp(bridge); err != nil {
		return err
	}

	// configure forwarding
	if err := n.applyIPTablesForwarding(ctx, n.bridgeName); err != nil {
		return err
	}

	// configure NAT masquerade
	if err := n.applyIPTablesMasquerade(ctx, n.bridgeName, n.network); err != nil {
		return err
	}

	return nil
}

func (n *NAT) createTapInterface(id string) (netlink.Link, error) {
	bridgeIface, err := netlink.LinkByName(n.bridgeName)
	if err != nil {
		return nil, fmt.Errorf("unable to load bridge %s: %w", n.bridgeName, err)
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
