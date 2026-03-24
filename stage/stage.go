// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Resource limits for VM creation
const (
	// Minimum resource limits (all users)
	MinMemory = 2 * 1000 * 1000 * 1000 // 2GB minimum RAM
	MinDisk   = 4 * 1000 * 1000 * 1000 // 4GB minimum disk (for ZFS base image)
	MinCPUs   = 1                      // 1 CPU minimum

	// Maximum resource limits for support users (root_support=1)
	SupportMaxMemory = 32 * 1000 * 1000 * 1000  // 32GB max RAM for support
	SupportMaxDisk   = 128 * 1000 * 1000 * 1000 // 128GB max disk for support
	SupportMaxCPUs   = 8                        // 8 CPUs max for support

	// DefaultMaxBoxes is the default maximum number of VMs per user.
	DefaultMaxBoxes = 25
	// DefaultMaxTeamBoxes is the default maximum number of VMs per team.
	DefaultMaxTeamBoxes = 100
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
	//
	// Similarly, do not switch on one attribute to derive another.
	// If you need an X, just add an X field directly. Fields are cheap!
	//
	// Pass Env as a whole struct to functions, not individual fields.
	// This keeps callsites self-documenting and lets the callee
	// decide which fields matter.

	WebHost  string // the base hostname of the website; prod is "exe.dev", dev is "localhost"
	ReplHost string // the base hostname of the repl; prod is "exe.dev"
	BoxHost  string // the base hostname of boxes; prod is "exe.dev" (but soon will be "exe.xyz"), dev is "exe.cloud"

	UseCobble            bool // whether to start cobble/pebble for local ACME testing
	DiscoverPublicIPs    bool // whether to attempt to discover public IPs of the server using EC2 metadata service
	PreloadTailscaleCert bool // whether to preload tailscale cert at startup (has 10s timeout, skip in tests)
	EnableLMTP           bool // whether to start the LMTP server for inbound email delivery

	MaxMaildirEmails int // max emails allowed in ~/Maildir/new before auto-disabling receive

	FakeEmail      bool // whether to log emails instead of sending them
	SkipBilling    bool // whether to skip billing/Stripe checkout for new signups (for tests)
	ReplDev        bool // whether to expose dev-only repl features (printing internal errors, showing hidden commands, skipping real email, etc.)
	WebDev         bool // whether to expose dev-only web features (auto-show email links, skipping real email, etc.)
	ProxyDev       bool // whether to expose dev-only proxy features (addressing a box directly via host:port, etc.)
	GatewayDev     bool // allow X-Exedev-Box auth even when request source IP isn't tailscale
	SkipBanner     bool // whether to skip showing the EXE banner on repl login
	BehindTLSProxy bool // whether running behind an external TLS-terminating proxy (e.g., exe.dev proxy)
	ExedWarnProxy  bool // exed will issue an error if it sees a proxy request that should have gone to exeprox

	ShowHiddenDocs    bool // whether to load and display unpublished docs
	ShowDocsPreview   bool // whether to display preview docs to all users; true for all stages except prod (sudoers always see them)
	AutoStartSSHPiper bool // whether to auto-start sshpiper for local workflows
	SSHCommandUsesAt  bool // whether ssh command logins use "box@host" format instead of "box.host" format
	AllowDeleteUser   bool // whether the debug delete-user endpoint is enabled; disabled in prod
	PostSlackFeed     bool // whether to post feed events to Slack; when false, logs them instead

	LogCmdAttr           bool   // whether to include "cmd" attribute in log entries; useful in dev/test, where multiple logs are interwoven, noise in prod
	LogFormat            string // default log format: "tint", "text", or "json"; empty defaults to "text"
	LogLevel             string // default log level: "debug", "info", "warn", "error"; empty defaults to "info"
	LogErrorSlackChannel string // Slack channel for error logs; empty means no Slack posting
	SlackFeedChannel     string // Slack channel for user activity feed (signups, etc.); empty means no posting
	SlackOpsChannel      string // Slack channel for ops notifications (server starts, exelet changes); empty means no posting
	SlackPageChannel     string // Slack channel for urgent pages (capacity warnings, etc.); empty means no posting
	HoneycombEnv         string // Honeycomb environment name for trace links in Slack ("production", "staging"); empty means no links

	NumShards  int   // number of IP shards available for box allocation
	ProxyPorts []int // ports to listen on for proxying; empty means none

	DefaultMemory uint64 // default memory for new boxes in bytes
	DefaultDisk   uint64 // default disk size for new boxes in bytes
	DefaultCPUs   uint64 // default number of CPUs for new boxes

	ListenOnTailscaleOnly bool // whether auxiliary daemons (metricsd) should bind only to the tailscale interface
	RedirectHTTPToHTTPS   bool // whether the HTTP server should redirect all requests to HTTPS (port 80 → 443)

	GitHubTokenRenewalStartupDelay time.Duration // base delay before first GitHub token renewal check; jitter of equal magnitude is added

	ProdLockEnv string // prodlock environment to lock during mass VM migrations; empty means no locking

	DebugLabel   string // short stage label for the /debug UI badge ("prod", "staging", "dev")
	DebugColor   string // CSS background color for the /debug UI badge (e.g. "#dc3545")
	DebugBgColor string // subtle CSS page background tint keyed to stage (e.g. "#fff5f5" for prod)

	SignupAllowlist *SignupAllowlist // if non-nil, only these emails/domains can sign up
}

