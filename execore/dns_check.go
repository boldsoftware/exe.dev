package execore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/dnsresolver"
	"exe.dev/domz"
	"golang.org/x/net/publicsuffix"
)

type dnsCheckResult struct {
	Domain string `json:"domain"`

	// User-provided VM name (without box host suffix)
	VMName string `json:"vmName,omitempty"`

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

	// Set when we couldn't resolve the user-supplied VM hostname.
	// This usually means the VM name is mistyped or the VM no longer exists.
	BoxResolveError string `json:"boxResolveError,omitempty"`

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

	vm := strings.TrimSpace(r.URL.Query().Get("vm"))
	if vm == "" {
		http.Error(w, `{"error":"vm parameter is required"}`, http.StatusBadRequest)
		return
	}
	// Allow users to enter "foo.exe.xyz" or just "foo".
	vm = strings.TrimSpace(domz.Canonicalize(vm))
	for _, suffix := range []string{s.env.BoxHost, "exe.xyz", "exe-staging.xyz"} {
		if name, ok := domz.CutBase(vm, suffix); ok {
			vm = name
			break
		}
	}
	if !boxname.IsValid(vm) {
		http.Error(w, `{"error":"invalid VM name"}`, http.StatusBadRequest)
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

	result := s.checkDNS(ctx, domain, vm)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) checkDNS(ctx context.Context, domain, vm string) dnsCheckResult {
	result := dnsCheckResult{
		Domain:  domain,
		VMName:  vm,
		BoxHost: s.env.BoxHost,
		BoxName: vm,
	}

	boxFQDN := domz.Canonicalize(vm + "." + s.env.BoxHost)

	// Resolve the VM's IPs. Used for apex A-record matching, and as a
	// sanity check that the user-supplied VM name actually exists.
	var boxIPs map[string]bool
	if ips, err := dnsresolver.Resolver().LookupNetIP(ctx, "ip4", boxFQDN); err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			result.BoxResolveError = "no DNS records for " + boxFQDN
		} else {
			result.BoxResolveError = err.Error()
		}
	} else if len(ips) == 0 {
		result.BoxResolveError = "no DNS records for " + boxFQDN
	} else {
		boxIPs = map[string]bool{}
		for _, ip := range ips {
			boxIPs[ip.Unmap().String()] = true
		}
		result.BoxIP = ips[0].Unmap().String()
	}

	// Step 1: CNAME lookup on the domain itself.
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
			if cname == boxFQDN {
				result.CNAMEPointsToExe = true
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
		allMatch := len(addrs) > 0 && boxIPs != nil
		for _, addr := range addrs {
			addr = addr.Unmap()
			result.ARecords = append(result.ARecords, addr.String())
			if boxIPs == nil || !boxIPs[addr.String()] {
				allMatch = false
			}
		}
		result.PointsToExe = allMatch
	}

	// Determine if apex
	result.IsApex = (result.CNAME == "" && len(result.ARecords) > 0)

	// Step 3: For apex domains or domains without a CNAME pointing to the
	// VM, also check the www subdomain.
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
				if wwwCname == boxFQDN {
					result.WWWPointsToExe = true
				}
			} else {
				result.WWWMissing = true
				result.WWWCNAMEError = "www points to itself, not to " + boxFQDN
			}
		}
	}

	// Determine overall status and message
	result.Status, result.Message = classifyDNSResult(&result, s.env.BoxHost)

	return result
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
	vmFQDN := r.BoxName + "." + boxHost

	// Case -1: We couldn't resolve the VM hostname itself. Probably a
	// mistyped VM name; without it we can't tell the user what their
	// records should resolve to.
	if r.BoxResolveError != "" {
		return "error", "We couldn't resolve " + vmFQDN + " (" + r.BoxResolveError + "). Check the VM name."
	}

	// Case 0: CNAME on an apex domain is an RFC violation.
	if r.ApexCNAME {
		return "error", "CNAME records are not allowed on apex domains (RFC 1912 § 2.4). A CNAME on " + r.Domain + " will break MX, NS, and other records. Replace it with an A, ALIAS, ANAME, or flattened-CNAME record pointing to " + vmFQDN + "."
	}

	// Case 1: CNAME directly points to the requested VM.
	if r.CNAMEPointsToExe {
		return "ok", "CNAME points to " + r.CNAME + ". Your domain is correctly configured."
	}

	// Case 2: CNAME exists but doesn't point to the requested VM.
	if r.CNAME != "" {
		return "error", "CNAME points to " + r.CNAME + ", not to " + vmFQDN + ". Update your CNAME to point to " + vmFQDN + "."
	}

	// Case 3: Apex domain with A records matching the VM's IP.
	if r.IsApex && r.PointsToExe {
		if r.WWWMissing {
			return "partial", "A record points to " + vmFQDN + ", but www." + r.Domain + " is missing a CNAME to " + vmFQDN + ". Add a CNAME record for www."
		}
		if r.WWWPointsToExe {
			return "ok", "Apex domain configured correctly. A record points to " + vmFQDN + " and www." + r.Domain + " points to " + r.WWWCNAME + "."
		}
		return "partial", "A record points to " + vmFQDN + ", but www." + r.Domain + " does not point to " + vmFQDN + ". Update the www CNAME."
	}

	// Case 4: Apex domain with A records NOT matching the VM.
	if r.IsApex && !r.PointsToExe {
		ips := strings.Join(r.ARecords, ", ")
		expected := vmFQDN
		if r.BoxIP != "" {
			expected = r.BoxIP + ", the current IP of " + vmFQDN
		}
		return "error", "A record resolves to " + ips + ", but should resolve to " + expected + ". Update your A/ALIAS/ANAME record."
	}

	// Case 5: No records at all
	if r.CNAME == "" && len(r.ARecords) == 0 {
		return "error", "No DNS records found for " + r.Domain + ". Add a CNAME record pointing to " + vmFQDN + "."
	}

	return "error", "DNS is not configured for " + vmFQDN + ". Add a CNAME record pointing to " + vmFQDN + "."
}
