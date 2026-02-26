package exelets

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	replicationv1 "exe.dev/pkg/api/exe/replication/v1"
)

func TestReplication(t *testing.T) {
	t.Skip("skipping CI -- this is flakey (evan)")
	// Get a unique test run ID for this test
	testRunID := fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

	// Start VM for the test
	exeletHost, err := testinfra.StartExeletVM(testRunID)
	if err != nil {
		if err == testinfra.ErrNoVM {
			t.Skip("no VM available")
		}
		t.Fatal(err)
	}

	// Setup log file if requested
	var replExeletLogFile *os.File
	logDir := os.Getenv("E1E_LOG_DIR")
	if logDir != "" {
		logDir = filepath.Join(logDir, "replication")
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			t.Logf("failed to create log dir: %v", err)
		} else {
			replExeletLogFile, err = os.OpenFile(filepath.Join(logDir, "exelet"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				t.Logf("failed to open log file: %v", err)
			}
		}
	}

	// Build exelet with replication configured
	// Target is a file path on the same host
	replConfig := &testinfra.ReplicationConfig{
		Enabled:   true,
		Target:    "file:///d/e-" + testRunID + "/repl-backups",
		Interval:  10 * time.Second,
		Retention: 3,
	}

	exelet, err := testinfra.StartExelet(t.Context(), exeletBinary, exeletHost,
		serverEnv.Exed.HTTPPort, testRunID, replExeletLogFile, false, replConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exelet.Stop(context.WithoutCancel(t.Context())) })

	// Restart exed with this exelet
	if err := serverEnv.Exed.Restart(t.Context(), []string{exelet.Address}, testRunID, false); err != nil {
		t.Fatal(err)
	}

	// Create a box
	pty, _, keyFile, email := register(t)
	_ = makeBox(t, pty, keyFile, email)
	disconnect(t, pty)

	client := exelet.Client()

	// Check replication status before triggering
	status, err := client.GetStatus(t.Context(), &replicationv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("failed to get replication status: %v", err)
	}
	t.Logf("replication status: enabled=%v target=%s type=%s interval=%ds",
		status.Status.Enabled, status.Status.Target, status.Status.TargetType, status.Status.IntervalSeconds)

	// Trigger replication explicitly instead of waiting for the ticker
	triggerResp, err := client.TriggerReplication(t.Context(), &replicationv1.TriggerReplicationRequest{})
	if err != nil {
		t.Fatalf("failed to trigger replication: %v", err)
	}
	t.Logf("triggered replication: queued=%d volumes=%v", triggerResp.QueuedCount, triggerResp.VolumeIds)

	// Poll for remote snapshots until at least one appears.
	var snapshots []*replicationv1.ListRemoteSnapshotsResponse
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		snapshots = nil
		stream, err := client.ListRemoteSnapshots(t.Context(), &replicationv1.ListRemoteSnapshotsRequest{Limit: 10})
		if err == nil {
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				snapshots = append(snapshots, resp)
			}
		}
		if len(snapshots) > 0 {
			break
		}

		// Log replication status periodically
		if st, err := client.GetStatus(t.Context(), &replicationv1.GetStatusRequest{}); err == nil {
			t.Logf("waiting for snapshots: workers=%d/%d queue=%d next_run=%ds",
				st.Status.WorkersBusy, st.Status.WorkersTotal, len(st.Status.Queue), st.Status.NextRunSeconds)
			for _, q := range st.Status.Queue {
				t.Logf("  queue: volume=%s state=%s progress=%.0f%% error=%s",
					q.VolumeID, q.State, q.ProgressPercent, q.ErrorMessage)
			}
		}

		time.Sleep(2 * time.Second)
	}

	if len(snapshots) == 0 {
		t.Fatal("expected at least one snapshot after replication cycle")
	}

	t.Logf("found %d remote snapshots", len(snapshots))
	for _, s := range snapshots {
		t.Logf("  volume=%s snapshot=%s", s.VolumeID, s.Snapshot.Name)
	}

	// Box cleanup is handled by exelet.Stop (t.Cleanup) which tears down
	// the entire ZFS dataset tree. We don't call deleteBox here because
	// a concurrent replication cycle may be holding the dataset busy.
}
