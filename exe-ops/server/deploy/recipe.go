package deploy

// Recipe defines how to build, deploy, and verify a process type.
// Each process type (exeletd, exeprox, etc.) has one Recipe entry.
type Recipe struct {
	// BuildTarget is the Go package to build (e.g. "./cmd/exeletd").
	BuildTarget string

	// BinaryName is the output binary name on the target machine.
	// Versioned copies use "<BinaryName>.<timestamp>-<sha>" naming
	// with a "<BinaryName>.latest" symlink.
	BinaryName string

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

	// PreBuildCmds are shell commands to run in the workdir before
	// go build (e.g. building embedded assets). Each entry is passed
	// to "bash -c". GOOS/GOARCH/CGO_ENABLED are set to the target.
	PreBuildCmds []string
}

func (r Recipe) remoteUser() string {
	if r.RemoteUser != "" {
		return r.RemoteUser
	}
	return "ubuntu"
}

// Recipes maps process name to its deploy recipe.
var Recipes = map[string]Recipe{
	"exeletd": {
		BuildTarget: "./cmd/exelet",
		BinaryName:  "exeletd",
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
	},
	"metricsd": {
		BuildTarget: "./cmd/metricsd",
		BinaryName:  "metricsd",
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "metricsd.service",
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
			"make build-ui build-agent-embed",
		},
	},
}
