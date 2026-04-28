package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIntegrationsReflection exercises the reflection integration end-to-end:
// a VM → exelet (169.254.169.254) → exed (/_/gateway/reflection) round-trip.
func TestIntegrationsReflection(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)
	bn := boxName(t)
	defer cleanupBox(t, keyFile, bn)
	pty.SendLine(fmt.Sprintf("new --name=%s", bn))
	pty.WantRE("Creating .*" + bn)
	pty.Want("Ready")
	pty.WantPrompt()
	waitForSSH(t, bn, keyFile)

	// Tag the VM so /tags has meaningful content.
	pty.SendLine(fmt.Sprintf("tag %s env-dev", bn))
	pty.WantPrompt()
	pty.SendLine(fmt.Sprintf("tag %s role-api", bn))
	pty.WantPrompt()

	// Set a comment on the VM so /comment has meaningful content.
	pty.SendLine(fmt.Sprintf("comment %s hello world", bn))
	pty.WantPrompt()

	// Add a reflection integration with a comment.
	pty.SendLine(`integrations add reflection --name=me --comment="who am i" --fields=email,integrations,tags,comment`)
	pty.Want("Added integration me")
	pty.WantPrompt()
	pty.SendLine(fmt.Sprintf("integrations attach me vm:%s", bn))
	pty.Want("Attached me to vm:" + bn)
	pty.WantPrompt()

	// Add a second integration so /integrations returns something
	// non-trivial. Attach it to the same VM.
	pty.SendLine(`integrations add http-proxy --name=echoproxy --target=https://httpbin.org --header=X-Custom:v --comment="echo service"`)
	pty.Want("Added integration echoproxy")
	pty.WantPrompt()
	pty.SendLine(fmt.Sprintf("integrations attach echoproxy vm:%s", bn))
	pty.Want("Attached echoproxy to vm:" + bn)
	pty.WantPrompt()

	curlRetry := func(t *testing.T, path, want string) string {
		t.Helper()
		cmd := fmt.Sprintf(`curl --max-time 5 -s http://me.int.exe.cloud%s`, path)
		var response string
		deadline := time.Now().Add(45 * time.Second)
		for {
			out, _ := boxSSHShell(t, bn, keyFile, cmd).CombinedOutput()
			response = string(out)
			if strings.Contains(response, want) {
				return response
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in response to %s:\n%s", want, path, response)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	t.Run("email", func(t *testing.T) {
		resp := curlRetry(t, "/email", "email")
		var got map[string]any
		if err := json.Unmarshal([]byte(resp), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, resp)
		}
		if got["email"] != email {
			t.Errorf("email: got %v, want %q", got["email"], email)
		}
	})

	t.Run("tags", func(t *testing.T) {
		resp := curlRetry(t, "/tags", "tags")
		var got map[string]any
		if err := json.Unmarshal([]byte(resp), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, resp)
		}
		tags, _ := got["tags"].([]any)
		if len(tags) != 2 {
			t.Errorf("tags: got %v, want 2 entries", got["tags"])
		}
		tagSet := map[string]bool{}
		for _, v := range tags {
			tagSet[fmt.Sprint(v)] = true
		}
		if !tagSet["env-dev"] || !tagSet["role-api"] {
			t.Errorf("missing expected tags in %v", got["tags"])
		}
	})

	t.Run("integrations", func(t *testing.T) {
		resp := curlRetry(t, "/integrations", "integrations")
		var got map[string]any
		if err := json.Unmarshal([]byte(resp), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, resp)
		}
		items, _ := got["integrations"].([]any)
		byName := map[string]map[string]any{}
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m != nil {
				byName[fmt.Sprint(m["name"])] = m
			}
		}
		if me := byName["me"]; me == nil {
			t.Errorf("missing 'me' integration in response: %s", resp)
		} else {
			if me["type"] != "reflection" {
				t.Errorf("me type: got %v, want reflection", me["type"])
			}
			if me["comment"] != "who am i" {
				t.Errorf("me comment: got %v, want 'who am i'", me["comment"])
			}
			if !strings.Contains(fmt.Sprint(me["help"]), "curl http://me.int") {
				t.Errorf("me help missing curl hint: %v", me["help"])
			}
		}
		if ep := byName["echoproxy"]; ep == nil {
			t.Errorf("missing 'echoproxy' integration")
		} else if ep["comment"] != "echo service" {
			t.Errorf("echoproxy comment: got %v, want 'echo service'", ep["comment"])
		}
	})

	t.Run("comment", func(t *testing.T) {
		resp := curlRetry(t, "/comment", "comment")
		var got map[string]any
		if err := json.Unmarshal([]byte(resp), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, resp)
		}
		if got["comment"] != "hello world" {
			t.Errorf("comment: got %v, want %q", got["comment"], "hello world")
		}
	})

	t.Run("index", func(t *testing.T) {
		resp := curlRetry(t, "/", "/email")
		for _, p := range []string{"/email", "/integrations", "/tags", "/comment"} {
			if !strings.Contains(resp, p) {
				t.Errorf("index missing %s:\n%s", p, resp)
			}
		}
	})

	t.Run("field_gating", func(t *testing.T) {
		// Recreate reflection with only email enabled.
		pty.SendLine("integrations remove me")
		pty.Want("Removed integration me")
		pty.WantPrompt()
		pty.SendLine("integrations add reflection --name=me --fields=email")
		pty.Want("Added integration me")
		pty.WantPrompt()
		pty.SendLine(fmt.Sprintf("integrations attach me vm:%s", bn))
		pty.Want("Attached me to vm:" + bn)
		pty.WantPrompt()

		// /email still works.
		curlRetry(t, "/email", "email")

		// /tags should be 403.
		cmd := `curl --max-time 5 -s -o /dev/null -w '%{http_code}' http://me.int.exe.cloud/tags`
		deadline := time.Now().Add(45 * time.Second)
		for {
			out, _ := boxSSHShell(t, bn, keyFile, cmd).CombinedOutput()
			if strings.Contains(string(out), "403") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("expected 403 on /tags when field disabled, got %s", string(out))
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
}
