package version

// These variables are set at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Full returns a human-readable version string including build date.
func Full() string {
	v := Version
	if Date != "unknown" {
		v += " built " + Date
	}
	return v
}
