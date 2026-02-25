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
