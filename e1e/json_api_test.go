package e1e

import (
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
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

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
		if key.Current {
			foundCurrent = true
			break
		}
	}
	if !foundCurrent {
		t.Errorf("expected at least one current SSH key in whoami output, got none")
	}

	// Also verify plain-text whoami output without PTY contains the email.
	// This exercises the non-interactive exec path and ensures clean output formatting.
	whoPlain, err := runExeDevSSHCommand(t, keyFile, "whoami")
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
	pty.sendLine("new --name=" + nbo.VMName)
	pty.wantRe("VM name .*" + regexp.QuoteMeta(nbo.VMName) + ".* is not available")
	pty.wantPrompt()

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
	// TODO: check image name

	delResult := runParseExeDevJSON[deleteVMOutput](t, keyFile, "rm", nbo.VMName, "--json")
	if delResult.VMName != nbo.VMName {
		t.Errorf("expected VM name %q in rm output, got %q", nbo.VMName, delResult.VMName)
	}
	if delResult.Status != "deleted" {
		t.Errorf("expected status 'deleted' in rm output, got %q", delResult.Status)
	}

	// Verify the VM is gone from the list
	vlo2 := runParseExeDevJSON[vmListOutput](t, keyFile, "ls", "--json")
	vms2 := vlo2.VMs
	if len(vms2) != 0 {
		t.Errorf("expected zero VMs in ls output after deletion, got %d", len(vms2))
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
	pty.sendLine("whoami")
	pty.reject(initialKey)
	pty.wantPrompt()

	// Verify that we can't log in using the deleted key.
	sshArgs := append(baseSSHArgs("", keyFile), "whoami")
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
	VMName  string `json:"vm_name"`
	SSHDest string `json:"ssh_dest"`
	Status  string `json:"status"`
}

type vmListOutput struct {
	VMs []vmListEntry `json:"vms"`
}

type deleteVMOutput struct {
	VMName string `json:"vm_name"`
	Status string `json:"status"`
}

type browserCommandOutput struct {
	MagicLink string `json:"magic_link"`
}

type whoamiOutput struct {
	Email   string `json:"email"`
	SSHKeys []struct {
		PublicKey string `json:"public_key"`
		Current   bool   `json:"current"`
	} `json:"ssh_keys"`
}

type deleteSSHKeyOutput struct {
	PublicKey string `json:"public_key"`
	Status    string `json:"status"`
}
