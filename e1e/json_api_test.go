package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"exe.dev/vouch"
)

// TestExeDevAPI tests a variety of exe.dev commands/repls.
func TestExeDevAPI(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

	whoOut, err := runExeDevSSHCommand(t, keyFile, "whoami", "--json")
	if err != nil {
		t.Fatalf("failed to run whoami command: %v\n%s", err, whoOut)
	}
	var who whoamiOutput
	err = json.Unmarshal(whoOut, &who)
	if err != nil {
		t.Fatalf("failed to parse whoami output as JSON: %v\n%s", err, whoOut)
	}
	if who.Email == "" {
		t.Errorf("expected email in whoami output, got empty string")
	}
	if len(who.SSHKeys) == 0 {
		t.Errorf("expected at least one SSH key in whoami output, got zero")
	}
	foundCurrent := false
	for _, key := range who.SSHKeys {
		if key.Current {
			foundCurrent = true
			break
		}
	}
	if !foundCurrent {
		t.Errorf("expected at least one current SSH key in whoami output, got none")
	}

	newOut, err := runExeDevSSHCommand(t, keyFile, "new", "--json")
	if err != nil {
		t.Fatalf("failed to run new box command: %v\n%s", err, newOut)
	}
	var nbo newBoxOutput
	err = json.Unmarshal(newOut, &nbo)
	if err != nil {
		t.Fatalf("failed to parse new box output as JSON: %v\n%s", err, newOut)
	}
	// TODO: actually use these values: ssh to the box, curl the https url, list the boxname using the exe.dev server, etc.
	if nbo.BoxName == "" {
		t.Errorf("expected box_name in JSON output, got empty string")
	}
	if nbo.SSH == "" {
		t.Errorf("expected ssh_command in JSON output, got empty string")
	}
	if nbo.HTTPS == "" {
		t.Errorf("expected https_url in JSON output, got empty string")
	}
	if !strings.HasPrefix(nbo.SSH, "ssh ") {
		t.Errorf("expected ssh_command to start with 'ssh ', got %q", nbo.SSH)
	}
	if !strings.HasPrefix(nbo.HTTPS, "http") {
		t.Errorf("expected https_url to start with 'http', got %q", nbo.HTTPS)
	}

	// Try to create a duplicate box using the repl.
	Env.addCanonicalization(nbo.BoxName, "BOX_NAME")
	pty.sendLine("new --name=" + nbo.BoxName)
	pty.wantRe("Box name .*" + regexp.QuoteMeta(nbo.BoxName) + ".* is not available")
	pty.wantPrompt()

	listOut, err := runExeDevSSHCommand(t, keyFile, "list", "--json")
	if err != nil {
		t.Fatalf("failed to run list command: %v\n%s", err, listOut)
	}
	t.Logf("list output: %s", listOut)
	var blo boxListOutput
	err = json.Unmarshal(listOut, &blo)
	if err != nil {
		t.Fatalf("failed to parse list output as JSON: %v\n%s", err, listOut)
	}
	boxes := blo.Boxes
	if len(boxes) != 1 {
		t.Errorf("expected exactly one box in list output, got %d", len(boxes))
	}
	box0 := boxes[0]
	if box0.BoxName != nbo.BoxName {
		t.Errorf("expected box name %q in list output, got %q", nbo.BoxName, boxes[0].BoxName)
	}
	if box0.Status != "running" {
		t.Errorf("expected status 'running' in list output, got %q", boxes[0].Status)
	}
	// TODO: check image name

	delOut, err := runExeDevSSHCommand(t, keyFile, "delete", nbo.BoxName, "--json")
	if err != nil {
		t.Fatalf("failed to run delete command: %v\n%s", err, delOut)
	}
	var delResult deleteBoxOutput
	err = json.Unmarshal(delOut, &delResult)
	if err != nil {
		t.Fatalf("failed to parse delete output as JSON: %v\n%s", err, delOut)
	}
	if delResult.BoxName != nbo.BoxName {
		t.Errorf("expected box name %q in delete output, got %q", nbo.BoxName, delResult.BoxName)
	}
	if delResult.Status != "deleted" {
		t.Errorf("expected status 'deleted' in delete output, got %q", delResult.Status)
	}

	// Verify the box is gone from the list
	listOut2, err := runExeDevSSHCommand(t, keyFile, "list", "--json")
	if err != nil {
		t.Fatalf("failed to run list command: %v\n%s", err, listOut2)
	}
	var blo2 boxListOutput
	err = json.Unmarshal(listOut2, &blo2)
	if err != nil {
		t.Fatalf("failed to parse list output as JSON: %v\n%s", err, listOut2)
	}
	boxes2 := blo2.Boxes
	if len(boxes2) != 0 {
		t.Errorf("expected zero boxes in list output after deletion, got %d", len(boxes2))
	}

	browserOut, err := runExeDevSSHCommand(t, keyFile, "browser", "--json")
	if err != nil {
		t.Fatalf("failed to run browser command: %v\n%s", err, browserOut)
	}
	var browser browserCommandOutput
	err = json.Unmarshal(browserOut, &browser)
	if err != nil {
		t.Fatalf("failed to parse browser output as JSON: %v\n%s", err, browserOut)
	}
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
	expectedHost := fmt.Sprintf("localhost:%d", Env.exed.HTTPPort)
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

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
}

type newBoxOutput struct {
	BoxName string `json:"box_name"`
	SSH     string `json:"ssh_command"`
	HTTPS   string `json:"https_url"`
}

type boxListEntry struct {
	BoxName string `json:"box_name"`
	Status  string `json:"status"`
}

type boxListOutput struct {
	Boxes []boxListEntry `json:"boxes"`
}

type deleteBoxOutput struct {
	BoxName string `json:"box_name"`
	Status  string `json:"status"`
}

type browserCommandOutput struct {
	MagicLink string `json:"magic_link"`
}

type whoamiOutput struct {
	Email   string `json:"email"`
	SSHKeys []struct {
		Key     string `json:"key"`
		Current bool   `json:"current"`
	} `json:"ssh_keys"`
}
