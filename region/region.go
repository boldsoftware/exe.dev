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
	// Note: Active is used for display/documentation purposes and does NOT
	// affect exelet selection. See RequiresUserMatch for selection filtering.
	Active bool

	// VMHardLimit is the maximum number of VMs per exelet in this region.
	// At or above this count, new VMs are rejected and auto-throttle is triggered.
	VMHardLimit int32

	// VMSoftLimit is the soft cap for VMs per exelet in this region.
	// At or above this count, the exelet is deprioritized in selection
	// and capacity warnings are sent.
	VMSoftLimit int32

	// RequiresUserMatch indicates that exelets in this region are only available to users whose configured region matches.
	// This allows bringing new, distant regions online without all new VMs flooding there due to low load.
	// This will eventually be set to true for all regions.
	// For now, during the transition to full region support, our primary regions (pdx, lax) allow any user.
	RequiresUserMatch bool
}

var allRegions = []Region{
	{Code: "pdx", Display: "Oregon, USA", Active: true, VMHardLimit: 400, VMSoftLimit: 350, RequiresUserMatch: false},
	{Code: "lax", Display: "Los Angeles, USA", Active: false, VMHardLimit: 800, VMSoftLimit: 700, RequiresUserMatch: false},
	{Code: "nyc", Display: "New York, USA", Active: false, VMHardLimit: 800, VMSoftLimit: 700, RequiresUserMatch: true},
	{Code: "fra", Display: "Frankfurt, Germany", Active: false, VMHardLimit: 800, VMSoftLimit: 700, RequiresUserMatch: true},
	{Code: "tyo", Display: "Tokyo, Japan", Active: false, VMHardLimit: 800, VMSoftLimit: 700, RequiresUserMatch: true},
	{Code: "syd", Display: "Sydney, Australia", Active: false, VMHardLimit: 800, VMSoftLimit: 700, RequiresUserMatch: true},
	{Code: "dev", Display: "$HOME", Active: false, VMHardLimit: 400, VMSoftLimit: 350, RequiresUserMatch: false},
	{Code: "ci", Display: "CI", Active: false, VMHardLimit: 400, VMSoftLimit: 350, RequiresUserMatch: false},
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
	return Region{Code: "", Display: "", Active: false, VMHardLimit: 0, VMSoftLimit: 0, RequiresUserMatch: false}, fmt.Errorf("unknown region code %q", code)
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
	for seg := range strings.SplitSeq(host, "-") {
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

	return Region{Code: "", Display: "", Active: false, VMHardLimit: 0, VMSoftLimit: 0, RequiresUserMatch: false}, fmt.Errorf("cannot parse region from exelet host %q", host)
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
