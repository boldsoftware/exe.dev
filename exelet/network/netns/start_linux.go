//go:build linux

package netns

import (
	"context"
	"fmt"
	"os"

	"github.com/vishvananda/netlink"
)

// Start creates and configures the shared outbound bridge (br-exe-0).
func (m *Manager) Start(ctx context.Context) error {
	primaryBridge := m.primaryBridgeName()
	m.log.InfoContext(ctx, "starting netns network manager", "shared_bridge", primaryBridge)

	br, err := ensureBridge(primaryBridge)
	if err != nil {
		return fmt.Errorf("shared bridge: %w", err)
	}

	if err := setBridgeHashMax(primaryBridge, DefaultBridgeHashMax); err != nil {
		m.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", primaryBridge, "error", err)
	}

	addr, err := netlink.ParseAddr(SharedBridgeCIDR)
	if err != nil {
		return fmt.Errorf("parse shared bridge addr: %w", err)
	}
	if err := netlink.AddrReplace(br, addr); err != nil {
		return fmt.Errorf("assign shared bridge addr: %w", err)
	}

	if err := writeSysctl("net.ipv4.ip_forward", "1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}

	if err := ensureIptablesRule(ctx,
		[]string{"-t", "nat", "-C", "POSTROUTING", "-s", SharedBridgeNetwork, "-j", "MASQUERADE"},
		[]string{"-t", "nat", "-A", "POSTROUTING", "-s", SharedBridgeNetwork, "-j", "MASQUERADE"},
	); err != nil {
		return fmt.Errorf("shared bridge masquerade: %w", err)
	}

	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-i", primaryBridge, "!", "-o", primaryBridge, "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-i", primaryBridge, "!", "-o", primaryBridge, "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("shared bridge forward out: %w", err)
	}
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-o", primaryBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-o", primaryBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("shared bridge forward in: %w", err)
	}

	if err := m.recoverSecondaryBridges(ctx); err != nil {
		return fmt.Errorf("recover secondary bridges: %w", err)
	}

	return nil
}

// Stop is a no-op.
func (m *Manager) Stop(_ context.Context) error {
	return nil
}

// recoverSecondaryBridges discovers any existing secondary bridges (br-exe-1, br-exe-2, ...)
// and adds them to the bridge list. Called on startup to handle exelet restart.
func (m *Manager) recoverSecondaryBridges(ctx context.Context) error {
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s-%d", SharedBridgeBaseName, i)
		_, err := netlink.LinkByName(name)
		if err != nil {
			break
		}

		links, err := netlink.LinkList()
		portCount := 0
		if err == nil {
			br, _ := netlink.LinkByName(name)
			if br != nil {
				brIdx := br.Attrs().Index
				for _, l := range links {
					if l.Attrs().MasterIndex == brIdx {
						portCount++
					}
				}
			}
		}

		// Subtract 1 for the veth connecting to primary bridge.
		if portCount > 0 {
			portCount--
		}

		m.mu.Lock()
		m.bridges = append(m.bridges, bridgeInfo{name: name, portCount: portCount})
		m.mu.Unlock()

		m.log.InfoContext(ctx, "recovered secondary shared bridge",
			"name", name, "port_count", portCount)
	}

	// Count ports on the primary bridge.
	links, err := netlink.LinkList()
	if err == nil {
		primary := m.primaryBridgeName()
		br, _ := netlink.LinkByName(primary)
		if br != nil {
			brIdx := br.Attrs().Index
			portCount := 0
			for _, l := range links {
				if l.Attrs().MasterIndex == brIdx {
					portCount++
				}
			}
			// Subtract veths connecting to secondary bridges — one per
			// secondary bridge — so we only count actual VM ports.
			m.mu.Lock()
			numSecondary := len(m.bridges) - 1 // bridges[0] is primary
			if numSecondary < 0 {
				numSecondary = 0
			}
			portCount -= numSecondary
			if portCount < 0 {
				portCount = 0
			}
			if len(m.bridges) > 0 {
				m.bridges[0].portCount = portCount
			}
			m.mu.Unlock()
			m.log.InfoContext(ctx, "recovered primary shared bridge port count",
				"name", primary, "port_count", portCount)
		}
	}

	return nil
}

// createSecondaryBridge creates a new shared bridge and connects it to the primary via veth.
func (m *Manager) createSecondaryBridge(ctx context.Context, bridgeName string) error {
	primaryBridge := m.primaryBridgeName()

	m.log.InfoContext(ctx, "creating secondary shared bridge", "name", bridgeName, "primary", primaryBridge)

	var br netlink.Link
	if existing, err := netlink.LinkByName(bridgeName); err == nil {
		br = existing
	} else {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("check bridge %s: %w", bridgeName, err)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = bridgeName
		newBr := &netlink.Bridge{LinkAttrs: attrs}
		if err := netlink.LinkAdd(newBr); err != nil {
			return fmt.Errorf("create bridge %s: %w", bridgeName, err)
		}
		br = newBr
	}

	if err := setBridgeHashMax(bridgeName, DefaultBridgeHashMax); err != nil {
		m.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", bridgeName, "error", err)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("bring up bridge %s: %w", bridgeName, err)
	}

	suffix := bridgeName[len(SharedBridgeBaseName)+1:]
	vethPrimary := fmt.Sprintf("veth-%s-p", suffix)
	vethSecondary := fmt.Sprintf("veth-%s-s", suffix)

	if _, err := netlink.LinkByName(vethPrimary); err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("check veth %s: %w", vethPrimary, err)
		}
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: vethPrimary},
			PeerName:  vethSecondary,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("create veth pair: %w", err)
		}
	}

	primaryBr, err := netlink.LinkByName(primaryBridge)
	if err != nil {
		return fmt.Errorf("get primary bridge %s: %w", primaryBridge, err)
	}
	vpLink, err := netlink.LinkByName(vethPrimary)
	if err != nil {
		return fmt.Errorf("get veth %s: %w", vethPrimary, err)
	}
	if err := netlink.LinkSetMaster(vpLink, primaryBr); err != nil {
		return fmt.Errorf("attach %s to %s: %w", vethPrimary, primaryBridge, err)
	}
	if err := netlink.LinkSetUp(vpLink); err != nil {
		return fmt.Errorf("bring up %s: %w", vethPrimary, err)
	}

	vsLink, err := netlink.LinkByName(vethSecondary)
	if err != nil {
		return fmt.Errorf("get veth %s: %w", vethSecondary, err)
	}
	if err := netlink.LinkSetMaster(vsLink, br); err != nil {
		return fmt.Errorf("attach %s to %s: %w", vethSecondary, bridgeName, err)
	}
	if err := netlink.LinkSetUp(vsLink); err != nil {
		return fmt.Errorf("bring up %s: %w", vethSecondary, err)
	}

	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("secondary bridge forward out: %w", err)
	}
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("secondary bridge forward in: %w", err)
	}

	m.log.InfoContext(ctx, "created secondary shared bridge",
		"name", bridgeName, "veth_primary", vethPrimary, "veth_secondary", vethSecondary)

	return nil
}

// setBridgeHashMax sets the FDB hash_max for a bridge.
func setBridgeHashMax(bridgeName string, hashMax int) error {
	path := fmt.Sprintf("/sys/class/net/%s/bridge/hash_max", bridgeName)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", hashMax)
	return err
}
