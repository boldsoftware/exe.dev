package e1e

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestNewCommandTagScope(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, rootKey, email := registerForExeDev(t)
	pty.Disconnect()

	enableRootSupport(t, email)

	addScopedKey := func(t *testing.T, flags []string) string {
		t.Helper()
		keyPath, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("GenSSHKey: %v", err)
		}
		args := []string{"ssh-key", "add", "--json"}
		args = append(args, flags...)
		args = append(args, pubKey)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, args...)
		if err != nil {
			t.Fatalf("ssh-key add %v: %v\n%s", flags, err, out)
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse ssh-key add JSON: %v\n%s", err, out)
		}
		if result.Status != "added" {
			t.Fatalf("expected status added, got %q", result.Status)
		}
		return keyPath
	}

	findVM := func(t *testing.T, keyFile, name string) (vmListEntry, bool) {
		t.Helper()
		vms := runParseExeDevJSON[vmListOutput](t, keyFile, "ls", "--json")
		for _, vm := range vms.VMs {
			if vm.VMName == name {
				return vm, true
			}
		}
		return vmListEntry{}, false
	}

	tagKey := addScopedKey(t, []string{"--tag=ci"})

	t.Run("defaults_to_scope_tag", func(t *testing.T) {
		vmName := boxName(t)
		defer func() {
			_, _ = Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "rm", vmName)
		}()

		result := runParseExeDevJSON[newBoxOutput](t, tagKey, "new", "--json", "--command=bash", "--name="+vmName)
		if result.VMName != vmName {
			t.Fatalf("vm_name = %q, want %q", result.VMName, vmName)
		}
		if want := []string{"ci"}; !slices.Equal(result.Tags, want) {
			t.Fatalf("tags = %v, want %v", result.Tags, want)
		}

		vm, ok := findVM(t, tagKey, vmName)
		if !ok {
			t.Fatalf("expected %s to be visible to tag-scoped key", vmName)
		}
		if want := []string{"ci"}; !slices.Equal(vm.Tags, want) {
			t.Fatalf("ls tags = %v, want %v", vm.Tags, want)
		}
	})

	t.Run("matching_tag_allowed", func(t *testing.T) {
		vmName := boxName(t)
		defer func() {
			_, _ = Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "rm", vmName)
		}()

		result := runParseExeDevJSON[newBoxOutput](t, tagKey, "new", "--json", "--command=bash", "--name="+vmName, "--tag=ci")
		if result.VMName != vmName {
			t.Fatalf("vm_name = %q, want %q", result.VMName, vmName)
		}
		if want := []string{"ci"}; !slices.Equal(result.Tags, want) {
			t.Fatalf("tags = %v, want %v", result.Tags, want)
		}

		vm, ok := findVM(t, tagKey, vmName)
		if !ok {
			t.Fatalf("expected %s to be visible to tag-scoped key", vmName)
		}
		if want := []string{"ci"}; !slices.Equal(vm.Tags, want) {
			t.Fatalf("ls tags = %v, want %v", vm.Tags, want)
		}
	})

	t.Run("other_tag_rejected", func(t *testing.T) {
		vmName := boxName(t)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey,
			"new", "--json", "--command=bash", "--name="+vmName, "--tag=deploy")
		if err == nil {
			t.Fatalf("new with non-matching --tag should fail for tag-scoped key, got: %s", out)
		}

		var result struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse new error JSON: %v\n%s", err, out)
		}
		if !strings.Contains(result.Error, "can only use --tag=ci") {
			t.Fatalf("expected scope restriction error, got: %s", out)
		}

		if _, ok := findVM(t, rootKey, vmName); ok {
			t.Fatalf("rejected new should not create %s", vmName)
		}
	})
}
