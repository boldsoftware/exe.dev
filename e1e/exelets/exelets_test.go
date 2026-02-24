// Package exelets contains tests using multiple exelets.
package exelets

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"exe.dev/e1e/testinfra"
)

// serverEnv describes the current set of running servers.
// Any test that modifies this must not use t.Parallel.
var serverEnv *testinfra.ServerEnv

// exeletBinary is the exelet binary we build.
var exeletBinary string

// exeletLogFile is the file for exelet logs.
var exeletLogFile *os.File

var (
	// exeletsMu proects exeletHosts and exelets.
	exeletsMu sync.Mutex

	// exeletHosts holds all the exelet hosts we've created.
	// These are strings as returned by [testinfra.StartExeletVM].
	exeletHosts []string

	// exelets holds all the exelets we've started.
	exelets []*testinfra.ExeletInstance

	// exeletTestRunIDs is testRunID values for each host.
	exeletTestRunIDs []string
)

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		fmt.Println("skipping tests in short mode")
		return
	}

	// TODO: Adjust these tests so they run on Darwin.
	// The issue is that on Linux we can create VMs as needed.
	// On Darwin they are currently precreated using Lima.
	if runtime.GOOS != "linux" {
		fmt.Printf("skipping tests on %s\n", runtime.GOOS)
		return
	}

	defer testinfra.RunCleanups()
	exit := func(code int) {
		testinfra.RunCleanups()
		os.Exit(code)
	}

	var (
		exedLogFile     *os.File
		exeproxLogFile  *os.File
		sshPiperLogFile *os.File
	)
	logDir := os.Getenv("E1E_LOG_DIR")
	if logDir != "" {
		logDir = filepath.Join(logDir, "exelets")
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create log dir: %v\n", err)
		} else {
			exeletLogFile, err = os.OpenFile(filepath.Join(logDir, "exelet"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			exedLogFile, err = os.OpenFile(filepath.Join(logDir, "exed"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			exeproxLogFile, err = os.OpenFile(filepath.Join(logDir, "exeprox"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			sshPiperLogFile, err = os.OpenFile(filepath.Join(logDir, "sshpiper"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}

	// Use a test ID to avoid name collisions.
	testRunID := fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)
	exeletTestRunIDs = []string{testRunID}

	// Start one exelet as part of setting up other services.
	exeletHost, err := testinfra.StartExeletVM(testRunID)
	if err != nil {
		if err == testinfra.ErrNoVM && os.Getenv("CI") != "" {
			fmt.Printf("skipping tests in CI: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
	exeletHosts = []string{exeletHost}

	exedHTTPProxy, err := testinfra.NewTCPProxy("exedHTTPProxy")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create HTTP proxy: %v\n", err)
		exit(1)
	}
	go exedHTTPProxy.Serve(context.Background())

	exeletBinary, err = testinfra.BuildExeletBinary(testRunID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "building exelet binary failed: %v\n", err)
		exit(1)
	}
	testinfra.AddCleanup(func() {
		os.Remove(exeletBinary)
	})

	exelet, err := testinfra.StartExelet(context.Background(), exeletBinary, exeletHost, exedHTTPProxy.Port(), testRunID, exeletLogFile, false, nil, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting exelet failed: %v\n", err)
		exit(1)
	}
	exelets = []*testinfra.ExeletInstance{exelet}

	serverEnv, err = testinfra.StartServers(context.Background(),
		[]*testinfra.ExeletInstance{exelet},
		exedHTTPProxy,
		exedLogFile,
		exeproxLogFile,
		sshPiperLogFile,
		false,
		false,
		nil,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting servers failed: %v\n", err)
		exit(1)
	}

	m.Run()
}

// ensureExeletCount makes sure we have count exelets.
func ensureExeletCount(ctx context.Context, count int) error {
	exeletsMu.Lock()
	defer exeletsMu.Unlock()

	// We don't want finishing the test to cancel the exelets.
	ctx = context.WithoutCancel(ctx)

	for len(exeletTestRunIDs) < count {
		exeletTestRunIDs = append(exeletTestRunIDs, fmt.Sprintf("%04x", rand.Uint32()&0xFFFF))
	}

	for len(exeletHosts) < count {
		exeletHost, err := testinfra.StartExeletVM(exeletTestRunIDs[len(exeletHosts)])
		if err != nil {
			return fmt.Errorf("error creating VM %d: %v", len(exeletHosts), err)
		}
		exeletHosts = append(exeletHosts, exeletHost)
	}

	for len(exelets) < count {
		exelet, err := testinfra.StartExelet(ctx, exeletBinary, exeletHosts[len(exelets)], serverEnv.ExedHTTPProxy.Port(), exeletTestRunIDs[len(exelets)], exeletLogFile, false, nil, nil)
		if err != nil {
			return fmt.Errorf("error starting exelet %d: %v", len(exelets), err)
		}
		exelets = append(exelets, exelet)
	}

	return nil
}

// register registers with exed, returning HTTP cookies,
// an ssh private key, and a test email address.
// It also registers a PTY that is connected to exed via ssh,
// and that may be used for further exed commands.
func register(t *testing.T) (pty *testinfra.PTY, cookies []*http.Cookie, keyFile, email string) {
	name := strings.ReplaceAll(t.Name(), "/", ".")
	email = name + testinfra.FakeEmailSuffix
	pty, cookies, keyFile = registerEmail(t, email)
	return pty, cookies, keyFile, email
}

// registerEmail is like register, but specifies the email address to use.
func registerEmail(t *testing.T, email string) (pty *testinfra.PTY, cookies []*http.Cookie, keyFile string) {
	pty, _, err := testinfra.MakePTY("", "ssh localhost", true)
	if err != nil {
		t.Fatal(err)
	}
	cookies, keyFile, sshCmd, err := serverEnv.RegisterForExeDevWithEmail(t.Context(), pty, email, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })
	return pty, cookies, keyFile
}

// makeBox makes a new box given a PTY that is connected to exed.
// It returns the name of the new box.
func makeBox(t *testing.T, pty *testinfra.PTY, keyFile, email string) string {
	boxName, err := serverEnv.NewBox(t.Name(), exeletTestRunIDs[0], pty)
	if err != nil {
		t.Fatal(err)
	}

	// Clear out the new-box email.
	if msg, err := serverEnv.Email.WaitForEmail(email); err != nil {
		t.Error(err)
	} else if !strings.Contains(msg.Subject, boxName) {
		t.Errorf("got email subject %q, expected it to contain box name %q", msg.Subject, boxName)
	}

	// Wait for the box to be up and running.
	if err := serverEnv.WaitForBoxSSHServer(t.Context(), boxName, keyFile); err != nil {
		t.Fatal(err)
	}

	return boxName
}

// disconnect disconnects a PTY.
func disconnect(t *testing.T, pty *testinfra.PTY) {
	if err := pty.Disconnect(); err != nil {
		t.Helper()
		t.Error(err)
	}
}

// deleteBox deletes the named box.
func deleteBox(t *testing.T, boxName, keyFile string) {
	pty, _, err := testinfra.MakePTY("", "ssh localhost", true)
	if err != nil {
		t.Fatal(err)
	}
	sshCmd, err := serverEnv.SSHToExeDev(context.WithoutCancel(t.Context()), pty, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	if err := pty.SendLine("rm " + boxName); err != nil {
		t.Fatal(err)
	}
	if err := pty.Want("Deleting"); err != nil {
		t.Fatal(err)
	}
	pty.Reject("internal error")
	if err := pty.Want("success"); err != nil {
		t.Fatal(err)
	}
	if err := pty.WantPrompt(); err != nil {
		t.Fatal(err)
	}
	if err := pty.Disconnect(); err != nil {
		t.Error(err)
	}
}

// boxHosts returns a mapping from exelet hosts to box names on that host.
func boxHosts(t *testing.T) map[string][]string {
	url := fmt.Sprintf("http://localhost:%d/debug/boxes?format=json", serverEnv.Exed.HTTPPort)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s returned status %d", url, resp.StatusCode)
	}

	var boxes []struct {
		Host string `json:"host"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&boxes); err != nil {
		t.Fatalf("failed to JSON decode %s: %v", url, err)
	}

	resp.Body.Close()

	ret := make(map[string][]string)
	for _, box := range boxes {
		ret[box.Host] = append(ret[box.Host], box.Name)
	}

	return ret
}
