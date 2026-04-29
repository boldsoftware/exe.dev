package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIntegrationsPeer tests the http-proxy integration with --peer auth.
// It creates two VMs, starts a web server on the target, creates a peer
// integration, and verifies the source VM can reach the target through
// the integration proxy.
func TestIntegrationsPeer(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create target VM first (web server host).
	targetBN := boxName(t) + "-tgt"
	pty.SendLine(fmt.Sprintf("new --name=%s", targetBN))
	pty.WantRE("Creating .*" + targetBN)
	pty.Want("Ready")
	pty.WantPrompt()

	// Create source VM (the one that curls the integration).
	sourceBN := boxName(t) + "-src"
	pty.SendLine(fmt.Sprintf("new --name=%s", sourceBN))
	pty.WantRE("Creating .*" + sourceBN)
	pty.Want("Ready")
	pty.WantPrompt()

	waitForSSH(t, sourceBN, keyFile)
	waitForSSH(t, targetBN, keyFile)

	// Start a web server on the target VM and make it publicly accessible.
	// Also drop in a CGI script that echoes selected request headers so we
	// can verify what the target sees (e.g. X-Exedev-Source-Vm).
	installHeaderEchoCGI(t, targetBN, keyFile)
	serveIndex(t, targetBN, keyFile, "hello-from-peer-target")
	configureProxyRoute(t, keyFile, targetBN, 8080, "public")

	// Build the target URL using the exeprox HTTP port. In production
	// the target would be https://vm.exe.xyz (port 443, TLS terminated
	// by caddy/exeprox). In test we use http with the explicit exeprox
	// port since there's no TLS. The peer integration routes through
	// exed's /_/peer-proxy gateway, so exed needs to reach the target.
	targetURL := fmt.Sprintf("http://%s.exe.cloud:%d", targetBN, Env.servers.Exeprox.HTTPPort)

	// Create a peer integration with the explicit target.
	pty.SendLine(fmt.Sprintf(
		"integrations add http-proxy --name=target-peer --target=%s --peer --attach=vm:%s",
		targetURL, sourceBN))
	pty.Want("Added integration")
	pty.Want(targetBN)
	pty.WantPrompt()

	// Verify it shows up with peer badge.
	pty.SendLine("integrations list")
	pty.Want("target-peer")
	pty.Want("http-proxy")
	pty.Want("peer=" + targetBN)
	pty.WantPrompt()

	// Verify the SSH key was created and is linked to the integration.
	pty.SendLine("ssh-key list")
	pty.Want("peer-target-peer")
	pty.WantPrompt()

	// Managed SSH keys cannot be removed directly.
	pty.SendLine("ssh-key remove peer-target-peer")
	pty.Want("managed by an integration")
	pty.WantPrompt()

	// Curl from the source VM through the integration proxy and verify
	// the response comes from the target VM's web server.
	curlCmd := "curl --max-time 5 -s http://target-peer.int.exe.cloud/"
	deadline := time.Now().Add(45 * time.Second)
	for {
		out, _ := boxSSHShell(t, sourceBN, keyFile, curlCmd).CombinedOutput()
		response := string(out)
		if strings.Contains(response, "hello-from-peer-target") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for peer proxy response, last output: %s", response)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Curl the header-echo CGI through the integration proxy. The peer
	// proxy in exed must stamp X-Exedev-Source-Vm with the authenticated
	// source VM name, overwriting any value sent by the source.
	echoCmd := fmt.Sprintf(
		`curl --max-time 5 -s -H 'X-Exedev-Source-Vm: spoofed-by-source' http://target-peer.int.exe.cloud/cgi-bin/headers`,
	)
	out, err := boxSSHShell(t, sourceBN, keyFile, echoCmd).CombinedOutput()
	if err != nil {
		t.Fatalf("curl header-echo CGI: %v\n%s", err, out)
	}
	want := "source-vm=" + sourceBN
	if !strings.Contains(string(out), want) {
		t.Fatalf("header-echo response missing %q: %s", want, out)
	}
	if strings.Contains(string(out), "spoofed-by-source") {
		t.Fatalf("source spoofed X-Exedev-Source-Vm leaked to target: %s", out)
	}

	// Remove the integration.
	pty.SendLine("integrations remove target-peer")
	pty.Want("Removed integration target-peer")
	pty.WantPrompt()

	// Verify the SSH key was cleaned up with the integration.
	pty.SendLine("ssh-key list")
	pty.WantPrompt()

	// Curl should fail now (integration removed).
	curlCmd = "curl --max-time 5 -s -o /dev/null -w '%{http_code}' http://target-peer.int.exe.cloud/"
	deadline = time.Now().Add(30 * time.Second)
	for {
		out, _ := boxSSHShell(t, sourceBN, keyFile, curlCmd).CombinedOutput()
		if strings.Contains(string(out), "403") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 403 after integration removal, got: %s", string(out))
		}
		time.Sleep(200 * time.Millisecond)
	}

	cleanupBox(t, keyFile, sourceBN)
	cleanupBox(t, keyFile, targetBN)
}

// TestIntegrationsPeerValidation tests peer auth validation.
func TestIntegrationsPeerValidation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// --peer without --target.
	pty.SendLine("integrations add http-proxy --name=bad --peer")
	pty.Want("--target is required")
	pty.WantPrompt()

	// --peer with a non-VM target.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://example.com --peer")
	pty.Want("must be a VM")
	pty.WantPrompt()

	// --peer referencing a nonexistent VM.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://nonexistent-vm.exe.cloud --peer")
	pty.Want("not found")
	pty.WantPrompt()

	// --peer is mutually exclusive with --header and --bearer.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.exe.cloud --peer --header=X-Foo:bar")
	pty.Want("mutually exclusive")
	pty.WantPrompt()
}

// installHeaderEchoCGI puts a busybox-httpd CGI script at
// /home/exedev/cgi-bin/headers on the box. The script echoes selected
// HTTP headers (only those forwarded as HTTP_* CGI env vars) back to
// the caller as `name=value` lines, e.g. `source-vm=<vm>`.
func installHeaderEchoCGI(t *testing.T, box, keyFile string) {
	t.Helper()
	shellCmd := `mkdir -p /home/exedev/cgi-bin
cat > /home/exedev/cgi-bin/headers <<'EOF'
#!/bin/sh
printf 'Content-Type: text/plain\r\n\r\n'
printf 'source-vm=%s\n' "$HTTP_X_EXEDEV_SOURCE_VM"
printf 'box=%s\n' "$HTTP_X_EXEDEV_BOX"
EOF
chmod +x /home/exedev/cgi-bin/headers
`
	if err := boxSSHShell(t, box, keyFile, shellCmd).Run(); err != nil {
		t.Fatalf("install header-echo CGI: %v", err)
	}
}
