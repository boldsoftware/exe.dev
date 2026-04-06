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
		if !p.AllowsDirectSSH() {
			t.Error("nil should allow direct SSH")
		}
	})
}

func TestAllowsCommand(t *testing.T) {
	t.Parallel()

	t.Run("nil_perms", func(t *testing.T) {
		var p *SSHKeyPerms
		if !p.AllowsCommand("anything") {
			t.Error("nil perms should allow any command")
		}
	})

	t.Run("exact_match", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ls", "whoami"}}
		if !p.AllowsCommand("ls") {
			t.Error("ls should be allowed")
		}
		if !p.AllowsCommand("whoami") {
			t.Error("whoami should be allowed")
		}
		if p.AllowsCommand("ssh") {
			t.Error("ssh should not be allowed")
		}
	})

	t.Run("parent_allows_subcommands", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ssh-key", "ls"}}
		if !p.AllowsCommand("ssh-key add") {
			t.Error("ssh-key should allow ssh-key add")
		}
		if !p.AllowsCommand("ssh-key list") {
			t.Error("ssh-key should allow ssh-key list")
		}
		if !p.AllowsCommand("ssh-key remove") {
			t.Error("ssh-key should allow ssh-key remove")
		}
		if !p.AllowsCommand("ssh-key") {
			t.Error("ssh-key should allow ssh-key itself")
		}
	})

	t.Run("parent_does_not_match_partial", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ssh"}}
		// "ssh" should not match "ssh-key add" (different command, not a subcommand).
		if p.AllowsCommand("ssh-key add") {
			t.Error("ssh should not match ssh-key add")
		}
		if !p.AllowsCommand("ssh") {
			t.Error("ssh should match ssh")
		}
	})

	t.Run("empty_resolved_cmd", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ls"}}
		if p.AllowsCommand("") {
			t.Error("empty command should not be allowed")
		}
	})
}

func TestAllowsDirectSSH(t *testing.T) {
	t.Parallel()

	t.Run("nil_perms", func(t *testing.T) {
		var p *SSHKeyPerms
		if !p.AllowsDirectSSH() {
			t.Error("nil perms should allow direct SSH")
		}
	})

	t.Run("no_cmds_restriction", func(t *testing.T) {
		p := &SSHKeyPerms{VM: "some-vm"}
		if !p.AllowsDirectSSH() {
			t.Error("perms with no cmds restriction should allow direct SSH")
		}
	})

	t.Run("cmds_includes_ssh", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ssh"}}
		if !p.AllowsDirectSSH() {
			t.Error("cmds=[ssh] should allow direct SSH")
		}
	})

	t.Run("cmds_includes_ssh_among_others", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ls", "ssh", "whoami"}}
		if !p.AllowsDirectSSH() {
			t.Error("cmds=[ls,ssh,whoami] should allow direct SSH")
		}
	})

	t.Run("cmds_without_ssh", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"new", "ls", "whoami"}}
		if p.AllowsDirectSSH() {
			t.Error("cmds=[new,ls,whoami] should NOT allow direct SSH")
		}
	})

	t.Run("cmds_empty_slice", func(t *testing.T) {
		// An empty cmds slice means "no commands allowed" (but this case
		// is normalized to nil by parseSSHKeyPerms). Test the raw struct.
		p := &SSHKeyPerms{Cmds: []string{}}
		if p.AllowsDirectSSH() {
			t.Error("empty cmds should NOT allow direct SSH")
		}
	})
}
