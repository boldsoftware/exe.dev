// Package exescroll embeds the exe-scroll session multiplexer and runs it
// without installing it: from a sealed memfd on Linux, from a verified copy in
// the user cache directory on macOS.
//
// `make` downloads a release binary for the build target into binary/, with
// its tag (see fetch); a build without it compiles, but Exec fails.
package exescroll

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed all:binary
var binaries embed.FS

var (
	name              = "binary/exe-scroll-" + runtime.GOOS + "-" + runtime.GOARCH
	embeddedBinary, _ = binaries.ReadFile(name)
	embeddedTag, _    = binaries.ReadFile(name + ".tag")
)

// Embedded reports whether exe-scroll is embedded for this platform.
func Embedded() bool { return len(embeddedBinary) > 0 }

// Release is the tag of the embedded exe-scroll release, if known.
func Release() string { return strings.TrimSpace(string(embeddedTag)) }

// ConfigureCommand arranges for a child process that runs Exec to reuse this
// process's copy of exe-scroll where the platform supports it.
func ConfigureCommand(cmd *exec.Cmd) error {
	return configureCommand(cmd)
}

// Exec replaces the current process with the embedded exe-scroll binary.
func Exec(args []string) error {
	if len(embeddedBinary) == 0 {
		return fmt.Errorf("exe-scroll is not embedded for this platform")
	}
	argv := append([]string{"exe-scroll"}, args...)
	return execEmbedded(embeddedBinary, argv, os.Environ())
}

func materialize(data []byte, root string) (string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := filepath.Join(root, digest)
	path := filepath.Join(dir, "exe-scroll")
	if got, err := os.ReadFile(path); err == nil && sha256.Sum256(got) == sha256.Sum256(data) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create exe-scroll cache: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure exe-scroll cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "exe-scroll-*")
	if err != nil {
		return "", fmt.Errorf("create cached exe-scroll: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write cached exe-scroll: %w", err)
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return "", fmt.Errorf("chmod cached exe-scroll: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("sync cached exe-scroll: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close cached exe-scroll: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if got, readErr := os.ReadFile(path); readErr == nil && sha256.Sum256(got) == sha256.Sum256(data) {
			return path, nil
		}
		return "", fmt.Errorf("publish cached exe-scroll: %w", err)
	}
	return path, nil
}
