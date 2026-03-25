package deploy

// Recipe defines how to build, deploy, and verify a process type.
// Each process type (exeletd, exeprox, etc.) has one Recipe entry.
type Recipe struct {
	// BuildTarget is the Go package to build (e.g. "./cmd/exeletd").
	BuildTarget string

	// BinaryName is the output binary name on the target machine.
	// Versioned copies use "<BinaryName>.<timestamp>-<sha>" naming.
	BinaryName string

	// SymlinkName is the symlink to update after upload.
	// Defaults to BinaryName if empty. Set to "-" to skip symlinking.
	SymlinkName string

	// BuildDir is the subdirectory within the repo checkout to use as the
	// working directory for go build. Empty means the repo root.
	BuildDir string

	// RemoteDir is the directory on target machines where binaries live.
	RemoteDir string

	// RemoteUser is the SSH user for the target machine (default "ubuntu").
	RemoteUser string

	// ServiceUnit is the systemd unit to restart after deploy.
	ServiceUnit string

	// HealthPort is the port to check for liveness after restart.
	HealthPort int

	// HealthPath is the HTTP path that should return the running git SHA.
	// Empty means skip verification.
	HealthPath string

	// HealthTLS uses HTTPS for the health check when true.
	HealthTLS bool

	// CGO enables CGo for the build (CGO_ENABLED=1). When false
	// (the default), builds use CGO_ENABLED=0 for static binaries.
	CGO bool

	// PreBuildCmds are shell commands to run in the workdir before
	// go build (e.g. building embedded assets). Each entry is passed
	// to "bash -c". GOOS/GOARCH/CGO_ENABLED are set to the target.
	PreBuildCmds []string

	// PreRestartCmds are shell commands to run on the remote host
	// (via SSH) after install but before restarting the service.
	// Commonly used for database backups. Each entry is passed to
	// "bash -c" on the remote host.
	PreRestartCmds []string
}

func (r Recipe) symlinkName() string {
	if r.SymlinkName != "" {
		return r.SymlinkName
	}
	return r.BinaryName
}

func (r Recipe) remoteUser() string {
	if r.RemoteUser != "" {
		return r.RemoteUser
	}
	return "ubuntu"
}

// prodDeployAllowed reports whether a process is allowed to be deployed
// to prod-stage hosts. Processes not in this set require staging first.
func prodDeployAllowed(process string) bool {
	switch process {
	case "metricsd", "cgtop":
		return true
	}
	return false
}

// Recipes maps process name to its deploy recipe.
var Recipes = map[string]Recipe{
	"exeletd": {
		BuildTarget: "./cmd/exelet",
		BinaryName:  "exeletd",
		SymlinkName: "exeletd.latest",
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "exelet.service",
		HealthPort:  9081,
		HealthPath:  "/debug/gitsha",
		PreBuildCmds: []string{
			"make exelet-fs exe-init",
		},
	},
	"cgtop": {
		BuildTarget: "./cmd/cgtop",
		BinaryName:  "cgtop",
		RemoteDir:   "/usr/local/bin",
		ServiceUnit: "cgtop.service",
		HealthPort:  9090,
		HealthPath:  "/debug/gitsha",
	},
	"exeprox": {
		BuildTarget: "./cmd/exeprox",
		BinaryName:  "exeprox",
		SymlinkName: "-", // service uses ls -t to find newest binary
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "exeprox.service",
		HealthPort:  443,
		HealthPath:  "/debug/gitsha",
		HealthTLS:   true,
	},
	"exed": {
		BuildTarget: "./cmd/exed",
		BinaryName:  "exed",
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "exed.service",
		HealthPort:  443,
		HealthPath:  "/debug/gitsha",
		HealthTLS:   true,
		PreBuildCmds: []string{
			"make ui",
		},
		PreRestartCmds: []string{
			`sqlite3 ~/exe.db .dump | zstd -o ~/exe.db.$(date +%Y%m%d-%H%M%S).sql.zst`,
		},
	},
	"metricsd": {
		BuildTarget: "./cmd/metricsd",
		BinaryName:  "metricsd",
		CGO:         true,
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "metricsd.service",
		HealthPort:  21090,
		HealthPath:  "/debug/gitsha",
	},
	"exe-ops": {
		BuildTarget: "./cmd/exe-ops-server",
		BuildDir:    "exe-ops",
		BinaryName:  "exe-ops-server",
		RemoteDir:   "/opt/exe-ops/bin",
		ServiceUnit: "exe-ops-server.service",
		HealthPort:  443,
		HealthPath:  "/debug/gitsha",
		HealthTLS:   true,
		PreBuildCmds: []string{
			"make build-ui",
		},
	},
}
