package execore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestDropPageCacheCommandShape locks in the in-VM command shape for
// both auth paths.
//
// dropPageCacheOnBox tries SSH-as-root first (no sudo needed) and falls
// back to SSH-as-login-user + sudo when root pubkey auth fails. The
// fallback path is what saves us on VMs created before commit
// af35594b ("exe-init: keep authorized_keys owned by root for
// multi-user SSH"): those VMs have /exe.dev/etc/ssh/authorized_keys
// chowned to the login user, and OpenSSH StrictModes refuses to read
// it for root, producing
// "ssh: unable to authenticate, attempted methods [none publickey]".
//
// If you change either command, please make sure both auth modes still
// drop page caches and emit the --DROP-- marker.
func TestDropPageCacheCommandShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		command  string
		wantSudo bool
	}{
		{"root", dropPageCacheCommandRoot, false},
		{"sudo", dropPageCacheCommandSudo, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.command, "/proc/sys/vm/drop_caches") {
				t.Errorf("command must write to drop_caches, got: %q", tc.command)
			}
			if !strings.Contains(tc.command, "--DROP--") {
				t.Errorf("command must emit --DROP-- marker, got: %q", tc.command)
			}
			if got := strings.Contains(tc.command, "sudo"); got != tc.wantSudo {
				t.Errorf("sudo presence = %v, want %v: %q", got, tc.wantSudo, tc.command)
			}
		})
	}
}

func TestIsSSHAuthError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection refused"), false},
		{"timeout", errors.New("i/o timeout"), false},
		{
			"observed handshake failure",
			errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"),
			true,
		},
		{
			"wrapped",
			fmt.Errorf("failed to dial host: %w", errors.New("ssh: unable to authenticate")),
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHAuthError(tc.err); got != tc.want {
				t.Errorf("isSSHAuthError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestParseDropPageCacheOutput exercises parseDropPageCacheOutput with a
// canned before/after /proc/meminfo blob.
func TestParseDropPageCacheOutput(t *testing.T) {
	out := []byte(strings.Join([]string{
		"MemTotal:        995696 kB",
		"MemFree:         100000 kB",
		"MemAvailable:    200000 kB",
		"Cached:           50000 kB",
		"--DROP--",
		"MemTotal:        995696 kB",
		"MemFree:         180000 kB",
		"MemAvailable:    250000 kB",
		"Cached:           10000 kB",
		"",
	}, "\n"))
	res := parseDropPageCacheOutput(out)
	const kB = 1024
	if got, want := res.MemFreeBeforeBytes, int64(100000*kB); got != want {
		t.Errorf("MemFreeBefore = %d, want %d", got, want)
	}
	if got, want := res.MemFreeAfterBytes, int64(180000*kB); got != want {
		t.Errorf("MemFreeAfter = %d, want %d", got, want)
	}
	if got, want := res.MemFreeDeltaBytes, int64(80000*kB); got != want {
		t.Errorf("MemFreeDelta = %d, want %d", got, want)
	}
	if got, want := res.CachedBeforeBytes, int64(50000*kB); got != want {
		t.Errorf("CachedBefore = %d, want %d", got, want)
	}
	if got, want := res.CachedAfterBytes, int64(10000*kB); got != want {
		t.Errorf("CachedAfter = %d, want %d", got, want)
	}
}
