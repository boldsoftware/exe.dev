package execonnect

import "runtime/debug"

// Version is the release version. release.sh sets it at link time.
var Version string

// BuildVersion returns the release version and Git revision, or just the
// revision for unreleased builds.
func BuildVersion() string {
	if Version == "" {
		return BuildRevision()
	}
	return Version + " (" + BuildRevision() + ")"
}

// BuildRevision returns the connector's abbreviated Git revision.
func BuildRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				if len(setting.Value) > 7 {
					return setting.Value[:7]
				}
				if setting.Value != "" {
					return setting.Value
				}
			}
		}
	}
	return "unknown"
}
