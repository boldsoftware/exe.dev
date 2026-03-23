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

// StripDots removes all dots from the local part of an email address.
// Gmail treats dots as cosmetic: "a.b.c@gmail.com" and "abc@gmail.com"
// are the same mailbox.
func StripDots(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return addr
	}
	local, domain := addr[:at], addr[at+1:]
	return strings.ReplaceAll(local, ".", "") + "@" + domain
}

// GmailEqual reports whether two Gmail addresses refer to the same mailbox,
// ignoring dots and plus-suffixes in the local part.
func GmailEqual(a, b string) bool {
	return strings.EqualFold(StripDots(StripPlusSuffix(a)), StripDots(StripPlusSuffix(b)))
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
