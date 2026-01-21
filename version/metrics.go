package version

import (
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"
)

var gitBuildInfo = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "git_build_info",
		Help: "Git build information.",
	},
	[]string{"commit"},
)

func init() {
	commit := "unknown"
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range buildInfo.Settings {
			if setting.Key == "vcs.revision" {
				commit = setting.Value
				if len(commit) > 7 {
					commit = commit[:7]
				}
				break
			}
		}
	}
	gitBuildInfo.WithLabelValues(commit).Set(1)
}

// RegisterBuildInfo registers the git_build_info metric with
// the given prometheus registry.
func RegisterBuildInfo(registry *prometheus.Registry) {
	registry.MustRegister(gitBuildInfo)
}
