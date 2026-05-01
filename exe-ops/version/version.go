package version

import (
	"runtime/debug"
	"sync"
)

// These variables are set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Full returns a human-readable version string including build date.
func Full() string {
	version, _, date := Resolved()
	v := version
	if date != "unknown" && date != "" {
		v += " built " + date
	}
	return v
}

var (
	resolvedOnce                                  sync.Once
	resolvedVersion, resolvedCommit, resolvedDate string
)

// Resolved returns the running version, commit SHA, and build date.
//
// Values come from -ldflags first (set by the deploy script and Makefile);
// any field still at its zero placeholder ("dev"/"unknown") is recovered
// from runtime/debug.BuildInfo, which Go populates from the surrounding git
// checkout when the binary is built from a VCS-tracked tree. This means a
// binary built without -ldflags (e.g. `go run`, the bash bootstrap script,
// or a stock `go build`) still knows its own commit, so the UI can detect
// when a self-deploy has finished restarting.
func Resolved() (version, commit, date string) {
	resolvedOnce.Do(func() {
		resolvedVersion = Version
		resolvedCommit = Commit
		resolvedDate = Date
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		var vcsRev, vcsTime string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				vcsRev = s.Value
			case "vcs.time":
				vcsTime = s.Value
			}
		}
		if (resolvedCommit == "" || resolvedCommit == "unknown") && vcsRev != "" {
			resolvedCommit = vcsRev
		}
		if (resolvedDate == "" || resolvedDate == "unknown") && vcsTime != "" {
			resolvedDate = vcsTime
		}
		if (resolvedVersion == "" || resolvedVersion == "dev") && len(vcsRev) >= 12 {
			resolvedVersion = vcsRev[:12]
		}
	})
	return resolvedVersion, resolvedCommit, resolvedDate
}
