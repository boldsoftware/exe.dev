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

	// PreflightCmds are shell commands to run on the remote host
	// after backup but before restart. Used for migration preflight
	// checks. Template variables: {binary} = remote binary path,
	// {stage} = deploy stage. Each entry is passed to "bash -c".
	PreflightCmds []string

	// ServiceFiles maps deploy stage to the repo-relative path of the
	// systemd service file to install. If a stage is not present in the
	// map but a "" (empty string) key exists, that entry is used as a
	// fallback for all stages. When nil or empty, the service file step
	// is skipped.
	ServiceFiles map[string]string
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
	case "metricsd", "cgtop", "exeletd", "exed", "exeprox", "exepipe", "exe-ops":
		return true
	}
	return false
}

// serviceFile returns the repo-relative path to the systemd service file
// for the given stage, or "" if no service file is configured.
func (r Recipe) serviceFile(stage string) string {
	if len(r.ServiceFiles) == 0 {
		return ""
	}
	if p, ok := r.ServiceFiles[stage]; ok {
		return p
	}
	return r.ServiceFiles[""]
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
		ServiceFiles: map[string]string{
			"staging": "ops/deploy/exelet-staging.service",
			"prod":    "ops/deploy/exelet-prod.service",
			"global":  "ops/deploy/exelet-prod.service",
		},
	},
	"cgtop": {
		BuildTarget: "./cmd/cgtop",
		BinaryName:  "cgtop",
		SymlinkName: "cgtop",
		RemoteDir:   "/usr/local/bin",
		ServiceUnit: "cgtop.service",
		HealthPort:  9090,
		HealthPath:  "/debug/gitsha",
		ServiceFiles: map[string]string{
			"": "ops/deploy/cgtop.service",
		},
	},
	"exeprox": {
		BuildTarget: "./cmd/exeprox",
		BinaryName:  "exeprox",
		// The service picks the newest binary via "ls -t exeprox.* | head -n1".
		// We still create exeprox.latest as a symlink so that rollback works
		// by simply `ln -sf` to an older binary — that bumps the symlink's
		// mtime, which makes it the newest entry and routes exec through it.
		SymlinkName: "exeprox.latest",
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "exeprox.service",
		HealthPort:  443,
		HealthPath:  "/debug/gitsha",
		HealthTLS:   true,
		ServiceFiles: map[string]string{
			"staging": "ops/deploy/exeprox-staging.service",
			"prod":    "ops/deploy/exeprox-prod.service",
			"global":  "ops/deploy/exeprox-prod.service",
		},
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
		PreflightCmds: []string{
			`sqlite3 ~/exe.db ".backup /tmp/preflight.db" && {binary} --preflight --db /tmp/preflight.db --stage {stage}; rc=$?; rm -f /tmp/preflight.db; exit $rc`,
		},
		ServiceFiles: map[string]string{
			"staging": "ops/deploy/exed-staging.service",
			"prod":    "ops/deploy/exed-prod.service",
			"global":  "ops/deploy/exed-prod.service",
		},
	},
	"metricsd": {
		BuildTarget: "./cmd/metricsd",
		BinaryName:  "metricsd",
		SymlinkName: "metricsd.latest",
		CGO:         true,
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "metricsd.service",
		HealthPort:  21090,
		HealthPath:  "/debug/gitsha",
		ServiceFiles: map[string]string{
			"staging": "ops/deploy/metricsd-staging.service",
			"prod":    "ops/deploy/metricsd-prod.service",
			"global":  "ops/deploy/metricsd-prod.service",
		},
	},
	"exepipe": {
		BuildTarget: "./cmd/exepipe",
		BinaryName:  "exepipe",
		// The service picks the newest binary via "ls -t exepipe.* | head -n1".
		// exepipe.latest symlink exists so that rollback works by `ln -sf`ing
		// to an older binary — that bumps the symlink's mtime, making it the
		// newest entry and routing exec through it (same pattern as exeprox).
		SymlinkName: "exepipe.latest",
		RemoteDir:   "/home/ubuntu",
		ServiceUnit: "exepipe.service",
		HealthPort:  30304,
		HealthPath:  "/debug/gitsha",
		ServiceFiles: map[string]string{
			"staging": "ops/deploy/exepipe-staging.service",
			"prod":    "ops/deploy/exepipe-prod.service",
			"global":  "ops/deploy/exepipe-prod.service",
		},
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
		ServiceFiles: map[string]string{
			"": "exe-ops/ops/exe-ops-server.service",
		},
	},
}
