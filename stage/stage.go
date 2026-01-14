// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

import (
	"fmt"
	"os"

	"exe.dev/billing"
)

// An Env represents a deployment stage/environment.
//
//exe:completeinit
type Env struct {
	// Note: there is intentionally no string Name or DevMode field here.
	// The instant one gets added, LLMs will start doing things like `if env.Name == "prod" {...}`.
	// That ends up being a fragile headache. We've been there once.
	// Instead, use specifically-named flags for specific features.
	// Avoid generic "is this prod or dev?" checks.

	WebHost  string // the base hostname of the website; prod is "exe.dev", dev is "localhost"
	ReplHost string // the base hostname of the repl; prod is "exe.dev"
	BoxHost  string // the base hostname of boxes; prod is "exe.dev" (but soon will be "exe.xyz"), dev is "exe.cloud"

	UseRoute53        bool // whether to use Route53 for DNS management and LetsEncrypt DNS challenges; when false, uses alley53 if available, otherwise noop
	UseCobble         bool // whether to start cobble/pebble for local ACME testing
	DiscoverPublicIPs bool // whether to attempt to discover public IPs of the server using EC2 metadata service

	FakeEmail   bool // whether to log emails instead of sending them
	SkipBilling bool // whether to skip billing/Stripe checkout for new signups (for tests)
	ReplDev     bool // whether to expose dev-only repl features (printing internal errors, showing hidden commands, skipping real email, etc.)
	WebDev      bool // whether to expose dev-only web features (auto-show email links, skipping real email, etc.)
	ProxyDev    bool // whether to expose dev-only proxy features (addressing a box directly via host:port, etc.)
	GatewayDev  bool // allow X-Exedev-Box auth even when request source IP isn't tailscale
	SkipBanner  bool // whether to skip showing the EXE banner on repl login

	ShowHiddenDocs    bool // whether to load and display unpublished docs
	AutoStartSSHPiper bool // whether to auto-start sshpiper for local workflows
	SSHCommandUsesAt  bool // whether ssh command logins use "box@host" format instead of "box.host" format
	PostSlackFeed     bool // whether to post feed events to Slack; when false, logs them instead

	LogCmdAttr           bool   // whether to include "cmd" attribute in log entries; useful in dev/test, where multiple logs are interwoven, noise in prod
	LogFormat            string // default log format: "tint", "text", or "json"; empty defaults to "text"
	LogLevel             string // default log level: "debug", "info", "warn", "error"; empty defaults to "info"
	LogErrorSlackChannel string // Slack channel for error logs; empty means no Slack posting
	SlackFeedChannel     string // Slack channel for user activity feed (signups, etc.); empty means no posting
	SlackOpsChannel      string // Slack channel for ops notifications (server starts, exelet changes); empty means no posting
	HoneycombEnv         string // Honeycomb environment name for trace links in Slack ("production", "staging"); empty means no links

	NumShards  int   // number of IP shards available for box allocation, max 253
	ProxyPorts []int // ports to listen on for proxying; empty means none

	DefaultMemory uint64 // default memory for new boxes in bytes
	DefaultDisk   uint64 // default disk size for new boxes in bytes

	StripeAPIKey string // Stripe API key for billing operations
}

// Invalid returns an Env with obviously invalid values.
// Use this instead of a zero Env when an invalid Env is needed.
// Values are set to fail closed: an invalid env means all features are disabled.
func Invalid() Env {
	return Env{
		WebHost:  "INVALID.INVALID",
		ReplHost: "INVALID.INVALID",
		BoxHost:  "INVALID.INVALID",

		UseRoute53:        false,
		UseCobble:         false,
		DiscoverPublicIPs: false,

		FakeEmail:   true, // something is wrong, so don't send real email
		SkipBilling: true, // something is wrong, so skip billing
		ReplDev:     false,
		WebDev:      false,
		ProxyDev:    false,
		GatewayDev:  false,
		SkipBanner:  false,

		ShowHiddenDocs:    false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		PostSlackFeed:     false,

		LogCmdAttr:           false,
		LogFormat:            "INVALID",
		LogLevel:             "INVALID",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		HoneycombEnv:         "",

		NumShards:  0, // invalid: must be >= 1
		ProxyPorts: nil,

		DefaultMemory: 0, // invalid: must be > 0
		DefaultDisk:   0, // invalid: must be > 0

		StripeAPIKey: "", // invalid: no API key
	}
}

// Local returns an Env configured for convenient local human development.
// It enables more expensive features (cobble, auto-starting sshpiper),
// and provides convenience shortcuts like email links in the console/web.
func Local() Env {
	return Env{
		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "exe.cloud",

		// auto-start cobble/pebble for ACME testing
		UseRoute53:        false,
		UseCobble:         true,
		DiscoverPublicIPs: false,

		FakeEmail:   true,
		SkipBilling: true,
		ReplDev:     true,
		WebDev:      true,
		ProxyDev:    true,
		GatewayDev:  true,
		SkipBanner:  false,

		ShowHiddenDocs:    true,
		AutoStartSSHPiper: true,
		SSHCommandUsesAt:  true,
		PostSlackFeed:     false,

		LogCmdAttr:           true,
		LogFormat:            "tint",
		LogLevel:             "debug",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		HoneycombEnv:         "",

		NumShards:  25,
		ProxyPorts: []int{8001, 8002, 8003, 8004, 8005, 8006, 8007, 8008, 9999},

		DefaultMemory: 1 * 1000 * 1000 * 1000,  // 1GB
		DefaultDisk:   10 * 1000 * 1000 * 1000, // 10GB

		StripeAPIKey: billing.TestAPIKey,
	}
}

