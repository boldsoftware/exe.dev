//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/vishvananda/netlink"
)

const (
	hostnamePath   = "/etc/hostname"
	resolvConfPath = "/etc/resolv.conf"
)

func configureNetworking() error {
	conf, err := getBootArg("network")
	if err != nil {
		return err
	}

	netConfig, err := parseNetConf(conf)
	if err != nil {
		return err
	}

	if netConfig == nil {
		slog.Info("ip boot arg not specified; skipping networking config")
		return nil
	}

	// configure interface
	if netConfig.Device == "" {
		slog.Info("device not specified in netconf; skipping networking config")
		return nil
	}

	slog.Debug("configuring interface", "device", netConfig.Device)
	link, err := netlink.LinkByName(netConfig.Device)
	if err != nil {
		return err
	}

	slog.Debug("configuring network", "ip", netConfig.IP)
	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}

	netmask := net.ParseIP(netConfig.Netmask)
	sz, _ := net.IPMask(netmask.To4()).Size()

	ifaceIP := fmt.Sprintf("%s/%d", netConfig.IP, sz)

	// only configure if addr not on the interface (otherwise netlink fails)
	ipConfigured := false
	for _, addr := range ifaceAddrs {
		slog.Debug("checking interface", "addr", addr)
		if addr.String() == ifaceIP {
			ipConfigured = true
			break
		}
	}
	if !ipConfigured {
		addr, err := netlink.ParseAddr(ifaceIP)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return err
		}
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}

	// configure gateway
	slog.Debug("configuring default route", "gateway", netConfig.Gateway)
	gwIP := net.ParseIP(netConfig.Gateway)
	defaultRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: gwIP, Mask: net.CIDRMask(32, 32)},
		Scope:     netlink.SCOPE_UNIVERSE,
	}

	if err := netlink.RouteAdd(defaultRoute); err != nil {
		return err
	}

	// Add route to metadata service (169.254.169.254)
	metadataIP := net.ParseIP("169.254.169.254")
	metadataRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: metadataIP, Mask: net.CIDRMask(32, 32)},
		Gw:        gwIP,
		Scope:     netlink.SCOPE_UNIVERSE,
	}
	slog.Debug("configuring metadata service route", "metadata_ip", "169.254.169.254", "gateway", netConfig.Gateway)
	if err := netlink.RouteAdd(metadataRoute); err != nil {
		return err
	}

	// configure /etc/resolv.conf
	if v := netConfig.Nameserver; v != "" {
		slog.Debug("configuring nameserver", "ns", v)
		if err := os.WriteFile(resolvConfPath, fmt.Appendf([]byte{}, "nameserver %s\n", v), 0o644); err != nil {
			return err
		}
	}
	if v := netConfig.BackupNameserver; v != "" {
		slog.Debug("configuring backup nameserver", "ns", v)
		f, err := os.OpenFile(resolvConfPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err := f.Write([]byte(v + "\n")); err != nil {
			return err
		}
	}

	// TODO: configure NTP

	// configure hostname
	slog.Debug("configuring hostname", "hostname", netConfig.Hostname)
	if err := os.WriteFile(hostnamePath, []byte(netConfig.Hostname+"\n"), 0o644); err != nil {
		return err
	}

	return nil
}
