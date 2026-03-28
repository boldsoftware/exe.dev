package boxname

import (
	"errors"
	mathrand "math/rand"
	"regexp"
	"strings"
)

// denySubstrings are vmnames that are forbidden even as substrings.
// They are for avoiding names known to be used by spammers, often drugspam.
// See what happens when these are actively ignored:
// https://www.google.com/search?q=ollama+adderall
var denySubstrings = []string{
	"adderall", "ambien", "antidepressant", "antiviral", "antivirus", "anxiety", "bipolar", "cialis", "depression",
	"erectile", "fatigue", "fibromyalgia", "gabapentin", "herpes", "hiv", "insomnia", "levitra", "lupus",
	"melatonin", "narcotic", "opioid", "oxycodone", "painkiller", "parkinson", "pharmacy", "prescription",
	"prozac", "sildenafil", "sleepaid", "tramadol", "viagra", "xanax", "zolpidem",

	"exelet", // reserved for assorted exelet-specific uses
}

// reserved is a list of reserved or denylisted box names.
// It does not include names that are invalid for other reasons (too short, invalid characters, etc).
var reserved = []string{
	"teams", "abort", "admin", "allow", "array", "async", "audit", "block", "board", "boost", "break", "build", "bytes", "cable", "cache", "catch", "chain",
	"check", "chips", "class", "clock", "cloud", "codec", "codes", "const", "cores", "crawl", "crypt", "debug", "drive", "email", "entry", "error", "event",
	"fetch", "fiber", "field", "flash", "frame", "games", "grant", "guard", "guest", "https", "image", "index", "input", "laser", "links", "logic", "login",
	"macro", "match", "merge", "modem", "mount", "nodes", "parse", "paste", "patch", "pixel", "ports", "power", "print", "proxy", "query", "radio", "regex",
	"reset", "route", "scope", "serve", "setup", "share", "shell", "solid", "sound", "speed", "spell", "stack", "start", "store", "style", "table", "theme",
	"throw", "timer", "token", "tower", "trace", "trash", "trust", "users", "video", "virus", "watts", "agent", "agents", "claude", "openai", "jules", "cursor",
	"cline", "qwencode", "claudecode", "editor", "terminal", "sketch", "webterm", "daemon", "server", "client", "remote", "session", "tunnel", "bridge", "exedev",
	"shelley", "secret", "secrets", "environ", "variable", "variables", "metadata", "cloudinit", "firewall", "config", "network", "networks", "database", "configure",
	"gateway", "router", "switch", "firewall", "cluster", "docker", "podman", "kubernetes", "helm", "ansible", "terraform", "vagrant", "puppet", "consul", "vault",
	"nomad", "etcd", "redis", "nginx", "apache", "traefik", "envoy", "istio", "linkerd", "cilium", "weave", "calico", "flannel", "zookeeper", "kafka", "rabbit",
	"zeromq", "websocket", "telnet", "rsync", "netcat", "socat", "screen", "byobu", "mosh", "tmate", "gotty", "ttyd", "shellinabox", "wetty", "xterm", "xtermjs",
	"monaco", "codemirror", "ace", "vscode", "neovim", "emacs", "sublime", "atom", "bracket", "theia", "gitpod", "codespace", "replit", "sandbox", "container",
	"chroot", "namespace", "cgroup", "systemd", "upstart", "supervisor", "monit", "circus", "gunicorn", "uwsgi", "passenger", "puma", "unicorn", "process",
	"thread", "worker", "queue", "scheduler", "crontab", "systemctl", "service", "socket", "target", "volume", "overlay", "union", "btrfs", "iptables", "netfilter",
	"fail2ban", "selinux", "apparmor", "grsec", "hardening", "syslog", "journald", "rsyslog", "fluentd", "logstash", "filebeat", "prometheus", "grafana", "influx",
	"telegraf", "collectd", "nagios", "zabbix", "sensu", "datadog", "newrelic", "splunk", "elastic", "kibana", "jaeger", "zipkin", "opentracing", "honeycomb",
	"lightstep", "wavefront", "signalfx", "vibes", "awesome", "panel", "adminpanel", "console", "dashboard", "settings", "config", "preferences", "options",
	"management", "control", "monitor", "viewer", "preview", "observability", "report", "analytics", "metric", "metrics", "stats", "endpoint", "identity", "oauth",
	"whoami", "profile", "username", "password", "passkey", "gitlab", "githost", "gitty", "jupyter", "notebook", "gerrit", "reviewboard", "zulip", "jitsi",
	"mastodon", "nextcloud", "owncloud", "seafile", "alertmanager", "jenkins", "philz", "buildbot", "drone", "gitea", "forgejo", "sourcehut", "mattermost",
	"rocketchat", "element", "discourse", "flarum", "nodebb", "wikijs", "bookstack", "outline", "jellyfin", "plex", "emby", "homeassistant", "openhab", "domoticz",
	"bitwarden", "vaultwarden", "keepass", "immich", "photoprism", "piwigo", "pihole", "adguard", "unbound", "wireguard", "openvpn", "tailscale", "caddy",
	"haproxy", "portainer", "rancher", "k3s", "minio", "rclone", "syncthing", "ghost", "strapi", "directus", "supabase", "appwrite", "pocketbase", "invoiceninja",
	"crater", "akaunting", "nodered", "huginn", "box-name", "new-link", "test-name", "invite", "unlink", "source-port", "target-port", "ssh-port", "admin-user",
	"admin-name", "admin-login", "user-name", "user-login", "user-pass", "dev-user", "dev-name", "dev-login", "dev-pass", "demo-user", "demo-name", "demo-login",
	"demo-pass", "test-user", "test-login", "test-pass", "example", "examples", "sample", "samples", "foobar", "foo-bar", "bar-foo", "hello", "world",
	"hello-world", "lorem", "ipsum", "lorem-ipsum", "access-level", "priority", "read-only", "readwrite", "path-prefix", "subdomain", "two-factor", "twofactor",
	"multi-factor", "multifactor", "mfa-required", "ssh-key", "ssh-keys", "sshkey", "sshkeys", "ssh-access", "sshaccess", "ssh-login", "sshlogin", "sshport",
	"ssh-user", "sshuser", "ssh-host", "sshhost", "ssh-hostname", "sshhostname", "ssh-identity", "sshidentity", "ssh-auth", "sshauth", "ssh-authentication",
	"sshauthentication", "ssh-agent", "sshagent", "ssh-config", "sshconfig", "ssh-command", "sshcommand", "ssh-connection", "sshconnection", "ssh-tunnel",
	"sshtunnel", "ssh-forward", "sshforward", "ssh-forwarding", "sshforwarding", "ssh-session", "sshsession", "ssh-socket", "sshsocket", "ssh-agent-forward",
	"sshagentforward", "ssh-agent-forwarding", "sshagentforwarding", "ssh-keygen", "sshkeygen", "ssh-copy-id", "sshcopyid", "ssh-add", "sshadd",
	"boxname", "box-name", "boxnames", "box-names", "my-box", "mybox", "your-box", "yourbox", "our-box", "ourbox",
	"vm-name", "vmname", "vm-names", "vmnames", "my-vm", "myvm", "your-vm", "yourvm", "our-vm", "ourvm",
	"mail",     // reserved for mail server subdomain (mail.exe.xyz)
	"team-int", // reserved for *.team-int.exe.xyz integration proxy domain
}

