package stage

// SignupAllowlist restricts which email addresses can sign up.
// A nil *SignupAllowlist on an Env means all emails are allowed (no restriction).
// A non-nil allowlist means only matching emails are permitted.
type SignupAllowlist struct {
	Emails  []string // individual emails; "user+anything@domain" variants are automatically allowed
	Domains []string // domain names; any email @domain is allowed
}
