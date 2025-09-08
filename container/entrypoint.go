package container

import (
	"encoding/json"
	"fmt"
	"strings"

	"exe.dev/tagresolver"
)

// parseImageInspectJSON parses nerdctl/docker image inspect JSON.
// It tolerates both an array of objects (normal) and a single object.
func parseImageInspectJSON(data []byte) (tagresolver.ImageConfig, error) {
	// Define the subset of fields we care about
	type cfg struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
			Cmd        []string `json:"Cmd"`
			User       string   `json:"User"`
		} `json:"Config"`
	}

	// Try array form first
	var arr []cfg
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		c := arr[0].Config
		return tagresolver.ImageConfig{Entrypoint: c.Entrypoint, Cmd: c.Cmd, User: c.User}, nil
	}

	// Try single object form
	var single cfg
	if err := json.Unmarshal(data, &single); err == nil {
		c := single.Config
		return tagresolver.ImageConfig{Entrypoint: c.Entrypoint, Cmd: c.Cmd, User: c.User}, nil
	}

	return tagresolver.ImageConfig{}, fmt.Errorf("unrecognized inspect JSON format")
}

// buildEntrypointAndCmdArgs builds args to append after the image reference in nerdctl run.
// When useExetini is true, returns exetini args (e.g., -g -- ...) with the right command.
// Otherwise, returns command args or empty to use image defaults.
func buildEntrypointAndCmdArgs(useExetini bool, override string, imageEntrypoint, imageCmd []string) []string {
	// Normalize override values
	ov := strings.TrimSpace(override)

	// Helper to prefix tini flags when we have a command to run under exetini
	exetiniPrefix := func(cmdParts []string) []string {
		if !useExetini {
			return cmdParts
		}
		return append([]string{"-g", "--"}, cmdParts...)
	}

	// Custom override provided (and not auto/none)
	if ov != "" && ov != "auto" && ov != "none" {
		parts := strings.Fields(ov)
		return exetiniPrefix(parts)
	}

	if useExetini {
		// Prefer image entrypoint + cmd when available
		if len(imageEntrypoint) > 0 || len(imageCmd) > 0 {
			cmd := append([]string{}, imageEntrypoint...)
			cmd = append(cmd, imageCmd...)
			return exetiniPrefix(cmd)
		}
		// Fallback to keeping container alive
		return exetiniPrefix([]string{"sleep", "infinity"})
	}

	// Not using exetini
	if ov == "none" {
		// Ensure the container stays up to allow SSH setup
		return []string{"sleep", "infinity"}
	}

	// No override; rely on image defaults
	return nil
}
