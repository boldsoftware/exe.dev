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

var serverEnv *testinfra.ServerEnv

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
	cassettePath := filepath.Join("testdata", "stripe-proxy.httprr")
	if _, err := os.Stat(cassettePath); os.IsNotExist(err) && os.Getenv("STRIPE_SECRET_KEY") == "" {
		fmt.Println("skipping billing e1e tests: no cassette file and STRIPE_SECRET_KEY not set")
		return
	}
	stripeProxy, err := testinfra.StartStripeProxy(cassettePath)
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
	testinfra.AddCleanup(func() { os.Remove(exeletBinary) })

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

// TestBillingRequired verifies that a user without billing cannot create a VM
// when the exed instance has billing enabled (SkipBilling=false).
func TestBillingRequired(t *testing.T) {
	pty, _, _, _ := register(t)

	pty.SendLine("new --name=billing-test-vm")
	pty.WantRE("Billing Required")
	pty.Disconnect()
}
