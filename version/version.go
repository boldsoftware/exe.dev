package version

import (
	"runtime/debug"
)

const (
	Name        = "exe"
	Description = "VMs for everyone"
)

// BuildVersion returns the git commit hash.
func BuildVersion() string {
	return gitCommit()
}

// FullVersion returns "exe/1f4a07dc"
func FullVersion() string {
	return Name + "/" + BuildVersion()
}

func gitCommit() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.revision" {
				if len(setting.Value) > 7 {
					return setting.Value[:7]
				}
				return setting.Value
			}
		}
	}
	return "unknown"
}
