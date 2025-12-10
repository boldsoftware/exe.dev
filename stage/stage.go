// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

import "fmt"

// An Env represents a deployment stage/environment.
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

	FakeEmail  bool // whether to log emails instead of sending them
	ReplDev    bool // whether to expose dev-only repl features (printing internal errors, showing hidden commands, skipping real email, etc.)
	WebDev     bool // whether to expose dev-only web features (auto-show email links, skipping real email, etc.)
	ProxyDev   bool // whether to expose dev-only proxy features (addressing a box directly via host:port, etc.)
	GatewayDev bool // allow X-Exedev-Box auth even when request source IP isn't tailscale
	SkipBanner bool // whether to skip showing the EXE banner on repl login

	ShowHiddenDocs    bool // whether to load and display unpublished docs
	AutoStartSSHPiper bool // whether to auto-start sshpiper for local workflows
	SSHCommandUsesAt  bool // whether ssh command logins use "box@host" format instead of "box.host" format

	NumShards int // number of IP shards available for box allocation, max 253
}

// BoxLogin returns the login string for a box in this environment.
// For local env, it's "boxname@boxhost".
// For non-local env, it's "boxname.boxhost".
func (e Env) BoxLogin(boxName string) string {
	if e.SSHCommandUsesAt {
		return fmt.Sprintf("%s@%s", boxName, e.BoxHost)
	}
	return fmt.Sprintf("%s.%s", boxName, e.BoxHost)
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

		FakeEmail:  true,
		ReplDev:    true,
		WebDev:     true,
		ProxyDev:   true,
		GatewayDev: true,
		SkipBanner: false,

		ShowHiddenDocs:    true,
		AutoStartSSHPiper: true,
		SSHCommandUsesAt:  true,

		NumShards: 25,
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

		FakeEmail:  true,
		ReplDev:    false,
		WebDev:     false,
		ProxyDev:   true,
		GatewayDev: true,
		SkipBanner: true,

		ShowHiddenDocs:    true,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  true,

		NumShards: 25,
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

		FakeEmail:  false,
		ReplDev:    false,
		WebDev:     false,
		ProxyDev:   false,
		GatewayDev: false,
		SkipBanner: false,

		ShowHiddenDocs:    false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,

		NumShards: 25,
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

		FakeEmail:  false,
		ReplDev:    false,
		WebDev:     false,
		ProxyDev:   false,
		GatewayDev: false,
		SkipBanner: false,

		ShowHiddenDocs:    false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,

		NumShards: 25,
	}
}

func (e Env) String() string {
	return e.ReplHost
}

func (e Env) BoxSub(sub string) string      { return sub + "." + e.BoxHost }
func (e Env) BoxXtermSub(sub string) string { return sub + ".xterm." + e.BoxHost }

// ShardIsValid reports whether shard is within the valid range for this stage.
func (e Env) ShardIsValid(shard int) bool {
	return shard >= 1 && shard <= e.NumShards
}
