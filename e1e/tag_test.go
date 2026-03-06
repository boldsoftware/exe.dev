package e1e

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTag(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	// Create a box
	box := newBox(t, pty)
	waitForSSH(t, box, keyFile)

	// Test usage errors
	t.Run("Usage", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag")
		repl.Want("usage")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("InvalidTagName", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag " + box + " UPPER")
		repl.Want("invalid tag name")
		repl.WantPrompt()

		repl.SendLine("tag " + box + " 123")
		repl.Want("invalid tag name")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("AddTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag " + box + " prod")
		repl.Want("Added")
		repl.Want("prod")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("AddDuplicateTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag " + box + " prod")
		repl.Want("already exists")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("AddSecondTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag " + box + " staging")
		repl.Want("Added")
		repl.Want("staging")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("LsShowsTags", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("ls")
		repl.Want("#prod")
		repl.Want("#staging")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("LsJSONShowsTags", func(t *testing.T) {
		raw, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ls", "--json")
		if err != nil {
			t.Fatalf("failed to run ls --json: %v\n%s", err, raw)
		}
		var result struct {
			VMs []struct {
				VMName string   `json:"vm_name"`
				Tags   []string `json:"tags"`
			} `json:"vms"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("failed to parse ls JSON: %v\n%s", err, raw)
		}
		var found bool
		for _, vm := range result.VMs {
			if vm.VMName == box {
				found = true
				if len(vm.Tags) != 2 {
					t.Fatalf("expected 2 tags, got %d: %v", len(vm.Tags), vm.Tags)
				}
				// Tags are sorted
				if vm.Tags[0] != "prod" || vm.Tags[1] != "staging" {
					t.Fatalf("expected [prod, staging], got %v", vm.Tags)
				}
			}
		}
		if !found {
			t.Fatalf("box %q not found in ls JSON output", box)
		}
	})

	t.Run("TagJSONOutput", func(t *testing.T) {
		raw, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "tag", "--json", box, "web")
		if err != nil {
			t.Fatalf("failed to run tag --json: %v\n%s", err, raw)
		}
		var result struct {
			VMName string   `json:"vm_name"`
			Tags   []string `json:"tags"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("failed to parse tag JSON: %v\n%s", err, raw)
		}
		if result.VMName != box {
			t.Fatalf("expected vm_name %q, got %q", box, result.VMName)
		}
		if len(result.Tags) != 3 {
			t.Fatalf("expected 3 tags, got %d: %v", len(result.Tags), result.Tags)
		}
	})

	t.Run("LsFilterByTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("ls prod")
		repl.Want(box)
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("DeleteTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag -d " + box + " prod")
		repl.Want("Removed")
		repl.Want("prod")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("DeleteNonExistentTag", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag -d " + box + " nonexistent")
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("LsAfterDelete", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("ls")
		repl.Reject("#prod")
		repl.Want("#staging")
		repl.Want("#web")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("VMNotFound", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("tag nonexistent-vm-xyz prod")
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Verify tags work with ls filter -- exact tag match after deleting prod
	t.Run("LsFilterByTagAfterDelete", func(t *testing.T) {
		raw, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ls", "--json", "staging")
		if err != nil {
			t.Fatalf("failed to run ls --json staging: %v\n%s", err, raw)
		}
		if !strings.Contains(string(raw), box) {
			t.Fatalf("expected box %q in filtered ls output, got: %s", box, raw)
		}
	})

	cleanupBox(t, keyFile, box)
}
