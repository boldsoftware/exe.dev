// Package domz provides utilities for working with domains and hostnames and DNS, oh my.
package domz

import (
	"net"
	"strings"
)

// Canonicalize returns the canonical form of hostname.
// It lowercases the hostname and removes any trailing dot.
func Canonicalize(hostname string) string {
	return strings.TrimSuffix(strings.ToLower(hostname), ".")
}

// Matches reports whether host is equal to or a subdomain of domain.
func Matches(host, domain string) bool {
	host = Canonicalize(host)
	domain = Canonicalize(domain)
	if host == "" || domain == "" {
		return false
	}
	if host == domain {
		return true
	}
	return strings.HasSuffix(host, "."+domain)
}

// FirstMatch reports the first domain in domains that host is equal to or a subdomain of.
// If none match, FirstMatch returns "".
func FirstMatch(host string, domains ...string) string {
	for _, domain := range domains {
		if Matches(host, domain) {
			return domain
		}
	}
	return ""
}

// CutBase reports whether hostname ends with the given base domain (suffix).
// If so, it returns the label/subdomain part (without the base domain).
func CutBase(hostname, domain string) (string, bool) {
	hostname = Canonicalize(hostname)
	domain = Canonicalize(domain)
	if domain == "" {
		return "", false
	}
	hostname, _ = strings.CutSuffix(hostname, domain)
	if hostname == "" {
		// Either root/apex, or different domain entirely, or empty string.
		return "", false
	}
	host, isSub := strings.CutSuffix(hostname, ".")
	if !isSub {
		return "", false
	}
	return host, true
}

// Label returns the sole subdomain part of hostname relative to domain.
// If hostname is not a subdomain of domain, or domain is empty, or hostname is a nested subdomain, Label returns "".
func Label(hostname, domain string) string {
	sub, ok := CutBase(hostname, domain)
	if !ok || strings.Contains(sub, ".") {
		return ""
	}
	return sub
}

// StripPort removes the port from host if present.
func StripPort(host string) string {
	hostname, _, err := net.SplitHostPort(host)
	if err == nil {
		// Had a port: return just the hostname.
		return hostname
	}
	return host
}

// FilterEmpty returns a new slice with all empty strings removed from the input slice.
func FilterEmpty(strs []string) []string {
	var result []string
	for _, s := range strs {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

func IsLocalhost(host string) bool {
	host = StripPort(Canonicalize(host))
	return host == "localhost" || host == "127.0.0.1" // no IPv6 anywhere in our systems yet
}
