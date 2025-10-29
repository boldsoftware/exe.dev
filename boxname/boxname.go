package boxname

import (
	mathrand "math/rand"
	"regexp"
	"strings"
)

// See what happens when these are actively ignored:
// https://www.google.com/search?q=ollama+adderall
var drugspam = []string{
	"adderall", "ambien", "antidepressant", "antiviral", "antivirus", "anxiety", "bipolar", "cialis", "depression",
	"erectile", "fatigue", "fibromyalgia", "gabapentin", "herpes", "hiv", "insomnia", "levitra", "lupus",
	"melatonin", "narcotic", "opioid", "oxycodone", "painkiller", "parkinson", "pharmacy", "prescription",
	"prozac", "sildenafil", "sleepaid", "tramadol", "viagra", "xanax", "zolpidem",
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
}

var JobsRelated = []string{"job", "jobs", "career", "careers", "apply", "work", "position", "positions", "opening", "openings", "hire", "hiring", "role", "roles", "join"}

var denylisted map[string]bool

func init() {
	denylisted = make(map[string]bool)
	for _, name := range reserved {
		denylisted[name] = true
	}
	for _, name := range JobsRelated {
		denylisted[name] = true
	}
}

const InvalidBoxNameMessage = "Invalid box name. Must be 5–64 characters: start with a lowercase letter, then lowercase letters or digits, with optional single hyphen separators (e.g., a-box-name)."

// Valid reports whether name is a valid box name.
// TODO: return a slice of validation errors instead of just true/false.
func Valid(name string) bool {
	// Must be at least 5 characters and at most 64 characters
	if len(name) < 5 || len(name) > 64 {
		return false
	}

	for _, drug := range drugspam {
		if strings.Contains(name, drug) {
			return false
		}
	}

	// Check pattern: starts with letter, contains only lowercase letters/numbers/hyphens, no consecutive hyphens, doesn't end with hyphen
	matched, _ := regexp.MatchString(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, name)
	return matched
}

// Denylisted reports whether name is in the denylist.
func Denylisted(name string) bool {
	_, exists := denylisted[name]
	return exists
}

// Random generates a random box name.
func Random() string {
	words := []string{
		// NATO phonetic + military
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu",

		// WWII / older phonetics
		"able", "baker", "dog", "easy", "fox", "george", "how", "item", "jig", "king", "love", "nan",
		"oboe", "prep", "queen", "roger", "sugar", "tare", "uncle", "victory", "william", "xray",
		"yolk", "zebra",

		// Nature & elements
		"earth", "wind", "fire", "water", "stone", "tree", "river", "mountain", "cloud", "storm",
		"rain", "snow", "ice", "sun", "moon", "star", "comet", "nova", "eclipse", "ocean", "tide",

		// Animals
		"lion", "tiger", "bear", "wolf", "eagle", "hawk", "falcon", "owl", "otter", "seal", "whale",
		"shark", "orca", "salmon", "trout", "crane", "heron", "sparrow", "crow", "raven", "fox",
		"badger", "ferret", "mole", "lynx", "cougar", "panther", "cobra", "viper", "python", "gecko",

		// Colors
		"red", "blue", "green", "yellow", "purple", "violet", "indigo", "orange", "white", "black",
		"gray", "silver", "gold", "bronze", "scarlet", "crimson", "azure", "emerald", "jade", "amber",

		// Space & science
		"asteroid", "nebula", "quasar", "galaxy", "pulsar", "orbit", "photon", "quantum", "fusion",
		"plasma", "nova", "eclipse", "meteor", "cosmos", "ion", "neutron", "proton", "electron",

		// Tools, tech & retro computing
		"format", "fdisk", "edit", "tree", "paint", "minesweeper", "fortune", "lynx", "telnet",
		"gopher", "ping", "traceroute", "router", "switch", "ethernet", "socket", "kernel", "patch",
		"compile", "linker", "loader", "buffer", "cache", "cookie", "daemon", "kernel", "driver",

		// Random objects
		"anchor", "beacon", "bridge", "compass", "harbor", "island", "lagoon", "mesa", "valley",
		"desert", "canyon", "fjord", "reef", "delta", "dune", "grove", "peak", "ridge", "plateau",

		// Misc “fun” filler
		"sphinx", "obelisk", "phoenix", "griffin", "hydra", "kraken", "unicorn", "pegasus", "chimera",
		"golem", "djinn", "troll", "sprite", "fairy", "dragon", "wyvern", "cyclops", "satyr", "nymph",
		"centaur", "minotaur", "harpy", "basilisk", "leviathan",
	}

	word1 := words[mathrand.Intn(len(words))]
	word2 := words[mathrand.Intn(len(words))]

	// Ensure we don't get the same word twice
	for word1 == word2 {
		word2 = words[mathrand.Intn(len(words))]
	}

	return word1 + "-" + word2
}
