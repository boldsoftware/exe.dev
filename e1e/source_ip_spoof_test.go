package e1e

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSourceIPSpoofing verifies that a VM cannot impersonate another VM
// by spoofing its source IP on the NAT bridge.
//
// Without per-TAP source-IP filtering, any VM can assign itself an arbitrary
// 10.42.0.0/16 address and make metadata / gateway / integration requests
// that are attributed to whichever VM owns that IP. We add two VMs, learn the
// victim's IP, then on the attacker VM:
//   - add victim's IP as a secondary address on eth0
//   - curl the metadata service bound to that address
//
// and assert the metadata service does *not* return the victim's name.
func TestSourceIPSpoofing(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	victim := newBox(t, pty)
	defer pty.deleteBox(victim)
	attacker := newBox(t, pty)
	defer pty.deleteBox(attacker)

	waitForSSH(t, victim, keyFile)
	waitForSSH(t, attacker, keyFile)

	getMetadata := func(t *testing.T, box, iface string) (name, sourceIP string) {
		t.Helper()
		args := []string{"curl", "--max-time", "5", "-s"}
		if iface != "" {
			args = append(args, "--interface", iface)
		}
		args = append(args, "http://169.254.169.254/")
		out, err := boxSSHCommand(t, box, keyFile, args...).CombinedOutput()
		if err != nil {
			// curl may fail (connect refused / timeout) once the fix is in; that's OK.
			t.Logf("curl on %s (iface=%s) failed: %v\n%s", box, iface, err, out)
			return "", ""
		}
		var resp struct {
			Name     string `json:"name"`
			SourceIP string `json:"source_ip"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Logf("metadata response from %s (iface=%s): %q", box, iface, string(out))
			return "", ""
		}
		return resp.Name, resp.SourceIP
	}

	// Baseline: each VM sees its own name.
	victimName, victimIP := getMetadata(t, victim, "")
	if victimName != victim {
		t.Fatalf("victim metadata: want name=%q, got %q", victim, victimName)
	}
	if victimIP == "" {
		t.Fatalf("victim source_ip unexpected: %q", victimIP)
	}
	t.Logf("victim %s has IP %s", victim, victimIP)

	attackerName, attackerIP := getMetadata(t, attacker, "")
	if attackerName != attacker {
		t.Fatalf("attacker metadata: want name=%q, got %q", attacker, attackerName)
	}

	// Attacker assigns victim's IP as a secondary address on eth0 and poisons
	// the bridge FDB with a gratuitous ARP so host-side TCP replies to victimIP
	// come back to the attacker's tap (otherwise handshake replies race to the
	// real victim and the curl just times out).
	spoofSetup := strings.Join([]string{
		"set -ex",
		"sudo ip addr add " + victimIP + "/32 dev eth0 label eth0:spoof",
		"which arping || sudo apt-get install -y iputils-arping >/dev/null 2>&1 || true",
		// Announce victimIP as our MAC so the host's ARP cache/bridge FDB
		// sends replies destined for victimIP back to our tap instead of the
		// real victim. Retry a few times.
		"for i in 1 2 3; do sudo arping -U -I eth0 -c 1 -w 1 " + victimIP + " || true; done",
		"ip addr show eth0 | grep -E 'inet ' || true",
	}, "\n")
	if out, err := boxSSHShell(t, attacker, keyFile, spoofSetup).CombinedOutput(); err != nil {
		t.Fatalf("failed to set up spoof on attacker: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		boxSSHShell(t, attacker, keyFile, "sudo ip addr del "+victimIP+"/32 dev eth0 2>/dev/null || true").Run()
	})

	spoofedName, spoofedIP := getMetadata(t, attacker, victimIP)
	t.Logf("spoof attempt from %s (attacker=%s) as %s -> name=%q source_ip=%q",
		attacker, attackerIP, victimIP, spoofedName, spoofedIP)

	if spoofedName == victim {
		t.Fatalf("SECURITY: attacker VM %s successfully impersonated victim VM %s via source-IP spoofing (metadata returned name=%q source_ip=%q)",
			attacker, victim, spoofedName, spoofedIP)
	}

	// Also assert that the gateway endpoint (which does GetInstanceByIP)
	// does not successfully identify the victim box from a spoofed source.
	out, err := boxSSHCommand(t, attacker, keyFile,
		"curl", "--max-time", "5", "-s", "--interface", victimIP,
		"-o", "/dev/null", "-w", "%{http_code}",
		"http://169.254.169.254/gateway/llm/ready").CombinedOutput()
	if err == nil {
		code := strings.TrimSpace(string(out))
		if code == "200" {
			t.Fatalf("SECURITY: gateway /ready returned 200 to spoofed request from %s as %s", attacker, victimIP)
		}
		t.Logf("gateway /ready from spoofed source returned HTTP %s (non-200, good)", code)
	} else {
		t.Logf("gateway /ready from spoofed source failed at transport (good): %v\n%s", err, out)
	}
}
