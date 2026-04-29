// Package billing contains e1e tests that run with billing enabled (SkipBilling=false).
// These tests exercise the real Stripe checkout flow end-to-end.
//
// This package follows the e1e/exelets/ pattern: its own TestMain starts
// a separate exed instance with billing enabled, using an httprr proxy
// to record/replay Stripe API traffic.
package billing

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

var (
	serverEnv   *testinfra.ServerEnv
	stripeProxy *testinfra.StripeProxy
)

var testRunID string

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		fmt.Println("skipping billing e1e tests in short mode")
		return
	}

	if runtime.GOOS != "linux" {
		fmt.Printf("skipping billing e1e tests on %s\n", runtime.GOOS)
		return
	}

	defer testinfra.RunCleanups()
	exit := func(code int) {
		testinfra.RunCleanups()
		os.Exit(code)
	}

	if os.Getenv("CTR_HOST") == "localhost" {
		if err := testinfra.BootstrapLocalhost(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to bootstrap localhost: %v\n", err)
			exit(1)
		}
	}

	testRunID = fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

	// Start the httprr Stripe proxy. In record mode (-httprecord), requests
	// are forwarded to real Stripe; in replay mode, responses come from the
	// cassette file. If the cassette doesn't exist and we're not recording,
	// skip the suite — the cassettes need to be recorded first.
	cassettePath := filepath.Join("testdata", "stripe-checkout.httprr")
	if _, err := os.Stat(cassettePath); os.IsNotExist(err) && os.Getenv("STRIPE_SECRET_KEY") == "" {
		fmt.Println("skipping billing e1e tests: no cassette file and STRIPE_SECRET_KEY not set")
		return
	}
	var err error
	stripeProxy, err = testinfra.StartStripeProxy(cassettePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start stripe proxy: %v\n", err)
		exit(1)
	}
	testinfra.AddCleanup(func() { stripeProxy.Close() })

	// Set env vars so the exec'd exed picks them up via stage.Test().
	// STRIPE_SECRET_KEY enables billing (SkipBilling=false).
	// STRIPE_API_URL routes Stripe calls through our httprr proxy.
	if os.Getenv("STRIPE_SECRET_KEY") == "" {
		// Use a dummy key for replay mode. The proxy serves recorded responses
		// so the key doesn't need to be valid.
		os.Setenv("STRIPE_SECRET_KEY", "sk_test_billing_e1e_replay")
	}
	os.Setenv("STRIPE_API_URL", stripeProxy.URL())

	exedHTTPProxy, err := testinfra.NewTCPProxy(context.Background(), "exedHTTPProxy")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create HTTP proxy: %v\n", err)
		exit(1)
	}
	go exedHTTPProxy.Serve()

	// Boot one exelet VM.
	vmHost, err := testinfra.StartExeletVM(testRunID)
	if err != nil {
		if err == testinfra.ErrNoVM && os.Getenv("CI") != "" {
			fmt.Printf("skipping billing e1e tests in CI: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}

	bins, exeletBinary, err := testinfra.BuildAll(context.Background(), testRunID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "building binaries failed: %v\n", err)
		exit(1)
	}
	// Cleanup of exeletBinary is handled inside testinfra.BuildExeletBinary
	// for non-prebuilt paths. When PREBUILT_EXELET is set (CI), it points to
	// a shared cache used by sibling jobs and must not be deleted.

	exelet, err := testinfra.StartExelet(context.Background(), exeletBinary, vmHost, exedHTTPProxy.Port(), exedHTTPProxy.Port(), nil, testRunID, nil, nil, false, nil, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting exelet failed: %v\n", err)
		exit(1)
	}

	var logDir string
	if d := os.Getenv("E1E_LOG_DIR"); d != "" {
		logDir = filepath.Join(d, "billing")
		os.MkdirAll(logDir, 0o700)
	}

	var exedLog, exeproxLog, piperLog *os.File
	if logDir != "" {
		exedLog, _ = os.OpenFile(filepath.Join(logDir, "exed"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		exeproxLog, _ = os.OpenFile(filepath.Join(logDir, "exeprox"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		piperLog, _ = os.OpenFile(filepath.Join(logDir, "sshpiper"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}

	serverEnv, err = testinfra.StartServers(context.Background(),
		bins,
		[]*testinfra.ExeletInstance{exelet},
		nil, // no exepipe
		[]*testinfra.TCPProxy{exedHTTPProxy},
		exedLog,
		exeproxLog,
		piperLog,
		false, // logPorts
		false, // verboseEmailServer
		nil,   // metricsd
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting servers failed: %v\n", err)
		exit(1)
	}

	m.Run()
}

// register creates a new user via SSH without billing.
// Returns a PTY connected to exed, cookies, SSH key file, and email.
func register(t *testing.T) (pty *testinfra.TestPTY, cookies []*http.Cookie, keyFile, email string) {
	name := strings.ReplaceAll(t.Name(), "/", ".")
	email = name + testinfra.FakeEmailSuffix
	pty, _ = testinfra.MakeTestPTY(t, "", "ssh localhost", true)
	cookies, keyFile, sshCmd, err := serverEnv.RegisterForExeDevWithEmail(t.Context(), pty.PTY(), email, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })
	return pty, cookies, keyFile, email
}

// TestBillingRequired verifies that a new user starts on the basic plan
// (no VMCreate entitlement) and is blocked from creating a VM.
func TestBillingRequired(t *testing.T) {
	pty, _, _, email := register(t)

	// Verify the user starts on the basic plan.
	planID, err := serverEnv.QueryUserPlanByEmail(email)
	if err != nil {
		t.Fatalf("QueryUserPlanByEmail: %v", err)
	}
	if !strings.HasPrefix(planID, "basic:") {
		t.Fatalf("expected basic plan, got %q", planID)
	}

	pty.SendLine("new --name=billing-test-vm")
	pty.WantRE("need an active plan")
	pty.Disconnect()
}

// TestStripeCheckoutE2E exercises the full Stripe checkout flow:
//
//	basic plan → billing gate → Stripe checkout → individual plan → VM creation
func TestStripeCheckoutE2E(t *testing.T) {
	pty, cookies, keyFile, email := register(t)

	// User starts on basic plan — VM creation should be blocked.
	planID, err := serverEnv.QueryUserPlanByEmail(email)
	if err != nil {
		t.Fatalf("QueryUserPlanByEmail (before): %v", err)
	}
	if !strings.HasPrefix(planID, "basic:") {
		t.Fatalf("expected basic plan before checkout, got %q", planID)
	}

	pty.SendLine("new --name=checkout-e2e-vm")
	pty.WantRE("need an active plan")

	// Complete the Stripe checkout flow.
	if err := serverEnv.CompleteStripeCheckout(t.Context(), cookies, stripeProxy); err != nil {
		t.Fatalf("CompleteStripeCheckout: %v", err)
	}

	// Verify the user is now on the individual plan.
	planID, err = serverEnv.QueryUserPlanByEmail(email)
	if err != nil {
		t.Fatalf("QueryUserPlanByEmail (after): %v", err)
	}
	if !strings.HasPrefix(planID, "individual:") {
		t.Fatalf("expected individual plan after checkout, got %q", planID)
	}

	// Reconnect via SSH — the user should now have billing.
	pty.Disconnect()
	pty2, _ := testinfra.MakeTestPTY(t, "", "ssh localhost", true)
	cmd, err := serverEnv.SSHToExeDev(t.Context(), pty2.PTY(), keyFile)
	if err != nil {
		t.Fatalf("SSHToExeDev: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty2.WantPrompt()

	// Create a VM — should succeed with billing active.
	boxName, err := serverEnv.NewBox(t.Name(), testRunID, pty2.PTY())
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	// Wait for the VM to be reachable.
	if err := serverEnv.WaitForBoxSSHServer(t.Context(), boxName, keyFile); err != nil {
		t.Fatalf("WaitForBoxSSHServer: %v", err)
	}
}
