package version

import (
	"fmt"
	"runtime"
)

var (
	// Name is the name of the application
	Name = "exe"

	// Version defines the application version
	Version = "next"

	// Description is the application description
	Description = "VMs for everyone"

	// Build will be overwritten automatically by the build system
	Build = "-dev"

	// Commit will be overwritten automatically by the build system
	Commit = "HEAD"
)

// BuildVersion returns the build version information including version, build and git commit
func BuildVersion() string {
	return Version + Build + " (" + Commit + ") " + runtime.GOOS + "/" + runtime.GOARCH
}

// FullVersion returns the build version information including version, build and git commit
func FullVersion() string {
	return Name + "/" + BuildVersion()
}

// ShortVersion returns the version and commit
func ShortVersion() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
