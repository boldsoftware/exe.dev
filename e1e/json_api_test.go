package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestExeDevAPI tests a variety of exe.dev commands/repls.
func TestExeDevAPI(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	t.Run("ls_empty", func(t *testing.T) {
		raw, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ls", "--json")
		if err != nil {
			t.Fatalf("failed to run ls --json: %v\n%s", err, raw)
		}
		if strings.Contains(string(raw), "null") {
			t.Errorf("expected no null values in ls --json output: %s", raw)
		}
		if !strings.Contains(string(raw), "[]") {
			t.Errorf("expected empty array [] in ls --json output: %s", raw)
		}
	})

	t.Run("ssh_key_list_no_null", func(t *testing.T) {
		raw := runExeDevSSHNoNull(t, keyFile, "ssh-key", "list", "--json")
		if !strings.Contains(string(raw), "[]") && !strings.Contains(string(raw), "[{") {
			t.Errorf("expected array (empty or populated) in ssh-key list --json output: %s", raw)
		}
	})

	who := runParseExeDevJSON[whoamiOutput](t, keyFile, "whoami", "--json")
	if who.Email == "" {
		t.Errorf("expected email in whoami output, got empty string")
	}
	if len(who.SSHKeys) == 0 {
		t.Errorf("expected at least one SSH key in whoami output, got zero")
	}
	foundCurrent := false
	initialKey := strings.TrimSpace(who.SSHKeys[0].PublicKey)
	for _, key := range who.SSHKeys {
		if key.Fingerprint == "" {
			t.Errorf("expected fingerprint for SSH key, got empty string")
		}
		if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
			t.Errorf("expected fingerprint to start with SHA256:, got %q", key.Fingerprint)
		}
		if key.Current {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Errorf("expected at least one current SSH key in whoami output, got none")
	}

	// Also verify plain-text whoami output without PTY contains the email.
	// This exercises the non-interactive exec path and ensures clean output formatting.
	whoPlain, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "whoami")
	if err != nil {
		t.Fatalf("failed to run whoami (plain): %v\n%s", err, whoPlain)
	}
	if !strings.Contains(string(whoPlain), who.Email) {
		t.Fatalf("expected whoami output to include email %q, got: %s", who.Email, string(whoPlain))
	}

	nbo := runParseExeDevJSON[newBoxOutput](t, keyFile, "new", "--command=bash", "--json")
	// TODO: actually use these values: ssh to the box, curl the https url, list the boxname using the exe.dev server, etc.
	if nbo.VMName == "" {
		t.Errorf("expected vm_name in JSON output, got empty string")
	}
	if nbo.HTTPS == "" {
		t.Errorf("expected https_url in JSON output, got empty string")
	}
	if !strings.HasPrefix(nbo.HTTPS, "http") {
		t.Errorf("expected https_url to start with 'http', got %q", nbo.HTTPS)
	}
	if nbo.SSHDest == "" {
		t.Errorf("expected ssh_dest in JSON output, got empty string")
	}
	if nbo.SSHPort == 0 {
		t.Errorf("expected ssh_port in JSON output, got 0")
	}
	if nbo.SSH == "" {
		t.Errorf("expected ssh_command in JSON output, got empty string")
	}
	expectedSSH := "ssh "
	if nbo.SSHPort != 22 {
		expectedSSH += fmt.Sprintf("-p %d ", nbo.SSHPort)
	}
	expectedSSH += nbo.SSHDest
	if nbo.SSH != expectedSSH {
		t.Errorf("expected ssh_command %q, got %q", expectedSSH, nbo.SSH)
	}
	if nbo.SSHPort != Env.sshPort() {
		t.Errorf("expected ssh_port %d, got %d", Env.sshPort(), nbo.SSHPort)
	}

	// Try to create a duplicate box using the repl.
	testinfra.AddCanonicalization(nbo.VMName, "VM_NAME")
	pty.SendLine("new --name=" + nbo.VMName)
	pty.WantRE("VM name .*" + regexp.QuoteMeta(nbo.VMName) + ".* is not available")
	pty.WantPrompt()

	// ls --json -l should work (not error); -l is a quiet no-op with --json.
	vloL := runParseExeDevJSON[vmListOutput](t, keyFile, "ls", "--json", "-l")
	if len(vloL.VMs) != 1 {
		t.Errorf("expected 1 VM from ls --json -l, got %d", len(vloL.VMs))
	}

	vlo := runParseExeDevJSON[vmListOutput](t, keyFile, "ls", "--json")
	t.Logf("ls output: %+v", vlo)
	vms := vlo.VMs
	if len(vms) != 1 {
		t.Errorf("expected exactly one VM in ls output, got %d", len(vms))
	}
	vm0 := vms[0]
	if vm0.VMName != nbo.VMName {
		t.Errorf("expected VM name %q in ls output, got %q", nbo.VMName, vms[0].VMName)
	}
	if vm0.Status != "running" {
		t.Errorf("expected status 'running' in ls output, got %q", vms[0].Status)
	}
	if vm0.SSHDest == "" {
		t.Errorf("expected ssh_dest in ls output, got empty string")
	}
	if vm0.Region == "" {
		t.Errorf("expected region in ls output, got empty string")
	}
	if vm0.HTTPS == "" {
		t.Errorf("expected https_url in ls output, got empty string")
	}
	if !strings.HasPrefix(vm0.HTTPS, "http") {
		t.Errorf("expected https_url to start with 'http', got %q", vm0.HTTPS)
	}
	if vm0.ShelleyURL == "" {
		t.Errorf("expected shelley_url in ls output for default image, got empty string")
	}
	if !strings.HasPrefix(vm0.ShelleyURL, "http") {
		t.Errorf("expected shelley_url to start with 'http', got %q", vm0.ShelleyURL)
	}
	// TODO: check image name

	t.Run("share_show_no_null", func(t *testing.T) {
		runExeDevSSHNoNull(t, keyFile, "share", "show", nbo.VMName, "--json")
	})

	rmRaw := runExeDevSSHNoNull(t, keyFile, "rm", nbo.VMName, "--json")
	var delResult deleteVMOutput
	if err := json.Unmarshal(rmRaw, &delResult); err != nil {
		t.Fatalf("failed to parse rm --json output: %v\n%s", err, rmRaw)
	}
	if len(delResult.Deleted) != 1 || delResult.Deleted[0] != nbo.VMName {
		t.Errorf("expected deleted=[%q] in rm output, got %v", nbo.VMName, delResult.Deleted)
	}
	if len(delResult.Failed) != 0 {
		t.Errorf("expected no failed VMs in rm output, got %v", delResult.Failed)
	}

	// Verify the VM is gone from the list and no null values
	lsRaw := runExeDevSSHNoNull(t, keyFile, "ls", "--json")
	var vlo2 vmListOutput
	if err := json.Unmarshal(lsRaw, &vlo2); err != nil {
		t.Fatalf("failed to parse ls --json output: %v\n%s", err, lsRaw)
	}
	if len(vlo2.VMs) != 0 {
		t.Errorf("expected zero VMs in ls output after deletion, got %d", len(vlo2.VMs))
	}

	browser := runParseExeDevJSON[browserCommandOutput](t, keyFile, "browser", "--json")
	if browser.MagicLink == "" {
		t.Fatalf("expected magic_link in browser output, got empty string")
	}

	magicURL, err := url.Parse(browser.MagicLink)
	if err != nil {
		t.Fatalf("failed to parse magic_link %q: %v", browser.MagicLink, err)
	}
	if magicURL.Scheme != "http" {
		t.Errorf("expected magic_link scheme http, got %q", magicURL.Scheme)
	}
	expectedHost := fmt.Sprintf("localhost:%d", Env.servers.Exed.HTTPPort)
	if magicURL.Host != expectedHost {
		t.Errorf("expected magic_link host %q, got %q", expectedHost, magicURL.Host)
	}
	if magicURL.Path != "/auth/verify" {
		t.Errorf("expected magic_link path /auth/verify, got %q", magicURL.Path)
	}
	token := magicURL.Query().Get("token")
	if token == "" {
		t.Fatalf("expected token query parameter in magic_link %q", browser.MagicLink)
	}

	client := noRedirectClient(nil)
	resp, err := client.Get(browser.MagicLink)
	if err != nil {
		t.Fatalf("failed to fetch magic_link %q: %v", browser.MagicLink, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 307 from magic_link, got %d\n%s", resp.StatusCode, body)
	}
	foundAuthCookie := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "exe-auth" {
			foundAuthCookie = true
			break
		}
	}
	if !foundAuthCookie {
		t.Errorf("expected exe-auth cookie from magic_link response")
	}

	del := runParseExeDevJSON[deleteSSHKeyOutput](t, keyFile, "delete-ssh-key", initialKey, "--json")
	if del.PublicKey != initialKey {
		t.Fatalf("expected deleted key %q, got %q", initialKey, del.PublicKey)
	}
	if del.Status != "deleted" {
		t.Fatalf("expected status deleted from delete-ssh-key, got %q", del.Status)
	}

	// Confirm pty session is still usable after key deletion,
	// and that initialKey is not listed.
	pty.SendLine("whoami")
	pty.Reject(initialKey)
	pty.WantPrompt()

	// Verify that we can't log in using the deleted key.
	sshArgs := append(Env.servers.BaseSSHArgs("", keyFile), "whoami")
	cmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected login with deleted key to fail, got success: %q", output)
	}
	if s := string(output); !strings.Contains(s, "Please complete registration") && !strings.Contains(s, "Permission denied") {
		t.Fatalf("expected login with deleted key to fail with a permission error, got: %q", s)
	}
}

