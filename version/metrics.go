package version

import (
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
	gitBuildInfo.WithLabelValues(gitCommit()).Set(1)
}

// RegisterBuildInfo registers the git_build_info metric with
// the given prometheus registry.
func RegisterBuildInfo(registry *prometheus.Registry) {
	registry.MustRegister(gitBuildInfo)
}
