package exelets

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

func TestTwoHosts(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()

	cmd := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "true")
	cmd.Stdout = t.Output()
	cmd.Stderr = t.Output()
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run true: %v", err)
	}

	deleteBox(t, boxName, keyFile)
}

func TestUserOnSingleHost(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	// Start exed with a single exelet.
	exeletAddrs := []string{exelets[0].Address}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	// Create a box on that exelet.
	pty, _, keyFile, email := register(t)
	box1 := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, box1, keyFile)

	// Restart exed with two exelets.
	exeletAddrs = []string{
		exelets[0].Address,
		exelets[1].Address,
	}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], true); err != nil {
		t.Fatal(err)
	}

	// Create another box.
	pty, _ = testinfra.MakeTestPTY(t, "", "ssh localhost", true)
	cmd, err := serverEnv.SSHToExeDev(t.Context(), pty.PTY(), keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)
	box2 := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, box2, keyFile)

	// Check the list of boxes to see where they wound up.
	boxes := boxHosts(t)

	t.Logf("after creating two boxes: %v", boxes)

	cnt := 0
	for _, boxList := range boxes {
		cnt += len(boxList)
	}
	if cnt != 2 {
		t.Errorf("got %d boxes, want 2", cnt)
	}

	if len(boxes) != 1 {
		t.Errorf("boxes found on %d exelets, want 1", len(boxes))
	}
}

