// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

// An Env represents a deployment stage/environment.
type Env struct {
	Name string // the name of the stage (for logging/debugging): "prod", "staging", "local", "test", etc.

	WebHost  string // the base hostname of the website; prod is "exe.dev"
	ReplHost string // the base hostname of the repl; prod is "exe.dev"
	BoxHost  string // the base hostname of boxes; prod is "exe.dev" (but soon will be "exe.xyz")

	UseRoute53        bool // whether to use Route53 for DNS management and LetsEncrypt DNS challenges
	UseCobble         bool // whether to start cobble/pebble for local ACME testing
	DiscoverPublicIPs bool // whether to attempt to discover public IPs of the server using EC2 metadata service

	FakeEmail  bool // whether to log emails instead of sending them
	ReplDev    bool // whether to expose dev-only repl features (printing internal errors, showing hidden commands)
	WebDev     bool // whether to expose dev-only web features (auto-show email links, skipping real email, etc.)
	ProxyDev   bool // whether to expose dev-only proxy features (addressing a box directly via host:port, etc.)
	GatewayDev bool // allow X-Exedev-Box auth even when request source IP isn't tailscale
	SkipBanner bool // whether to skip showing the EXE banner on repl login

	ShowHiddenDocs    bool // whether to load and display unpublished docs
	AutoStartSSHPiper bool // whether to auto-start sshpiper for local workflows

	DevMode string // dev mode: "local", "test", or ""; TODO: delete in favor of more precise flags
}

func (e Env) String() string {
	if e.DevMode != "" {
		return e.Name + "(" + e.DevMode + ")"
	}
	return e.Name
}

func Local() Env {
	return Env{
		Name: "local",

		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

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

		DevMode: "local",
	}
}

func Test() Env {
	return Env{
		Name: "test",

		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

		// tests start their own cobble/pebble instances as needed
		UseRoute53:        false,
		UseCobble:         false,
		DiscoverPublicIPs: false,

		FakeEmail:  true,
		ReplDev:    false,
		WebDev:     true,
		ProxyDev:   true,
		GatewayDev: true,
		SkipBanner: true,

		ShowHiddenDocs:    true,
		AutoStartSSHPiper: false,

		DevMode: "test",
	}
}

func Staging() Env {
	return Env{
		Name: "staging",

		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.dev",

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

		DevMode: "",
	}
}

func Prod() Env {
	return Env{
		Name: "prod",

		WebHost:  "exe.dev",
		ReplHost: "exe.dev",
		BoxHost:  "exe.dev",

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

		DevMode: "",
	}
}
