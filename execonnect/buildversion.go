package execonnect

import "runtime/debug"

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
