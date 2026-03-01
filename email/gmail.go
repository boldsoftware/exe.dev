package email

import "strings"

// IsGmailAddress returns true if the email address is a consumer Gmail address
// (gmail.com or googlemail.com).
func IsGmailAddress(addr string) bool {
	_, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return false
	}
	domain = strings.ToLower(domain)
	return domain == "gmail.com" || domain == "googlemail.com"
}

// StripPlusSuffix removes the +suffix from the local part of an email address.
// For example, "user+tag@gmail.com" becomes "user@gmail.com".
// If there is no plus addressing, the original address is returned unchanged.
// The input must be a syntactically valid email address (local@domain).
func StripPlusSuffix(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return addr
	}
	base, _, _ := strings.Cut(local, "+")
	return base + "@" + domain
}