// Invalid returns an Env with obviously invalid values.
// Use this instead of a zero Env when an invalid Env is needed.
// Values are set to fail closed: an invalid env means all features are disabled.
func Invalid() Env {
	return Env{
		WebHost:  "INVALID.INVALID",
		ReplHost: "INVALID.INVALID",
		BoxHost:  "INVALID.INVALID",

		UseCobble:            false,
		DiscoverPublicIPs:    false,
		PreloadTailscaleCert: false,
		EnableLMTP:           false,

		MaxMaildirEmails: 0,

		FakeEmail:      true, // something is wrong, so don't send real email
		SkipBilling:    true, // something is wrong, so skip billing
		ReplDev:        false,
		WebDev:         false,
		ProxyDev:       false,
		GatewayDev:     false,
		SkipBanner:     false,
		BehindTLSProxy: false,
		ExedWarnProxy:  false,

		ShowHiddenDocs:    false,
		ShowDocsPreview:   false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		AllowDeleteUser:   false,
		PostSlackFeed:     false,

		LogCmdAttr:           false,
		LogFormat:            "INVALID",
		LogLevel:             "INVALID",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		SlackPageChannel:     "",
		HoneycombEnv:         "",

		NumShards:  0, // invalid: must be >= 1
		ProxyPorts: nil,

		DefaultMemory: 0, // invalid: must be > 0
		DefaultDisk:   0, // invalid: must be > 0
		DefaultCPUs:   0, // invalid: must be > 0

		ListenOnTailscaleOnly: false,
		RedirectHTTPToHTTPS:   false,

		GitHubTokenRenewalStartupDelay: 5 * time.Minute,

		ProdLockEnv: "",

		DebugLabel:   "INVALID",
		DebugColor:   "#999999",
		DebugBgColor: "#f5f5f5",

		SignupAllowlist: &SignupAllowlist{}, // fail closed: empty allowlist blocks all signups
	}
}

var envStripeKey = os.Getenv("STRIPE_SECRET_KEY")

