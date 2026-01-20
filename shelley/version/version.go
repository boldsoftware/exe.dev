package version

import (
	"encoding/json"
	"io/fs"
	"os"
	"runtime/debug"

	"shelley.exe.dev/ui"
)

// Version and Tag are set at build time via ldflags
var (
	Version = "dev"
	Tag     = ""
)

// Info holds build information from runtime/debug.ReadBuildInfo
type Info struct {
	Version    string `json:"version,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Modified   bool   `json:"modified,omitempty"`
}

// GetInfo returns build information using runtime/debug.ReadBuildInfo,
// falling back to the embedded build-info.json from the UI build.
// The SHELLEY_VERSION_OVERRIDE environment variable can override the tag for testing.
func GetInfo() Info {
	tag := Tag
	if override := os.Getenv("SHELLEY_VERSION_OVERRIDE"); override != "" {
		tag = override
	}

	info := Info{
		Version: Version,
		Tag:     tag,
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
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
	}

	// If we didn't get vcs info from debug.ReadBuildInfo, try the embedded build-info.json
	if info.Commit == "" {
		if data, err := fs.ReadFile(ui.Dist, "dist/build-info.json"); err == nil {
			var buildJSON struct {
				Commit     string `json:"commit"`
				CommitTime string `json:"commitTime"`
				Modified   bool   `json:"modified"`
			}
			if json.Unmarshal(data, &buildJSON) == nil {
				info.Commit = buildJSON.Commit
				info.CommitTime = buildJSON.CommitTime
				info.Modified = buildJSON.Modified
			}
		}
	}

	return info
}
