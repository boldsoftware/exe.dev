// Package region defines regions; a region is a datacenter where exe.dev hosts VMs.
package region

import (
	"fmt"
	"slices"
	"strings"

	"exe.dev/domz"
)

// Region represents a datacenter where exe.dev hosts VMs.
//
//exe:completeinit
type Region struct {
	// Code is the short internal identifier (e.g., "pdx", "lax", "dev").
	// Used for database storage, logging, and command line flags.
	Code string

	// Display is the user-friendly geographic description (e.g., "Oregon, USA").
	Display string

	// Active indicates whether this region is currently accepting new VMs.
	Active bool
}

var allRegions = []Region{
	{Code: "pdx", Display: "Oregon, USA", Active: true},
	{Code: "lax", Display: "Los Angeles, USA", Active: false},
	{Code: "nyc", Display: "New York, USA", Active: false},
	{Code: "fra", Display: "Frankfurt, Germany", Active: false},
	{Code: "tyo", Display: "Tokyo, Japan", Active: false},
	{Code: "syd", Display: "Sydney, Australia", Active: false},
	{Code: "dev", Display: "$HOME", Active: false},
	{Code: "ci", Display: "CI", Active: false},
}

// All returns all known regions.
func All() []Region {
	return slices.Clone(allRegions)
}

// ByCode looks up a region by its short code.
// Returns an error if the code is not recognized.
func ByCode(code string) (Region, error) {
	code = strings.ToLower(code)
	for _, r := range allRegions {
		if r.Code == code {
			return r, nil
		}
	}
	return Region{Code: "", Display: "", Active: false}, fmt.Errorf("unknown region code %q", code)
}

// Default returns the default region for new users and VMs.
func Default() Region {
	return allRegions[0]
}

// ParseExeletRegion determines the region from an exelet host name.
// Naming conventions:
//   - "exe-ctr-*" -> pdx (legacy AWS EC2 hosts)
//   - "lima-*", "localhost", "127.0.0.1" -> dev (local development)
//   - "*-REGION-*" or "*-REGION<digits>-*" -> parsed region (e.g., "exelet-lax2-staging-01" -> lax)
func ParseExeletRegion(host string) (Region, error) {
	// Remove scheme if present (e.g., "tcp://")
	if idx := strings.Index(host, "://"); idx != -1 {
		host = host[idx+len("://"):]
	}

	host = domz.StripPort(host)

	// Legacy: exe-ctr-* hosts are in pdx
	if strings.HasPrefix(host, "exe-ctr-") {
		return ByCode("pdx")
	}

	// Local development: lima hosts, localhost, 127.0.0.1 are in dev
	if strings.HasPrefix(host, "lima-") || host == "localhost" || host == "127.0.0.1" {
		return ByCode("dev")
	}

	// CI: ubuntu@192.168.122.* (libvirt default network on CI runner)
	if strings.HasPrefix(host, "ubuntu@192.168.122.") {
		return ByCode("ci")
	}

	// Look for -REGION- or -REGION<digits>- pattern in hostname.
	// Split into segments and match each against known region codes.
	// A segment matches if it equals the region code exactly or if
	// it starts with the region code and the rest is all digits
	// (e.g., "lax2", "pdx1").
	segments := strings.Split(host, "-")
	for _, seg := range segments {
		for _, r := range allRegions {
			if seg == r.Code {
				return r, nil
			}
			if strings.HasPrefix(seg, r.Code) {
				suffix := seg[len(r.Code):]
				if len(suffix) > 0 && isAllDigits(suffix) {
					return r, nil
				}
			}
		}
	}

	return Region{Code: "", Display: "", Active: false}, fmt.Errorf("cannot parse region from exelet host %q", host)
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