// Local returns an Env configured for convenient local human development.
// It enables more expensive features (cobble, auto-starting sshpiper),
// and provides convenience shortcuts like email links in the console/web.
func Local() Env {
	webHost := "localhost"
	onExeBox := false
	if fqdn := exeBoxFQDN(); fqdn != "" {
		webHost = fqdn
		onExeBox = true
	}
	return Env{
		WebHost:  webHost,
		ReplHost: webHost,
		BoxHost:  "exe.cloud",

		UseCobble:            !onExeBox, // auto-start cobble/pebble for ACME testing (not needed behind proxy)
		DiscoverPublicIPs:    false,
		PreloadTailscaleCert: false,
		EnableLMTP:           true,

		MaxMaildirEmails: 5, // low limit for local dev/testing

		FakeEmail:      true,
		SkipBilling:    envStripeKey == "",
		ReplDev:        true,
		WebDev:         true,
		ProxyDev:       true,
		GatewayDev:     true,
		SkipBanner:     false,
		BehindTLSProxy: onExeBox,
		ExedWarnProxy:  false,

		ShowHiddenDocs:    true,
		ShowDocsPreview:   true,
		AutoStartSSHPiper: true,
		SSHCommandUsesAt:  true,
		AllowDeleteUser:   true,
		PostSlackFeed:     false,

		LogCmdAttr:           true,
		LogFormat:            "tint",
		LogLevel:             "debug",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		SlackPageChannel:     "",
		HoneycombEnv:         "",

		NumShards:  25,
		ProxyPorts: []int{8001, 8002, 8003, 8004, 8005, 8006, 8007, 8008, 9999},

		ListenOnTailscaleOnly: false,
		RedirectHTTPToHTTPS:   false,

		DefaultMemory: 1 * 1000 * 1000 * 1000,  // 1GB
		DefaultDisk:   10 * 1000 * 1000 * 1000, // 10GB
		DefaultCPUs:   2,

		GitHubTokenRenewalStartupDelay: 5 * time.Second,

		ProdLockEnv: "",

		DebugLabel:   "dev",
		DebugColor:   "#2d6a30",
		DebugBgColor: "#f0fdf4",

		SignupAllowlist: nil, // no restriction for local dev
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

		UseCobble:            false, // tests start their own cobble/pebble instances as needed
		DiscoverPublicIPs:    false,
		PreloadTailscaleCert: false,
		EnableLMTP:           true,

		MaxMaildirEmails: 5, // low limit for testing

		FakeEmail:      true,
		SkipBilling:    envStripeKey == "",
		ReplDev:        false,
		WebDev:         false,
		ProxyDev:       true,
		GatewayDev:     true,
		SkipBanner:     true,
		BehindTLSProxy: false,
		ExedWarnProxy:  true,

		ShowHiddenDocs:    true,
		ShowDocsPreview:   true,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  true,
		AllowDeleteUser:   true,
		PostSlackFeed:     false,

		LogCmdAttr:           true,
		LogFormat:            "text",
		LogLevel:             "info",
		LogErrorSlackChannel: "",
		SlackFeedChannel:     "",
		SlackOpsChannel:      "",
		SlackPageChannel:     "",
		HoneycombEnv:         "",

		NumShards:  25,
		ProxyPorts: nil, // no proxy ports in tests to avoid conflicts

		ListenOnTailscaleOnly: false,
		RedirectHTTPToHTTPS:   false,

		DefaultMemory: 1 * 1000 * 1000 * 1000,  // 1GB
		DefaultDisk:   11 * 1000 * 1000 * 1000, // 11GB
		DefaultCPUs:   2,

		GitHubTokenRenewalStartupDelay: 5 * time.Second,

		ProdLockEnv: "",

		DebugLabel:   "test",
		DebugColor:   "#2d6a30",
		DebugBgColor: "#f0fdf4",

		SignupAllowlist: nil, // no restriction for tests
	}
}

// Staging returns an Env configured for the staging deployment environment.
// It should be as similar as possible to Prod, but with staging domains.
func Staging() Env {
	return Env{
		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.xyz",

		UseCobble:            false,
		DiscoverPublicIPs:    true,
		PreloadTailscaleCert: true,
		EnableLMTP:           true,

		MaxMaildirEmails: 1000, // 1000 emails before auto-disable

		FakeEmail:      false,
		SkipBilling:    false,
		ReplDev:        false,
		WebDev:         false,
		ProxyDev:       false,
		GatewayDev:     false,
		SkipBanner:     false,
		BehindTLSProxy: false,
		ExedWarnProxy:  false, // make true when using global load balancer

		ShowHiddenDocs:    false,
		ShowDocsPreview:   true,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		AllowDeleteUser:   true,
		PostSlackFeed:     false,

		LogCmdAttr:           false,
		LogFormat:            "json",
		LogLevel:             "info",
		LogErrorSlackChannel: "stag",
		SlackFeedChannel:     "stag",
		SlackOpsChannel:      "stag",
		SlackPageChannel:     "stag",
		HoneycombEnv:         "staging",

		ListenOnTailscaleOnly: true,
		RedirectHTTPToHTTPS:   true,

		NumShards:  1016,
		ProxyPorts: portRange(3000, 9999),

		DefaultMemory: 8 * 1000 * 1000 * 1000,  // 8GB
		DefaultDisk:   20 * 1000 * 1000 * 1000, // 20GB
		DefaultCPUs:   2,

		GitHubTokenRenewalStartupDelay: 5 * time.Minute,

		ProdLockEnv: "staging",

		DebugLabel:   "staging",
		DebugColor:   "#5b3a9e",
		DebugBgColor: "#f5f0ff",

		SignupAllowlist: &SignupAllowlist{
			Emails: []string{
				"david.crawshaw@gmail.com",
				"david@zentus.com",
				"josharian@gmail.com",
				"nchazlett@gmail.com",
				"philip.zeyliger@gmail.com",
			},
			Domains: []string{
				"bold.dev",
				"chicken.exe.xyz",
				"exe.dev",
				"phil-dev.exe.xyz",
				"example.com", // nobody can receive email here, which makes it convenient to open up for placeholder / similar tests
			},
		},
	}
}

