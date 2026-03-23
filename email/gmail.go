package email

import "strings"

// IsGmailAddress returns true if the email address is a consumer Gmail address
// (gmail.com or googlemail.com).
func IsGmailAddress(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(addr[at+1:])
	return domain == "gmail.com" || domain == "googlemail.com"
}

// StripPlusSuffix removes the +suffix from the local part of an email address.
// For example, "user+tag@gmail.com" becomes "user@gmail.com".
// If there is no plus addressing, the original address is returned unchanged.
// The input must be a syntactically valid email address (local@domain).
func StripPlusSuffix(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return addr
	}
	local, domain := addr[:at], addr[at+1:]
	base, _, _ := strings.Cut(local, "+")
	return base + "@" + domain
}
