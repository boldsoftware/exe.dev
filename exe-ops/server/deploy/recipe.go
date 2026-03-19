package deploy

// Recipe defines how to build, deploy, and verify a process type.
// Each process type (exeletd, exeprox, etc.) has one Recipe entry.
type Recipe struct {
	// BuildTarget is the Go package to build (e.g. "./cmd/exeletd").
	BuildTarget string

	// BinaryName is the output binary name on the target machine.
	// Versioned copies use "<BinaryName>.<timestamp>-<sha>" naming
	// with a "<BinaryName>.latest" symlink, except for cgtop which
	// uses a direct overwrite at its install path.
	BinaryName string

	// RemoteDir is the directory on target machines where binaries live.
	RemoteDir string

	// ServiceUnit is the systemd unit to restart after deploy.
	ServiceUnit string

	// HealthPort is the port to check for liveness after restart.
	HealthPort int

	// HealthPath is the HTTP path that should return the running git SHA.
	// Empty means skip verification.
	HealthPath string

	// HealthTLS uses HTTPS for the health check when true.
	HealthTLS bool

	// DirectInstall means overwrite the binary in place instead of using
	// versioned copies + symlink (used for cgtop).
	DirectInstall bool

	// PreBuildCmds are shell commands to run in the workdir before
	// go build (e.g. building embedded assets). Each entry is passed
	// to "bash -c". GOOS/GOARCH/CGO_ENABLED are set to the target.
	PreBuildCmds []string
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
		BuildTarget:   "./cmd/cgtop",
		BinaryName:    "cgtop",
		RemoteDir:     "/usr/local/bin",
		ServiceUnit:   "cgtop.service",
		HealthPort:    9090,
		HealthPath:    "/debug/gitsha",
		DirectInstall: true,
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
}
