package execore

import (
	"testing"
	"time"
)

func TestParseSSHKeyPerms(t *testing.T) {
	t.Parallel()

	t.Run("empty_string", func(t *testing.T) {
		p, err := parseSSHKeyPerms("")
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected nil for empty string")
		}
	})

	t.Run("empty_object", func(t *testing.T) {
		p, err := parseSSHKeyPerms("{}")
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected nil for empty object")
		}
	})

	t.Run("cmds_only", func(t *testing.T) {
		p, err := parseSSHKeyPerms(`{"cmds":["ls","whoami"]}`)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("expected non-nil")
		}
		if !p.AllowsCommand("ls") {
			t.Error("should allow ls")
		}
		if !p.AllowsCommand("whoami") {
			t.Error("should allow whoami")
		}
		if p.AllowsCommand("rm") {
			t.Error("should not allow rm")
		}
	})

	t.Run("vm_only", func(t *testing.T) {
		p, err := parseSSHKeyPerms(`{"vm":"my-vm"}`)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("expected non-nil")
		}
		if !p.AllowsVM("my-vm") {
			t.Error("should allow my-vm")
		}
		if p.AllowsVM("other-vm") {
			t.Error("should not allow other-vm")
		}
		// No cmds restriction: all commands allowed.
		if !p.AllowsCommand("anything") {
			t.Error("should allow any command when cmds is nil")
		}
	})

	t.Run("exp_future", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour).Unix()
		p := &SSHKeyPerms{Exp: &future}
		if p.IsExpired() {
			t.Error("should not be expired")
		}
	})

	t.Run("exp_past", func(t *testing.T) {
		past := time.Now().Add(-24 * time.Hour).Unix()
		p := &SSHKeyPerms{Exp: &past}
		if !p.IsExpired() {
			t.Error("should be expired")
		}
	})

	t.Run("nil_perms_unrestricted", func(t *testing.T) {
		var p *SSHKeyPerms
		if p.IsExpired() {
			t.Error("nil should not be expired")
		}
		if !p.AllowsCommand("anything") {
			t.Error("nil should allow any command")
		}
		if !p.AllowsVM("any-vm") {
			t.Error("nil should allow any VM")
		}
	})
}
