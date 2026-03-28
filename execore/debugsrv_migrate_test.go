package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"google.golang.org/grpc"
)

// fakeLiveMigrationServer implements a minimal ComputeServiceServer for testing
// live migration. It can act as either source (SendVM) or target (ReceiveVM).
type fakeLiveMigrationServer struct {
	computeapi.UnimplementedComputeServiceServer
	sshPort               int32
	coldBooted            bool   // controls what ReceiveVMResult.ColdBooted returns
	role                  string // "source" or "target"
	sendPreMetadataStatus bool   // when true, sends a SendVMStatus before metadata (if client accepts)
}

func (f *fakeLiveMigrationServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   computeapi.VMState_RUNNING,
			SSHPort: f.sshPort,
		},
	}, nil
}

func (f *fakeLiveMigrationServer) DeleteInstance(_ context.Context, _ *computeapi.DeleteInstanceRequest) (*computeapi.DeleteInstanceResponse, error) {
	return &computeapi.DeleteInstanceResponse{}, nil
}

// SendVM implements the source side: sends metadata, a data chunk, and complete.
// When sendPreMetadataStatus is true and the client sets AcceptStatus, a
// SendVMStatus message is sent before metadata to simulate a replication wait.
func (f *fakeLiveMigrationServer) SendVM(stream computeapi.ComputeService_SendVMServer) error {
	// Receive start request.
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	startReq := req.GetStart()

	// If configured, send a status frame before metadata (simulates replication wait).
	if f.sendPreMetadataStatus && startReq != nil && startReq.AcceptStatus {
		if err := stream.Send(&computeapi.SendVMResponse{
			Type: &computeapi.SendVMResponse_Status{
				Status: &computeapi.SendVMStatus{
					Message: "waiting for storage replication to complete",
				},
			},
		}); err != nil {
			return err
		}
	}

	// Send metadata.
	if err := stream.Send(&computeapi.SendVMResponse{
		Type: &computeapi.SendVMResponse_Metadata{
			Metadata: &computeapi.SendVMMetadata{
				Instance: &computeapi.Instance{
					ID:    "test-instance",
					Image: "ubuntu:latest",
				},
				BaseImageID: "sha256:fake",
			},
		},
	}); err != nil {
		return err
	}

	// Send a small data chunk.
	if err := stream.Send(&computeapi.SendVMResponse{
		Type: &computeapi.SendVMResponse_Data{
			Data: &computeapi.SendVMDataChunk{
				Data: []byte("testdata"),
			},
		},
	}); err != nil {
		return err
	}

	// Send complete.
	if err := stream.Send(&computeapi.SendVMResponse{
		Type: &computeapi.SendVMResponse_Complete{
			Complete: &computeapi.SendVMComplete{
				Checksum: "fakechecksum",
			},
		},
	}); err != nil {
		return err
	}

	// Return nil to close the stream (EOF).
	return nil
}

// ReceiveVM implements the target side: sends ready, receives data, sends result.
func (f *fakeLiveMigrationServer) ReceiveVM(stream computeapi.ComputeService_ReceiveVMServer) error {
	// Receive start request.
	_, err := stream.Recv()
	if err != nil {
		return err
	}

	// Send ready with network interface.
	if err := stream.Send(&computeapi.ReceiveVMResponse{
		Type: &computeapi.ReceiveVMResponse_Ready{
			Ready: &computeapi.ReceiveVMReady{
				TargetNetwork: &computeapi.NetworkInterface{
					IP: &computeapi.IPAddress{
						IPV4:      "10.0.0.2/24",
						GatewayV4: "10.0.0.1",
					},
				},
			},
		},
	}); err != nil {
		return err
	}

	// Receive data chunks until complete.
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if req.GetComplete() != nil {
			break
		}
	}

	// Send result.
	if err := stream.Send(&computeapi.ReceiveVMResponse{
		Type: &computeapi.ReceiveVMResponse_Result{
			Result: &computeapi.ReceiveVMResult{
				Instance: &computeapi.Instance{
					ID:      "test-instance",
					SSHPort: f.sshPort,
					State:   computeapi.VMState_RUNNING,
				},
				ColdBooted: f.coldBooted,
			},
		},
	}); err != nil {
		return err
	}

	return nil
}

