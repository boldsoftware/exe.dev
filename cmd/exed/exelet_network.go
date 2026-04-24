package main

import (
	"fmt"
	"net/netip"
	"net/url"
)

const (
	localExeletNetworkDataDir     = "/data/exelet/network"
	defaultLocalExeletNetworkCIDR = "100.64.0.0/24"
)

func normalizeExeletNetworkCIDR(cidr string) (string, error) {
	if cidr == "" {
		return "", nil
	}

	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid -exelet-network-cidr %q: %w", cidr, err)
	}
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("-exelet-network-cidr must be an IPv4 CIDR: %q", cidr)
	}

	return prefix.Masked().String(), nil
}

func localExeletNetworkManagerAddress(cidr string) string {
	if cidr == "" {
		cidr = defaultLocalExeletNetworkCIDR
	}
	return "nat://" + localExeletNetworkDataDir + "?network=" + url.QueryEscape(cidr) + "&disable_bandwidth=true"
}

func limaExeletNetworkManagerAddress(cidr string) string {
	if cidr == "" {
		return "nat://" + localExeletNetworkDataDir
	}
	return "nat://" + localExeletNetworkDataDir + "?network=" + url.QueryEscape(cidr)
}