type newBoxOutput struct {
	VMName  string `json:"vm_name"`
	SSH     string `json:"ssh_command"`
	SSHDest string `json:"ssh_dest"`
	SSHPort int    `json:"ssh_port"`
	HTTPS   string `json:"https_url"`
}

type vmListEntry struct {
	VMName     string `json:"vm_name"`
	SSHDest    string `json:"ssh_dest"`
	Status     string `json:"status"`
	Region     string `json:"region"`
	HTTPS      string `json:"https_url"`
	ShelleyURL string `json:"shelley_url"`
}

type vmListOutput struct {
	VMs []vmListEntry `json:"vms"`
}

type deleteVMOutput struct {
	Deleted []string `json:"deleted"`
	Failed  []string `json:"failed"`
}

type browserCommandOutput struct {
	MagicLink string `json:"magic_link"`
}

type whoamiOutput struct {
	Email   string `json:"email"`
	SSHKeys []struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		Current     bool   `json:"current"`
	} `json:"ssh_keys"`
}

type deleteSSHKeyOutput struct {
	PublicKey string `json:"public_key"`
	Status    string `json:"status"`
}

// runExeDevSSHNoNull runs an SSH command and checks that the JSON output contains no null values.
func runExeDevSSHNoNull(t *testing.T, keyFile string, args ...string) []byte {
	t.Helper()
	raw, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, args...)
	if err != nil {
		t.Fatalf("failed to run %v: %v\n%s", args, err, raw)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("unexpected null in %v --json output: %s", args, raw)
	}
	return raw
}