// startFakeMigrationExelet starts a gRPC server with the given
// fakeLiveMigrationServer and returns the address and client.
func startFakeMigrationExelet(t *testing.T, srv *fakeLiveMigrationServer) (string, *exeletclient.Client) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	gs := grpc.NewServer()
	computeapi.RegisterComputeServiceServer(gs, srv)

	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	t.Cleanup(func() { _ = lis.Close() })

	addr := fmt.Sprintf("tcp://%s", lis.Addr().String())
	client, err := exeletclient.NewClient(addr, exeletclient.WithInsecure())
	if err != nil {
		t.Fatalf("failed to create exelet client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return addr, client
}

func TestMigrateVMLiveColdBootedPropagation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		coldBooted bool
	}{
		{"cold_booted_true", true},
		{"cold_booted_false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			ctx := context.Background()

			const sshPort int32 = 22222

			// Set up source exelet.
			sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "source"}
			sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

			// Set up target exelet.
			targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, coldBooted: tt.coldBooted, role: "target"}
			targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)

			// Register both exelets.
			server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
			server.exeletClients[sourceAddr].up.Store(true)
			server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
			server.exeletClients[targetAddr].up.Store(true)

			// Create a box on the source.
			userID := createTestUser(t, server, "migrate-propagation@example.com")
			boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
				userID:        userID,
				ctrhost:       sourceAddr,
				name:          "propagation-vm",
				image:         "ubuntu:latest",
				noShard:       true,
				region:        "pdx",
				allocatedCPUs: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			containerID := "container-propagation"
			err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
				return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
					ID:          boxID,
					ContainerID: &containerID,
					Status:      "running",
					SSHPort:     nil,
				})
			})
			if err != nil {
				t.Fatal(err)
			}

			box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "propagation-vm")
			if err != nil {
				t.Fatal(err)
			}

			var messages []string
			progress := func(format string, args ...any) {
				messages = append(messages, fmt.Sprintf(format, args...))
			}

			gotSSHPort, gotColdBooted, err := server.migrateVMLive(ctx, migrateVMLiveParams{
				source:     sourceClient,
				target:     targetClient,
				instanceID: containerID,
				box:        box,
				progress:   progress,
			})
			if err != nil {
				t.Fatalf("migrateVMLive failed: %v", err)
			}

			if gotColdBooted != tt.coldBooted {
				t.Errorf("coldBooted = %v, want %v", gotColdBooted, tt.coldBooted)
			}

			if gotSSHPort != int64(sshPort) {
				t.Errorf("sshPort = %d, want %d", gotSSHPort, sshPort)
			}

			// Verify progress messages reflect coldBooted status.
			joined := strings.Join(messages, "\n")
			if tt.coldBooted {
				if !strings.Contains(joined, "cold boot fallback") {
					t.Errorf("expected cold boot fallback message in progress, got:\n%s", joined)
				}
			} else {
				if strings.Contains(joined, "cold boot fallback") {
					t.Errorf("unexpected cold boot fallback message in progress:\n%s", joined)
				}
			}
		})
	}
}

func TestMigrateVMLivePreMetadataStatus(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 22222

	// Source sends a status frame before metadata.
	sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "source", sendPreMetadataStatus: true}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)

	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	userID := createTestUser(t, server, "migrate-status@example.com")
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       sourceAddr,
		name:          "status-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	containerID := "container-status"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "running",
			SSHPort:     nil,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "status-vm")
	if err != nil {
		t.Fatal(err)
	}

	var messages []string
	progress := func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	}

	_, _, err = server.migrateVMLive(ctx, migrateVMLiveParams{
		source:     sourceClient,
		target:     targetClient,
		instanceID: containerID,
		box:        box,
		progress:   progress,
	})
	if err != nil {
		t.Fatalf("migrateVMLive failed: %v", err)
	}

	// Verify the status message was surfaced through progress.
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "Source: waiting for storage replication to complete") {
		t.Errorf("expected replication status in progress messages, got:\n%s", joined)
	}
}

