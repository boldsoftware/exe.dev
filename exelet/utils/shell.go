package utils

import (
	"fmt"
	"os"
	"os/exec"
)

var shells = []string{"ash", "bash", "dash", "zsh", "csh", "sh"}

// GetShellPath returns the full path to the first resolved shell
func GetShellPath() (string, error) {
	for _, s := range shells {
		binPath, err := exec.LookPath(s)
		if err != nil {
			continue
		}
		// ensure exists
		if _, err := os.Stat(binPath); err != nil {
			continue
		}
		return binPath, nil
	}

	return "", fmt.Errorf("unable to resolve shell for environment: %w", ErrNotFound)
}
