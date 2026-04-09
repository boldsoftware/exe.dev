//go:build linux

package netns

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func writeSysctl(name, value string) error {
	path := fmt.Sprintf("/proc/sys/%s", strings.ReplaceAll(name, ".", "/"))
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, value)
	return err
}

// ensureIptablesRule checks if a rule exists (checkArgs) and adds it (addArgs) if not.
func ensureIptablesRule(ctx context.Context, checkArgs, addArgs []string) error {
	// -C returns 0 if rule exists, non-zero otherwise.
	if exec.CommandContext(ctx, "iptables", checkArgs...).Run() == nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "iptables", addArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %w (%s)", addArgs, err, out)
	}
	return nil
}