func TestLoadedHost(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	email1 := "testloadedhost1" + testinfra.FakeEmailSuffix
	pty1, _, keyFile1 := registerEmail(t, email1)
	defer pty1.Disconnect()

	email2 := "testloadedhost2" + testinfra.FakeEmailSuffix
	pty2, _, keyFile2 := registerEmail(t, email2)
	defer pty2.Disconnect()

	boxName1 := makeBox(t, pty1, keyFile1, email1)
	defer deleteBox(t, boxName1, keyFile1)

	boxName2 := makeBox(t, pty2, keyFile2, email2)
	defer deleteBox(t, boxName2, keyFile2)

	boxes := boxHosts(t)

	t.Logf("after creating two boxes: %v", boxes)

	// Override usage on both exelets so that exelet[0] appears heavily
	// loaded and exelet[1] appears idle.  We must also set DiskFree and
	// MemAvailable to large values because CI VMs have small disks/memory
	// that would otherwise trigger the "extreme usage" check in
	// exeletUsageCmp, causing both exelets to compare as equal regardless
	// of LoadAverage.
	for i, el := range exelets[:2] {
		resp, err := el.Client().GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		usage := resp.Usage
		if i == 0 {
			usage.LoadAverage = 50
		} else {
			usage.LoadAverage = 0
		}
		usage.MemAvailable = 64 << 20 // 64 GiB in KiB — well above extreme threshold
		usage.DiskFree = 100 << 20    // 100 GiB in KiB — well above extreme threshold
		_, err = el.Client().SetMachineUsage(t.Context(),
			&resourceapi.SetMachineUsageRequest{
				Available: true,
				Usage:     usage,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, el := range exelets[:2] {
			_, err := el.Client().SetMachineUsage(t.Context(),
				&resourceapi.SetMachineUsageRequest{
					Available: true,
				},
			)
			if err != nil {
				t.Errorf("failed to reset usage to default: %v", err)
			}
		}
	}()

	// Tell exed to update its view of exelet usage.
	url := fmt.Sprintf("http://localhost:%d/update-exelet-usage-517c8a904", serverEnv.Exed.HTTPPort)
	if _, err := http.Get(url); err != nil {
		t.Fatalf("failed to tell exed to update exelet usage: %v", err)
	}

	// Verify the usage update took effect on both exelets.
	for i, el := range exelets[:2] {
		resp, err := el.Client().GetMachineUsage(t.Context(), &resourceapi.GetMachineUsageRequest{})
		if err != nil {
			t.Logf("exelet[%d] GetMachineUsage error: %v", i, err)
		} else if resp.Usage != nil {
			t.Logf("exelet[%d] addr=%s load=%.1f mem=%d swap=%d disk=%d",
				i, el.Address, resp.Usage.LoadAverage,
				resp.Usage.MemAvailable, resp.Usage.SwapFree, resp.Usage.DiskFree)
		}
	}

	// Make two new boxes.

	email3 := "testloadedhost3" + testinfra.FakeEmailSuffix
	pty3, _, keyFile3 := registerEmail(t, email3)
	defer pty3.Disconnect()

	boxName3 := makeBox(t, pty3, keyFile3, email3)
	defer deleteBox(t, boxName3, keyFile3)

	boxes = boxHosts(t)
	t.Logf("after box3: %v", boxes)

	// Re-trigger usage update before box4 to rule out stale state.
	if _, err := http.Get(url); err != nil {
		t.Fatalf("failed second update-exelet-usage: %v", err)
	}

	email4 := "testloadedhost4" + testinfra.FakeEmailSuffix
	pty4, _, keyFile4 := registerEmail(t, email4)
	defer pty4.Disconnect()

	boxName4 := makeBox(t, pty4, keyFile4, email4)
	defer deleteBox(t, boxName4, keyFile4)

	// Both new boxes should wind up on the second exelet.

	boxes = boxHosts(t)

	t.Logf("after creating four boxes: %v", boxes)

	if !slices.Contains(boxes[exeletAddrs[1]], boxName3) {
		t.Errorf("box3 is not on expected exelet %s", exeletAddrs[1])
	}
	if !slices.Contains(boxes[exeletAddrs[1]], boxName4) {
		t.Errorf("box4 is not on expected exelet %s", exeletAddrs[1])
	}
}

func TestDirectMigration(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)

	sourceBox, ok := findBox(t, boxName)
	if !ok {
		t.Fatalf("box %q not found before migration", boxName)
	}

	var targetAddr string
	switch sourceBox.Host {
	case exeletAddrs[0]:
		targetAddr = exeletAddrs[1]
	case exeletAddrs[1]:
		targetAddr = exeletAddrs[0]
	default:
		t.Fatalf("box %q created on unexpected host %q", boxName, sourceBox.Host)
	}

	t.Run("cold", func(t *testing.T) {
		migrateBox(t, boxName, targetAddr, false)
		assertBoxEventuallyOnHost(t, boxName, targetAddr)
		assertBoxSSHWorks(t, boxName, keyFile)
	})

	t.Run("live_reverse", func(t *testing.T) {
		migrateBox(t, boxName, sourceBox.Host, true)
		assertBoxEventuallyOnHost(t, boxName, sourceBox.Host)
		assertBoxSSHWorks(t, boxName, keyFile)

		migrateBox(t, boxName, targetAddr, true)
		assertBoxEventuallyOnHost(t, boxName, targetAddr)
		assertBoxSSHWorks(t, boxName, keyFile)
	})
}

func migrateBox(t *testing.T, boxName, target string, live bool) {
	t.Helper()

	form := url.Values{
		"box_name":     {boxName},
		"target":       {target},
		"confirm_name": {boxName},
		"live":         {fmt.Sprintf("%t", live)},
	}
	url := fmt.Sprintf("http://localhost:%d/debug/vms/migrate", serverEnv.Exed.HTTPPort)
	resp, err := http.Post(url, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("failed to POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s returned status %d:\n%s", url, resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read migrate response: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "MIGRATION_SUCCESS:"+boxName) {
		t.Fatalf("migration response missing success marker:\n%s", text)
	}
	if !strings.Contains(text, "MIGRATION_DIRECT_CONFIRMED:") {
		t.Fatalf("migration did not use direct exelet-to-exelet path (no MIGRATION_DIRECT_CONFIRMED in response):\n%s", text)
	}
}

func assertBoxEventuallyOnHost(t *testing.T, boxName, wantHost string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		box, ok := findBox(t, boxName)
		if ok && box.Host == wantHost && box.Status == "running" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	box, ok := findBox(t, boxName)
	if !ok {
		t.Fatalf("box %q not found after waiting for host %q", boxName, wantHost)
	}
	t.Fatalf("box %q ended on host=%q status=%q, want host=%q status=running", boxName, box.Host, box.Status, wantHost)
}

func assertBoxSSHWorks(t *testing.T, boxName, keyFile string) {
	t.Helper()

	if err := serverEnv.WaitForBoxSSHServer(t.Context(), boxName, keyFile); err != nil {
		t.Fatal(err)
	}
	out, err := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "hostname").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to ssh to %q after migration: %v\n%s", boxName, err, out)
	}
	if !strings.Contains(string(out), boxName) {
		t.Fatalf("hostname output %q does not contain box name %q", strings.TrimSpace(string(out)), boxName)
	}
}

