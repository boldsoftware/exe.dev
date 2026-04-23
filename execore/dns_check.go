package execore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"exe.dev/dnsresolver"
	"exe.dev/domz"
	"golang.org/x/net/publicsuffix"
)

type dnsCheckResult struct {
	Domain string `json:"domain"`

	// CNAME lookup on the domain itself
	CNAME      string `json:"cname,omitempty"`
	CNAMEError string `json:"cnameError,omitempty"`

	// A record lookup
	ARecords []string `json:"aRecords,omitempty"`
	AError   string   `json:"aError,omitempty"`

	// Whether A records point to an exe.dev IP
	PointsToExe bool `json:"pointsToExe"`

	// Whether the CNAME points to *.exe.xyz
	CNAMEPointsToExe bool `json:"cnamePointsToExe"`

	// Whether a wildcard CNAME was detected
	WildcardCNAME bool `json:"wildcardCname,omitempty"`

	// Whether the domain is an apex that has a CNAME record.
	// This violates RFC 1912 § 2.4 (CNAMEs cannot coexist with the
	// SOA/NS/MX records every apex has) and tends to break email
	// and nameserver delegation.
	ApexCNAME bool `json:"apexCname,omitempty"`

	// The resolved box name, if any
	BoxName string `json:"boxName,omitempty"`

	// The resolved IP of the box (if boxName was found)
	BoxIP string `json:"boxIP,omitempty"`

	// Whether this looks like an apex domain (no CNAME, has A records)
	IsApex bool `json:"isApex"`

	// www subdomain check (relevant for apex domains)
	WWWCNAME       string `json:"wwwCname,omitempty"`
	WWWCNAMEError  string `json:"wwwCnameError,omitempty"`
	WWWMissing     bool   `json:"wwwMissing"`
	WWWPointsToExe bool   `json:"wwwPointsToExe"`

	// The box host suffix (e.g. "exe.xyz")
	BoxHost string `json:"boxHost"`

	// Overall status: "ok", "partial", "error"
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s *Server) handleDNSCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		http.Error(w, `{"error":"domain parameter is required"}`, http.StatusBadRequest)
		return
	}

	// Normalize
	domain = domz.Canonicalize(domain)
	if domain == "" || !strings.Contains(domain, ".") {
		http.Error(w, `{"error":"invalid domain"}`, http.StatusBadRequest)
		return
	}

	// Reject IP addresses
	if domz.IsIPAddr(domain) {
		http.Error(w, `{"error":"enter a domain name, not an IP address"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result := s.checkDNS(ctx, domain)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) checkDNS(ctx context.Context, domain string) dnsCheckResult {
	result := dnsCheckResult{
		Domain: domain,
	}

	// Step 1: CNAME lookup
	cname, cnameErr := dnsresolver.LookupCNAME(ctx, domain)
	if cnameErr != nil {
		if dnsErr, ok := cnameErr.(*net.DNSError); ok && dnsErr.IsNotFound {
			result.CNAMEError = "no CNAME record found"
		} else {
			result.CNAMEError = cnameErr.Error()
		}
	} else {
		cname = domz.Canonicalize(cname)
		if cname != domain {
			result.CNAME = cname
			if boxName := s.extractBoxName(cname); boxName != "" {
				result.CNAMEPointsToExe = true
				result.BoxName = boxName
			}
		}
	}

	// Step 1b: Wildcard CNAME detection
	if result.CNAME != "" {
		result.WildcardCNAME = detectWildcardCNAME(ctx, domain, result.CNAME)
	}

	// Step 1c: Apex-with-CNAME detection. A CNAME on an apex domain
	// violates RFC 1912 § 2.4 and breaks MX/NS records.
	if result.CNAME != "" && isApexDomain(domain) {
		result.ApexCNAME = true
	}

	// Step 2: A record lookup
	addrs, aErr := dnsresolver.Resolver().LookupNetIP(ctx, "ip4", domain)
	if aErr != nil {
		if dnsErr, ok := aErr.(*net.DNSError); ok && dnsErr.IsNotFound {
			result.AError = "no A records found"
		} else {
			result.AError = aErr.Error()
		}
	} else {
		for _, addr := range addrs {
			addr = addr.Unmap()
			result.ARecords = append(result.ARecords, addr.String())
			if s.isExeIP(addr) {
				result.PointsToExe = true
			}
		}
	}

	// Determine if apex
	result.IsApex = (result.CNAME == "" && len(result.ARecords) > 0)

	// Step 3: For apex domains or domains without a CNAME pointing to exe,
	// check www subdomain
	if !result.CNAMEPointsToExe {
		wwwHost := "www." + domain
		wwwCname, wwwErr := dnsresolver.LookupCNAME(ctx, wwwHost)
		if wwwErr != nil {
			if dnsErr, ok := wwwErr.(*net.DNSError); ok && dnsErr.IsNotFound {
				result.WWWCNAMEError = "no CNAME record found"
				result.WWWMissing = true
			} else {
				result.WWWCNAMEError = wwwErr.Error()
				result.WWWMissing = true
			}
		} else {
			wwwCname = domz.Canonicalize(wwwCname)
			if wwwCname != wwwHost {
				result.WWWCNAME = wwwCname
				if boxName := s.extractBoxName(wwwCname); boxName != "" {
					result.BoxName = boxName
					result.WWWPointsToExe = true
				}
			} else {
				result.WWWMissing = true
				result.WWWCNAMEError = "www points to itself, not to an exe.xyz VM"
			}
		}
	}

	result.BoxHost = s.env.BoxHost

	// Resolve the box IP if we found a box name
	if result.BoxName != "" {
		boxFQDN := result.BoxName + "." + s.env.BoxHost
		if ips, err := dnsresolver.Resolver().LookupNetIP(ctx, "ip4", boxFQDN); err == nil && len(ips) > 0 {
			result.BoxIP = ips[0].Unmap().String()
		}
	}

	// Determine overall status and message
	result.Status, result.Message = classifyDNSResult(&result, s.env.BoxHost)

	return result
}

// extractBoxName checks if a hostname is a VM name under any known exe.dev box host.
func (s *Server) extractBoxName(hostname string) string {
	for _, suffix := range []string{s.env.BoxHost, "exe.xyz", "exe-staging.xyz"} {
		if boxName, ok := domz.CutBase(hostname, suffix); ok && boxName != "" && !strings.Contains(boxName, ".") {
			return boxName
		}
	}
	return ""
}

// isApexDomain reports whether domain is the registrable apex under
// the public suffix list (e.g. "example.com" or "example.co.uk"),
// where placing a CNAME is illegal per RFC 1912 § 2.4.
func isApexDomain(domain string) bool {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return false
	}
	return etld1 == domain
}

func (s *Server) isExeIP(addr netip.Addr) bool {
	if s.LobbyIP.IsValid() && addr == s.LobbyIP {
		return true
	}
	for _, info := range s.PublicIPs {
		if addr == info.IP {
			return true
		}
	}
	return false
}

// detectWildcardCNAME checks whether the CNAME for domain is the result of a
// wildcard record by probing a sibling and a child name that should not exist.
// If either resolves to the same CNAME target, a wildcard is likely.
func detectWildcardCNAME(ctx context.Context, domain, cname string) bool {
	// Build probe names.
	// For "blog.example.com" → sibling "exe-cname-sibling.example.com"
	//                         → child  "exe-cname-child.blog.example.com"
	parts := strings.SplitN(domain, ".", 2)
	var probes []string
	if len(parts) == 2 {
		probes = append(probes, "exe-cname-sibling."+parts[1])
	}
	probes = append(probes, "exe-cname-child."+domain)

	for _, probe := range probes {
		probeCNAME, err := dnsresolver.LookupCNAME(ctx, probe)
		if err != nil {
			continue
		}
		probeCNAME = domz.Canonicalize(probeCNAME)
		if probeCNAME == cname {
			return true
		}
	}
	return false
}

func classifyDNSResult(r *dnsCheckResult, boxHost string) (status, message string) {
	// Case 0: CNAME on an apex domain is an RFC violation.
	if r.ApexCNAME {
		return "error", "CNAME records are not allowed on apex domains (RFC 1912 § 2.4). A CNAME on " + r.Domain + " will break MX, NS, and other records. Replace it with an A, ALIAS, ANAME, or flattened-CNAME record pointing to your VM."
	}

	// Case 1: CNAME directly points to exe.xyz
	if r.CNAMEPointsToExe {
		return "ok", "CNAME points to " + r.CNAME + ". Your domain is correctly configured."
	}

	// Case 2: CNAME exists but doesn't point to exe
	if r.CNAME != "" && !r.CNAMEPointsToExe {
		return "error", "CNAME points to " + r.CNAME + ", not to a " + boxHost + " address. Update your CNAME to point to your-vm." + boxHost + "."
	}

	// Case 3: Apex domain with A records pointing to exe
	if r.IsApex && r.PointsToExe {
		if r.WWWMissing {
			return "partial", "A record points to exe.dev, but www." + r.Domain + " is missing a CNAME to your-vm." + boxHost + ". Add a CNAME record for www."
		}
		if r.BoxName != "" {
			return "ok", "Apex domain configured correctly. A record points to exe.dev and www." + r.Domain + " points to " + r.WWWCNAME + "."
		}
		return "partial", "A record points to exe.dev, but www." + r.Domain + " does not point to a " + boxHost + " VM. Update the www CNAME."
	}

	// Case 4: Apex domain with A records NOT pointing to exe
	if r.IsApex && !r.PointsToExe {
		ips := strings.Join(r.ARecords, ", ")
		return "error", "A record resolves to " + ips + ", which is not an exe.dev IP. Update your A/ALIAS record to point to your VM's IP."
	}

	// Case 5: No records at all
	if r.CNAME == "" && len(r.ARecords) == 0 {
		return "error", "No DNS records found for " + r.Domain + ". Add a CNAME record pointing to your-vm." + boxHost + "."
	}

	// Case 6: Has A records but not apex-like (has CNAME error but A records resolve)
	if len(r.ARecords) > 0 && r.PointsToExe {
		if r.WWWMissing {
			return "partial", "Domain resolves to an exe.dev IP, but www." + r.Domain + " is missing. Add a CNAME record for www pointing to your-vm." + boxHost + "."
		}
		if r.BoxName != "" {
			return "ok", "Domain is correctly configured."
		}
	}

	return "error", "DNS is not configured for exe.dev. Add a CNAME record pointing to your-vm." + boxHost + "."
}