// Prod returns an Env configured for prod.
func Prod() Env {
	return Env{
		WebHost:  "exe.dev",
		ReplHost: "exe.dev",
		BoxHost:  "exe.xyz",

		UseCobble:            false,
		DiscoverPublicIPs:    true,
		PreloadTailscaleCert: true,
		EnableLMTP:           true,

		MaxMaildirEmails: 1000, // 1000 emails before auto-disable

		FakeEmail:      false,
		SkipBilling:    false,
		ReplDev:        false,
		WebDev:         false,
		ProxyDev:       false,
		GatewayDev:     false,
		SkipBanner:     false,
		BehindTLSProxy: false,
		ExedWarnProxy:  true,

		ShowHiddenDocs:    false,
		ShowDocsPreview:   false,
		AutoStartSSHPiper: false,
		SSHCommandUsesAt:  false,
		AllowDeleteUser:   false,
		PostSlackFeed:     true,

		LogCmdAttr:           false,
		LogFormat:            "json",
		LogLevel:             "info",
		LogErrorSlackChannel: "errs",
		SlackFeedChannel:     "feed",
		SlackOpsChannel:      "buzz",
		SlackPageChannel:     "page",
		HoneycombEnv:         "production",

		ListenOnTailscaleOnly: true,
		RedirectHTTPToHTTPS:   true,

		NumShards:  1016,
		ProxyPorts: portRange(3000, 9999),

		DefaultMemory: 8 * 1000 * 1000 * 1000,  // 8GB
		DefaultDisk:   20 * 1000 * 1000 * 1000, // 20GB
		DefaultCPUs:   2,

		GitHubTokenRenewalStartupDelay: 5 * time.Minute,

		ProdLockEnv: "prod",

		DebugLabel:   "prod",
		DebugColor:   "#8b6914",
		DebugBgColor: "#fdf8f0",

		SignupAllowlist: nil, // no restriction for prod
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

// IntegrationHostSuffix returns the domain suffix for integration proxy requests
// (e.g., ".int.exe.xyz" in prod, ".int.exe.cloud" in local/test).
func (e Env) IntegrationHostSuffix() string { return ".int." + e.BoxHost }

// TeamIntHostSuffix returns the domain suffix for team integration proxy requests
// (e.g., ".team-int.exe.xyz" in prod, ".team-int.exe.cloud" in local/test).
func (e Env) TeamIntHostSuffix() string { return ".team-int." + e.BoxHost }

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

// TailscaleListenAddr returns a listen address bound to the tailscale interface
// for the given port. If ListenOnTailscaleOnly is false, it returns ":port".
func (e Env) TailscaleListenAddr(port string) (string, error) {
	if !e.ListenOnTailscaleOnly {
		return ":" + port, nil
	}
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale ip -4: %w", err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("tailscale ip -4 returned empty")
	}
	return ip + ":" + port, nil
}

// MailgunDomain returns the Mailgun sending domain for this environment.
// This is the same as WebHost.
func (e Env) MailgunDomain() string {
	return e.WebHost
}

// exeBoxFQDN returns the FQDN if running on an exe.dev VM (*.exe.xyz),
// or empty string otherwise. This allows stage.Local() to auto-detect
// the correct WebHost when running behind the exe.dev TLS proxy.
func exeBoxFQDN() string {
	out, err := exec.Command("hostname", "-f").Output()
	if err != nil {
		return ""
	}
	fqdn := strings.TrimSpace(string(out))
	if strings.HasSuffix(fqdn, ".exe.xyz") {
		return fqdn
	}
	return ""
}
