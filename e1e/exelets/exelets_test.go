// Package exelets contains tests using multiple exelets.
package exelets

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
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

	exelet, err := testinfra.StartExelet(context.Background(), exeletBinary, exeletHost, exedHTTPProxy.Port(), testRunID, exeletLogFile, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting exelet failed: %v\n", err)
		exit(1)
	}
	exelets = []*testinfra.ExeletInstance{exelet}

	serverEnv, err = testinfra.StartServers(context.Background(),
		[]*testinfra.ExeletInstance{exelet},
		exedHTTPProxy,
		exedLogFile,
		sshPiperLogFile,
		false,
		false,
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
		exelet, err := testinfra.StartExelet(ctx, exeletBinary, exeletHosts[len(exelets)], serverEnv.ExedHTTPProxy.Port(), exeletTestRunIDs[len(exelets)], exeletLogFile, false)
		if err != nil {
			return fmt.Errorf("error starting exelet %d: %v", len(exelets), err)
		}
		exelets = append(exelets, exelet)
	}

	return nil
}
