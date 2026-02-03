package email

import (
	"strings"
)

// Rule defines how to canonicalize the local part and domain for a provider.
type Rule interface {
	// Canonicalize transforms the local part. The domain has already been lowercased.
	Canonicalize(local string) string
}

// RuleFunc adapts a plain function into a Rule.
type RuleFunc func(string) string

func (f RuleFunc) Canonicalize(local string) string { return f(local) }

// lowercaseRule: just lowercase. Used as the default for all domains.
var lowercaseRule = RuleFunc(func(local string) string {
	return strings.ToLower(local)
})

// --- Canonicalizer ---

// Canonicalizer normalizes email addresses for deduplication.
type Canonicalizer struct {
	// domain -> Rule
	rules map[string]Rule
	// domain aliases: alias -> canonical domain (e.g. "googlemail.com" -> "gmail.com")
	aliases map[string]string
	// fallback for unknown domains
	defaultRule Rule
}

// NewCanonicalizer creates a Canonicalizer with common provider aliases preconfigured.
func NewCanonicalizer() *Canonicalizer {
	c := &Canonicalizer{
		rules:       make(map[string]Rule),
		aliases:     make(map[string]string),
		defaultRule: lowercaseRule,
	}

	// Domain aliases — map alternate domains to a single canonical domain.
	c.AddAlias("googlemail.com", "gmail.com")
	c.AddAlias("google.com", "gmail.com")

	c.AddAlias("hotmail.com", "outlook.com")
	c.AddAlias("live.com", "outlook.com")

	c.AddAlias("ymail.com", "yahoo.com")

	c.AddAlias("proton.me", "protonmail.com")
	c.AddAlias("pm.me", "protonmail.com")

	c.AddAlias("me.com", "icloud.com")
	c.AddAlias("mac.com", "icloud.com")

	c.AddAlias("fastmail.fm", "fastmail.com")

	return c
}

// AddRule registers a canonicalization rule for a domain.
func (c *Canonicalizer) AddRule(domain string, r Rule) {
	c.rules[strings.ToLower(domain)] = r
}

// AddAlias maps an alias domain to a canonical domain.
func (c *Canonicalizer) AddAlias(alias, canonical string) {
	c.aliases[strings.ToLower(alias)] = strings.ToLower(canonical)
}

// SetDefault changes the fallback rule for unknown domains.
func (c *Canonicalizer) SetDefault(r Rule) {
	c.defaultRule = r
}

// Canonicalize normalizes an email address for deduplication.
func (c *Canonicalizer) Canonicalize(email string) (string, error) {
	email = strings.TrimSpace(email)
	email = strings.TrimRight(email, ".")

	at := strings.LastIndex(email, "@")
	if at < 1 || at >= len(email)-1 {
		return "", &InvalidEmailError{email}
	}

	local := email[:at]
	domain := strings.ToLower(email[at+1:])

	// Resolve alias
	if canonical, ok := c.aliases[domain]; ok {
		domain = canonical
	}

	// Pick rule
	rule := c.defaultRule
	if r, ok := c.rules[domain]; ok {
		rule = r
	}

	return rule.Canonicalize(local) + "@" + domain, nil
}

// --- Errors ---

// InvalidEmailError is returned when an email address cannot be parsed.
type InvalidEmailError struct {
	Input string
}

func (e *InvalidEmailError) Error() string {
	return "invalid email address: " + e.Input
}

// --- Default instance ---

var defaultCanonicalizer = NewCanonicalizer()

// CanonicalizeEmail normalizes an email address using the default canonicalizer.
func CanonicalizeEmail(addr string) (string, error) {
	return defaultCanonicalizer.Canonicalize(addr)
}