// Test returns an Env configured for automated tests.
// It disables external dependencies and speeds up operations where possible.
// It should be otherwise similar to Staging/Prod.
// In particular, dev features should be disabled unless strictly needed for automated tests.
func Test() Env {
	return Env{
		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "exe.cloud",

		// tests start their own cobble/pebble instances as needed
		UseRoute53:        false,
		UseCobble:         false,
		DiscoverPublicIPs: false,

		FakeEmail:   true,
		SkipBilling: true,
		ReplDev:     false,
		WebDev:      false,
		ProxyDev:    true,
		GatewayDev:  true,
		SkipBanner:  true,

		ShowHiddenDocs:    true,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  true,
		PostSlackFeed:     false,

		LogCmdAttr:           true,
		LogFormat:            "text",
		LogLevel:             "info",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		HoneycombEnv:         "",

		NumShards:  25,
		ProxyPorts: nil, // no proxy ports in tests to avoid conflicts

		DefaultMemory: 1 * 1000 * 1000 * 1000,  // 1GB
		DefaultDisk:   10 * 1000 * 1000 * 1000, // 10GB

		StripeAPIKey: billing.TestAPIKey,
	}
}

// Staging returns an Env configured for the staging deployment environment.
// It should be as similar as possible to Prod, but with staging domains.
func Staging() Env {
	return Env{
		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.xyz",

		UseRoute53:        true,
		UseCobble:         false,
		DiscoverPublicIPs: true,

		FakeEmail:   false,
		SkipBilling: false,
		ReplDev:     false,
		WebDev:      false,
		ProxyDev:    false,
		GatewayDev:  false,
		SkipBanner:  false,

		ShowHiddenDocs:    false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		PostSlackFeed:     false,

		LogCmdAttr:           true,
		LogFormat:            "json",
		LogLevel:             "info",
		LogErrorSlackChannel: "stag",
		SlackFeedChannel:     "stag",
		SlackOpsChannel:      "stag",
		HoneycombEnv:         "staging",

		NumShards:  25,
		ProxyPorts: portRange(3000, 9999),

		DefaultMemory: 8 * 1000 * 1000 * 1000,  // 8GB
		DefaultDisk:   20 * 1000 * 1000 * 1000, // 20GB

		StripeAPIKey: os.Getenv("STRIPE_API_KEY"),
	}
}

// Prod returns an Env configured for prod.
func Prod() Env {
	return Env{
		WebHost:  "exe.dev",
		ReplHost: "exe.dev",
		BoxHost:  "exe.xyz",

		UseRoute53:        true,
		UseCobble:         false,
		DiscoverPublicIPs: true,

		FakeEmail:   false,
		SkipBilling: false,
		ReplDev:     false,
		WebDev:      false,
		ProxyDev:    false,
		GatewayDev:  false,
		SkipBanner:  false,

		ShowHiddenDocs:    false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		PostSlackFeed:     true,

		LogCmdAttr:           true,
		LogFormat:            "json",
		LogLevel:             "info",
		LogErrorSlackChannel: "errs",
		SlackFeedChannel:     "feed",
		SlackOpsChannel:      "buzz",
		HoneycombEnv:         "production",

		NumShards:  25,
		ProxyPorts: portRange(3000, 9999),

		DefaultMemory: 8 * 1000 * 1000 * 1000,  // 8GB
		DefaultDisk:   20 * 1000 * 1000 * 1000, // 20GB

		StripeAPIKey: os.Getenv("STRIPE_API_KEY"),
	}
}

// Parse parses a stage name and returns the corresponding Env.
// Valid names are "prod", "staging", "local", and "test".
func Parse(name string) (Env, error) {
	switch name {
	case "prod":
		return Prod(), nil
	case "staging":
		return Staging(), nil
	case "local":
		return Local(), nil
	case "test":
		return Test(), nil
	default:
		return Invalid(), fmt.Errorf("invalid stage %q: must be prod, staging, local, or test", name)
	}
}

// portRange returns a slice of integers from start to end inclusive.
func portRange(start, end int) []int {
	ports := make([]int, 0, end-start+1)
	for p := start; p <= end; p++ {
		ports = append(ports, p)
	}
	return ports
}

func (e Env) String() string {
	return e.ReplHost
}

func (e Env) BoxSub(sub string) string        { return sub + "." + e.BoxHost }
func (e Env) BoxXtermSub(sub string) string   { return sub + ".xterm." + e.BoxHost }
func (e Env) BoxShelleySub(sub string) string { return sub + ".shelley." + e.BoxHost }

// ShardIsValid reports whether shard is within the valid range for this stage.
func (e Env) ShardIsValid(shard int) bool {
	return shard >= 1 && shard <= e.NumShards
}

// BoxDest returns the SSH destination for a box in this environment.
// For local env, it's "boxname@boxhost".
// For non-local env, it's "boxname.boxhost".
func (e Env) BoxDest(boxName string) string {
	if e.SSHCommandUsesAt {
		return fmt.Sprintf("%s@%s", boxName, e.BoxHost)
	}
	return fmt.Sprintf("%s.%s", boxName, e.BoxHost)
}

// BillingClient returns a configured billing manager for this environment.
func (e Env) BillingClient() *billing.Manager {
	return &billing.Manager{APIKey: e.StripeAPIKey}
}

// MailgunDomain returns the Mailgun sending domain for this environment.
// This is the same as WebHost.
func (e Env) MailgunDomain() string {
	return e.WebHost
}