var JobsRelated = []string{"job", "jobs", "career", "careers", "apply", "work", "position", "positions", "opening", "openings", "hire", "hiring", "role", "roles", "join"}

var denylist map[string]bool

func init() {
	denylist = make(map[string]bool)
	for _, name := range reserved {
		denylist[name] = true
	}
	for _, name := range JobsRelated {
		denylist[name] = true
	}
}

var (
	// errInvalidNameFormat is the default error message returned for routine invalid vm names.
	// The aim is to helpfully guide users to pick valid names.
	errInvalidNameFormat = errors.New("invalid VM name, must be 5-52 characters: start with a lowercase letter, then lowercase letters or digits, with optional single hyphen separators (e.g., a-vm-name)")
	// errUnavailableName is the error returned for vm names that are denylisted or reserved.
	// It is intentionally ambiguous.
	errUnavailableName = errors.New("invalid VM name: this VM name is not available")
)

// reservedSuffixRE holds suffix rejection patterns.
// Names ending with -NNN, -pNNN, or -portNNN are reserved for possible future port signifier suffixes.
var reservedSuffixRE = regexp.MustCompile(`-(p|port)?[0-9]+$`)

// reservedFullNameRE holds full name rejection patterns.
// p80, p8080, port8080, port9000 etc are reserved for possible future port subdomains.
var reservedFullNameRE = regexp.MustCompile(`^(p|port)[0-9]*$`)

// reservedShardRE reserves infrastructure shard names (na0001, na12345, etc).
var reservedShardRE = regexp.MustCompile(`^na[0-9]+$`)

