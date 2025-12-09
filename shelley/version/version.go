package version

import (
	"runtime/debug"
)

// Info holds build information from runtime/debug.ReadBuildInfo
type Info struct {
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Modified   bool   `json:"modified,omitempty"`
}

// GetInfo returns build information using runtime/debug.ReadBuildInfo
func GetInfo() Info {
	var info Info

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	for _, setting := range buildInfo.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Commit = setting.Value
		case "vcs.time":
			info.CommitTime = setting.Value
		case "vcs.modified":
			info.Modified = setting.Value == "true"
		}
	}

	return info
}