func TestMigrateVMColdPreMetadataStatus(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	// Source sends a status frame before metadata.
	sourceSrv := &fakeLiveMigrationServer{role: "source", sendPreMetadataStatus: true}
	_, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	targetSrv := &fakeLiveMigrationServer{role: "target"}
	_, targetClient := startFakeMigrationExelet(t, targetSrv)

	var messages []string
	progress := func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	}

	err := server.migrateVM(ctx, sourceClient, targetClient, "test-instance", false, progress)
	if err != nil {
		t.Fatalf("migrateVM failed: %v", err)
	}

	// Verify the status message was surfaced through progress.
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "Source: waiting for storage replication to complete") {
		t.Errorf("expected replication status in progress messages, got:\n%s", joined)
	}
}

func TestMigrateBoxColdBootSendsEmail(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	// Set up a fake email server to capture the maintenance email.
	// sendBoxMaintenanceEmail runs in a goroutine, so we use a channel to
	// synchronize.
	type capturedEmail struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	emailCh := make(chan capturedEmail, 1)
	fakeEmailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e capturedEmail
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Errorf("fake email server: failed to decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		emailCh <- e
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeEmailSrv.Close)
	server.fakeHTTPEmail = fakeEmailSrv.URL

	const sshPort int32 = 33333

	// Set up source exelet (handles SendVM, GetInstance, DeleteInstance).
	sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "source"}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	// Set up target exelet (handles ReceiveVM, GetInstance; coldBooted=true).
	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, coldBooted: true, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)

	// Register both exelets.
	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	// Create a box with a non-bogus email domain so the email reaches the
	// fake HTTP email server (example.com is in the bogus list).
	const boxName = "coldboot-email-vm"
	userID := createTestUser(t, server, boxName+"@boldvm.com")
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       sourceAddr,
		name:          boxName,
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	containerID := "container-" + boxName
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "running",
			SSHPort:     nil,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// POST to the debug migrate handler.
	form := url.Values{
		"box_name":     {boxName},
		"target":       {targetAddr},
		"confirm_name": {boxName},
	}
	req := httptest.NewRequest("POST", "/debug/vms/migrate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	server.handleDebugBoxMigrate(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "WARNING: Live migration fell back to cold boot") {
		t.Errorf("expected cold-boot warning in response body, got:\n%s", body)
	}

	if !strings.Contains(body, "MIGRATION_SUCCESS") {
		t.Errorf("expected MIGRATION_SUCCESS in response body, got:\n%s", body)
	}

	if strings.Contains(body, "MIGRATION_ERROR") {
		t.Errorf("unexpected MIGRATION_ERROR in response body:\n%s", body)
	}

	// Wait for the maintenance email sent in the background goroutine.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case got := <-emailCh:
		if got.To != boxName+"@boldvm.com" {
			t.Errorf("email to = %q, want %q", got.To, boxName+"@boldvm.com")
		}
		wantSubject := "exe.dev: system maintenance on " + boxName
		if got.Subject != wantSubject {
			t.Errorf("email subject = %q, want %q", got.Subject, wantSubject)
		}
		if !strings.Contains(got.Body, "was rebooted as part of routine system maintenance") {
			t.Errorf("email body missing maintenance text, got: %s", got.Body)
		}
		if !strings.Contains(got.Body, boxName) {
			t.Errorf("email body missing box name %q, got: %s", boxName, got.Body)
		}
	case <-timer.C:
		t.Fatal("timed out waiting for maintenance email")
	}
}