// nameFormatRE matches valid box name format:
// starts with letter, contains only lowercase letters/numbers/hyphens, no consecutive hyphens, doesn't end with hyphen
var nameFormatRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsValid reports whether name is a valid box name.
func IsValid(name string) bool {
	return Valid(name) == nil
}

// Valid returns nil if name is a valid box name, or an error describing why not.
func Valid(name string) error {
	if len(name) < 5 {
		return errInvalidNameFormat
	}
	// Max length is 52 (not the DNS label limit of 63) so we can safely
	// append "-port12345" (11 chars) if we implement port-specific subdomains.
	if len(name) > 52 {
		return errInvalidNameFormat
	}

	// Check format first so we give a good error for garbage input
	if !nameFormatRE.MatchString(name) {
		return errInvalidNameFormat
	}

	// Check denylist and reserved substrings.
	withoutHyphens := strings.ReplaceAll(name, "-", "")
	if denylist[withoutHyphens] {
		return errUnavailableName
	}
	for _, denied := range denySubstrings {
		if strings.Contains(withoutHyphens, denied) {
			return errUnavailableName
		}
	}

	// Reject names ending with -NNN, -pNNN, or -portNNN (reserved for port signifiers)
	if reservedSuffixRE.MatchString(name) {
		return errUnavailableName
	}

	// Reject names that are entirely reserved patterns (e.g., p, p80, p8080)
	if reservedFullNameRE.MatchString(name) {
		return errUnavailableName
	}

	// Reject infrastructure shard names (na0001, na12345, etc)
	if reservedShardRE.MatchString(name) {
		return errUnavailableName
	}

	return nil
}

var words = []string{
	// NATO phonetic + military
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
	"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
	"uniform", "victor", "whiskey", "xray", "yankee", "zulu",

	// WWII / older phonetics
	"able", "baker", "dog", "early", "waltz", "george", "how", "item", "jig", "king", "love", "nan",
	"oboe", "prep", "queen", "roger", "sweet", "tare", "uncle", "victory", "william", "extra",
	"yolk", "zebra",

	// Nature & elements
	"earth", "wind", "fire", "water", "stone", "tree", "river", "mountain", "cloud", "storm",
	"rain", "snow", "ice", "sun", "moon", "star", "comet", "nova", "eclipse", "ocean", "tide",
	"sky", "oak", "maple", "pine", "cedar", "willow", "elm", "spruce", "fir",

	// Animals
	"lion", "tiger", "bear", "wolf", "eagle", "hawk", "falcon", "owl", "otter", "seal", "whale",
	"shark", "orca", "salmon", "trout", "crane", "heron", "sparrow", "crow", "raven", "fox",
	"badger", "ferret", "bird", "bobcat", "cougar", "panther", "cobra", "viper", "python", "gecko",

	// Colors
	"red", "blue", "green", "yellow", "purple", "violet", "indigo", "orange", "egg", "ruby",
	"gray", "silver", "gold", "bronze", "scarlet", "crimson", "azure", "emerald", "jade", "amber",

	// Space & science
	"asteroid", "nebula", "quasar", "galaxy", "pulsar", "orbit", "photon", "quantum", "fusion",
	"plasma", "tin", "quark", "meteor", "cosmos", "ion", "neutron", "proton", "electron",

	// Tools, tech & retro computing
	"format", "disk", "edit", "finder", "paint", "minesweeper", "fortune", "lynx", "telnet",
	"gopher", "ping", "traceroute", "router", "switch", "ethernet", "socket", "kernel", "patch",
	"compile", "linker", "loader", "buffer", "cache", "cookie", "daemon", "popcorn", "driver",

	// Random objects
	"anchor", "beacon", "bridge", "compass", "harbor", "island", "lagoon", "mesa", "valley",
	"desert", "canyon", "fun", "reef", "stream", "dune", "grove", "peak", "ridge", "plateau",

	// Misc “fun” filler
	"sphinx", "obelisk", "party", "griffin", "hydra", "kraken", "unicorn", "pegasus", "chimera",
	"golem", "spin", "road", "alley", "sprite", "fairy", "dragon", "wyvern", "cyclops", "satyr", "noon",
	"centaur", "minotaur", "harp", "basilisk", "leviathan",
}

// Random generates a random box name.
func Random() string {
	word1 := words[mathrand.Intn(len(words))]
	word2 := words[mathrand.Intn(len(words))]

	// Ensure we don't get the same word twice
	for word1 == word2 {
		word2 = words[mathrand.Intn(len(words))]
	}

	return word1 + "-" + word2
}