func TestResumableSidebandMigration(t *testing.T) {
	if err := ensureExeletCount(t.Context(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}
	if err := serverEnv.Exed.Restart(t.Context(), exeletAddrs, exeletTestRunIDs[0], false); err != nil {
		t.Fatal(err)
	}

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)

	sourceBox, ok := findBox(t, boxName)
	if !ok {
		t.Fatalf("box %q not found before migration", boxName)
	}

	// Figure out source and target exelets.
	var sourceExelet, targetExelet *testinfra.ExeletInstance
	var targetAddr string
	switch sourceBox.Host {
	case exeletAddrs[0]:
		sourceExelet = exelets[0]
		targetExelet = exelets[1]
		targetAddr = exeletAddrs[1]
	case exeletAddrs[1]:
		sourceExelet = exelets[1]
		targetExelet = exelets[0]
		targetAddr = exeletAddrs[0]
	default:
		t.Fatalf("box %q on unexpected host %q", boxName, sourceBox.Host)
	}
	_ = sourceExelet // used only for clarity

	// Extract the gRPC port from the target address ("tcp://host:port").
	targetURL, err := url.Parse(targetAddr)
	if err != nil {
		t.Fatalf("parse target addr %q: %v", targetAddr, err)
	}
	_, grpcPort, err := net.SplitHostPort(targetURL.Host)
	if err != nil {
		t.Fatalf("split host port %q: %v", targetURL.Host, err)
	}

	// Install iptables rule on the target VM that blocks all incoming TCP
	// from the source EXCEPT the gRPC port. This breaks the sideband TCP
	// while keeping the gRPC control channel alive for resume.
	sourceHost := sourceExelet.RemoteHost
	blockRule := fmt.Sprintf(
		"iptables -I INPUT -p tcp -s %s ! --dport %s -j REJECT --reject-with tcp-reset",
		sourceHost, grpcPort,
	)
	unblockRule := fmt.Sprintf(
		"iptables -D INPUT -p tcp -s %s ! --dport %s -j REJECT --reject-with tcp-reset",
		sourceHost, grpcPort,
	)

	// Make sure we clean up the iptables rule no matter what.
	t.Cleanup(func() {
		targetExelet.Exec(context.Background(), unblockRule)
	})

	// Start the migration in a goroutine.
	type migrateResult struct {
		body string
		err  error
	}
	resultCh := make(chan migrateResult, 1)
	go func() {
		form := url.Values{
			"box_name":     {boxName},
			"target":       {targetAddr},
			"confirm_name": {boxName},
			"live":         {"true"},
		}
		reqURL := fmt.Sprintf("http://localhost:%d/debug/vms/migrate", serverEnv.Exed.HTTPPort)
		resp, err := http.Post(reqURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if err != nil {
			resultCh <- migrateResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			resultCh <- migrateResult{err: fmt.Errorf("status %d: %s", resp.StatusCode, body)}
			return
		}
		resultCh <- migrateResult{body: string(body)}
	}()

	// Wait for the transfer to start, then block the sideband.
	// Poll the exelet debug page until we see bytes flowing.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		// Check the exelet debug page for an active migration with bytes transferred.
		infoURL := fmt.Sprintf("http://localhost:%d/debug/exelets/%s",
			serverEnv.Exed.HTTPPort, url.PathEscape(sourceBox.Host))
		infoResp, err := http.Get(infoURL)
		if err != nil {
			continue
		}
		infoBody, _ := io.ReadAll(infoResp.Body)
		infoResp.Body.Close()

		// "MiB" or "GiB" in the page means bytes are flowing.
		if strings.Contains(string(infoBody), "MiB") || strings.Contains(string(infoBody), "GiB") {
			break
		}
	}

	t.Log("migration in progress, blocking sideband...")
	out, err := targetExelet.Exec(t.Context(), blockRule)
	if err != nil {
		t.Fatalf("failed to install iptables rule: %v\n%s", err, out)
	}

	// Keep the block for a few seconds so the TCP connection dies
	// and the keepalive detects it, then unblock for the resume.
	time.Sleep(5 * time.Second)
	t.Log("unblocking sideband...")
	out, err = targetExelet.Exec(t.Context(), unblockRule)
	if err != nil {
		t.Logf("warning: failed to remove iptables rule: %v\n%s", err, out)
	}

	// Wait for the migration to complete (with a generous timeout for resume).
	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("migration failed: %v", res.err)
		}
		if !strings.Contains(res.body, "MIGRATION_SUCCESS:"+boxName) {
			t.Fatalf("migration response missing success marker:\n%s", res.body)
		}
		if !strings.Contains(res.body, "MIGRATION_DIRECT_CONFIRMED:") {
			t.Fatalf("migration did not use direct path:\n%s", res.body)
		}
		// Verify the resume actually happened.
		if !strings.Contains(res.body, "transfer interrupted") {
			t.Fatalf("migration succeeded but no resume occurred (expected 'transfer interrupted' in output):\n%s", res.body)
		}
	case <-time.After(5 * time.Minute):
		t.Fatalf("timed out waiting for migration to complete after resume")
	}

	assertBoxEventuallyOnHost(t, boxName, targetAddr)
	assertBoxSSHWorks(t, boxName, keyFile)
}
