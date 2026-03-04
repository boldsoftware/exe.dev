package execore

import (
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// validateIntegrationName checks that a name is a valid DNS label suitable for
// use as a subdomain (e.g. "myproxy" in "myproxy.int.exe.cloud").
// Rules: 1–63 chars, lowercase ASCII letters, digits, and hyphens,
// must start and end with a letter or digit.
func validateIntegrationName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 63 {
		return fmt.Errorf("name must be 63 characters or fewer")
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			return fmt.Errorf("name must be lowercase (got %q)", name)
		}
		if c == '-' {
			return fmt.Errorf("name must not start or end with a hyphen")
		}
		return fmt.Errorf("name contains invalid character %q; use lowercase letters, digits, and hyphens", c)
	}
	return nil
}

// validateHTTPHeader validates a header string in "Key:Value" format.
// The key must be a valid HTTP header token; the value must not contain
// CR/LF characters (defense-in-depth against header injection).
func validateHTTPHeader(header string) error {
	key, value, ok := strings.Cut(header, ":")
	if !ok {
		return fmt.Errorf("header must be in Key:Value format")
	}
	if key == "" {
		return fmt.Errorf("header key is empty")
	}
	for _, c := range key {
		if !isTokenChar(byte(c)) {
			return fmt.Errorf("header key %q contains invalid character %q", key, c)
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("header value is empty")
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header value must not contain CR or LF characters")
	}
	// Reject internal headers that would be scrubbed.
	canon := textproto.CanonicalMIMEHeaderKey(key)
	if canon == "X-Exedev-Box" || canon == "X-Exedev-Integration" || canon == http.CanonicalHeaderKey("Host") {
		return fmt.Errorf("header key %q is reserved", key)
	}
	return nil
}

// isTokenChar reports whether c is a valid HTTP token character per RFC 7230.
func isTokenChar(c byte) bool {
	// token = 1*tchar
	// tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
	//         "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

// validateTargetURL checks that a target URL is well-formed HTTPS with a real
// hostname (not a bare IP). The real security enforcement (private-IP blocking,
// Tailscale-IP blocking, post-connect verification) happens at dial time in the
// exelet; this is defense-in-depth.
func validateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("target URL scheme must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("target URL must have a host")
	}
	hostname := u.Hostname()
	if net.ParseIP(hostname) != nil {
		return fmt.Errorf("target URL must use a hostname, not an IP address")
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("target URL must use port 443 (got %s)", port)
	}
	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("target URL must not point to localhost")
	}
	if strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("target URL must not use a .local domain")
	}
	if strings.HasSuffix(lower, ".internal") {
		return fmt.Errorf("target URL must not use a .internal domain")
	}
	if strings.HasSuffix(lower, ".ts.net") {
		return fmt.Errorf("target URL must not use a .ts.net domain")
	}
	if strings.HasSuffix(lower, ".exe.cloud") {
		return fmt.Errorf("target URL must not use a .exe.cloud domain")
	}
	if strings.HasSuffix(lower, ".exe.dev") {
		return fmt.Errorf("target URL must not use a .exe.dev domain")
	}
	return nil
}
