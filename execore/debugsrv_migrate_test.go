package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/region"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLiveMigrationServer implements a minimal ComputeServiceServer for testing
// migration via the unary RPCs (InitSendVM/PollSendVM/SubmitSendVMControl/AbortSendVM).
// It can also act as a target (GetInstance, StartInstance, etc.).
type fakeLiveMigrationServer struct {
	computeapi.UnimplementedComputeServiceServer
	sshPort               int32
	coldBooted            bool   // controls what SendVMResult.ColdBooted returns
	role                  string // "source" or "target"
	sendPreMetadataStatus bool   // when true, sends a SendVMStatus before metadata
	deleteErrs            []error
	deleteCalls           int

	// Migration session state (source role, set by InitSendVM).
	mu      sync.Mutex
	events  []*computeapi.SendVMEvent
	nextSeq uint64
}

func (f *fakeLiveMigrationServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   computeapi.VMState_RUNNING,
			SSHPort: f.sshPort,
		},
	}, nil
}

func (f *fakeLiveMigrationServer) StartInstance(_ context.Context, _ *computeapi.StartInstanceRequest) (*computeapi.StartInstanceResponse, error) {
	return &computeapi.StartInstanceResponse{}, nil
}

func (f *fakeLiveMigrationServer) StopInstance(_ context.Context, _ *computeapi.StopInstanceRequest) (*computeapi.StopInstanceResponse, error) {
	return &computeapi.StopInstanceResponse{}, nil
}

func (f *fakeLiveMigrationServer) DeleteInstance(_ context.Context, _ *computeapi.DeleteInstanceRequest) (*computeapi.DeleteInstanceResponse, error) {
	f.deleteCalls++
	if len(f.deleteErrs) > 0 {
		err := f.deleteErrs[0]
		f.deleteErrs = f.deleteErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &computeapi.DeleteInstanceResponse{}, nil
}

// addEvent is a helper that appends an event with an auto-incremented sequence number.
func (f *fakeLiveMigrationServer) addEvent(evt *computeapi.SendVMEvent) {
	f.nextSeq++
	evt.Seq = f.nextSeq
	f.events = append(f.events, evt)
}

// InitSendVM prepares the migration event sequence and returns a session ID.
func (f *fakeLiveMigrationServer) InitSendVM(_ context.Context, req *computeapi.InitSendVMRequest) (*computeapi.InitSendVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events = nil
	f.nextSeq = 0

	// Both live (direct) and cold (direct) paths send TargetReady first
	// because InitSendVM always sets TargetAddress in production.
	f.addEvent(&computeapi.SendVMEvent{
		Type: &computeapi.SendVMEvent_TargetReady{
			TargetReady: &computeapi.SendVMTargetReady{
				TargetNetwork: &computeapi.NetworkInterface{
					IP: &computeapi.IPAddress{
						IPV4:      "10.0.0.2/24",
						GatewayV4: "10.0.0.1",
					},
				},
			},
		},
	})

	// Optional pre-metadata status.
	if f.sendPreMetadataStatus {
		f.addEvent(&computeapi.SendVMEvent{
			Type: &computeapi.SendVMEvent_Status{
				Status: &computeapi.SendVMStatus{
					Message: "waiting for storage replication to complete",
				},
			},
		})
	}

	// Metadata.
	f.addEvent(&computeapi.SendVMEvent{
		Type: &computeapi.SendVMEvent_Metadata{
			Metadata: &computeapi.SendVMMetadata{
				Instance: &computeapi.Instance{
					ID:    "test-instance",
					Image: "ubuntu:latest",
				},
				BaseImageID: "sha256:fake",
			},
		},
	})

	// Result event — available immediately for both live and cold paths.
	// (The real source handles target interaction internally; execore just sees the result.)
	f.addEvent(&computeapi.SendVMEvent{
		Type: &computeapi.SendVMEvent_Result{
			Result: &computeapi.SendVMResult{
				Instance: &computeapi.Instance{
					ID:      "test-instance",
					SSHPort: f.sshPort,
					State:   computeapi.VMState_RUNNING,
				},
				ColdBooted: f.coldBooted,
			},
		},
	})

	return &computeapi.InitSendVMResponse{SessionID: "test-session"}, nil
}

// PollSendVM returns events with seq > after_seq.
func (f *fakeLiveMigrationServer) PollSendVM(_ context.Context, req *computeapi.PollSendVMRequest) (*computeapi.PollSendVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var result []*computeapi.SendVMEvent
	var hasResult bool
	for _, e := range f.events {
		if e.Seq > req.AfterSeq {
			result = append(result, e)
		}
		if _, ok := e.Type.(*computeapi.SendVMEvent_Result); ok {
			hasResult = true
		}
	}

	return &computeapi.PollSendVMResponse{
		Events:    result,
		Completed: hasResult,
	}, nil
}

// SubmitSendVMControl is a no-op — the fake includes the Result in the initial event list.
func (f *fakeLiveMigrationServer) SubmitSendVMControl(_ context.Context, _ *computeapi.SubmitSendVMControlRequest) (*computeapi.SubmitSendVMControlResponse, error) {
	return &computeapi.SubmitSendVMControlResponse{}, nil
}

// AbortSendVM cancels the migration session.
func (f *fakeLiveMigrationServer) AbortSendVM(_ context.Context, _ *computeapi.AbortSendVMRequest) (*computeapi.AbortSendVMResponse, error) {
	return &computeapi.AbortSendVMResponse{}, nil
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

			// Set up source exelet (handles InitSendVM/PollSendVM/SubmitSendVMControl).
			sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, coldBooted: tt.coldBooted, role: "source"}
			sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

			// Set up target exelet (handles GetInstance, StartInstance, etc.).
			targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
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
				targetAddr: targetAddr,
				instanceID: containerID,
				box:        box,
				progress:   progress,
				directOnly: false,
				sudoPrefix: "",
				guestShell: "",
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
		targetAddr: targetAddr,
		instanceID: containerID,
		box:        box,
		progress:   progress,
		directOnly: false,
		sudoPrefix: "",
		guestShell: "",
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
	targetAddr, _ := startFakeMigrationExelet(t, targetSrv)

	var messages []string
	progress := func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	}

	err := server.migrateVM(ctx, sourceClient, "test-instance", "tcp://source:9080", targetAddr, "test-box", false, false, nil, progress)
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

	// Set up source exelet (handles InitSendVM/PollSendVM/SubmitSendVMControl, GetInstance, DeleteInstance).
	sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, coldBooted: true, role: "source"}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	// Set up target exelet (handles GetInstance, StartInstance).
	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
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

func TestHandleDebugBoxMigrateRetriesSourceDeleteWhileMigrationUnlocks(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 33333
	sourceSrv := &fakeLiveMigrationServer{
		sshPort: sshPort,
		role:    "source",
		deleteErrs: []error{
			status.Error(codes.FailedPrecondition, "instance is being migrated"),
			nil,
		},
	}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)

	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	const boxName = "delete-retry-vm"
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

	form := url.Values{
		"box_name":     {boxName},
		"target":       {targetAddr},
		"live":         {"true"},
		"confirm_name": {boxName},
	}
	req := httptest.NewRequest("POST", "/debug/vms/migrate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	server.handleDebugBoxMigrate(w, req)
	body := w.Body.String()

	if sourceSrv.deleteCalls != 2 {
		t.Fatalf("DeleteInstance calls = %d, want 2\nbody:\n%s", sourceSrv.deleteCalls, body)
	}
	if strings.Contains(body, "WARNING: failed to delete source instance") {
		t.Fatalf("unexpected source delete warning after retry:\n%s", body)
	}
	if !strings.Contains(body, "Source instance deleted.") {
		t.Fatalf("expected successful source delete message, got:\n%s", body)
	}
	if !strings.Contains(body, "MIGRATION_SUCCESS:") {
		t.Fatalf("expected migration success, got:\n%s", body)
	}
}

func TestHandleDebugBoxMigrateSSHFailureFallsBackToCold(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 33333

	// Source and target exelets.
	sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "source"}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)

	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)

	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	// Set up a fake email server to verify the maintenance email is sent.
	type capturedEmail struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	emailCh := make(chan capturedEmail, 1)
	fakeEmailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e capturedEmail
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		emailCh <- e
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeEmailSrv.Close)
	server.fakeHTTPEmail = fakeEmailSrv.URL

	const boxName = "ssh-fallback-vm"
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

	// Set container ID and SSH fields so the SSH pre-check is attempted.
	// The SSH connection will fail because no real SSH server is listening.
	containerID := "container-" + boxName
	sshPortVal := int64(sshPort)
	sshUser := "user"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:                   boxID,
			ContainerID:          &containerID,
			Status:               "running",
			SSHPort:              &sshPortVal,
			SSHUser:              &sshUser,
			SSHClientPrivateKey:  []byte("not-a-real-key"),
			SSHServerIdentityKey: []byte("not-a-real-key"),
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// POST to the debug migrate handler. Don't set live=false — let it
	// default to live (because VM is running), then fall back.
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

	// Should NOT have a MIGRATION_ERROR.
	if strings.Contains(body, "MIGRATION_ERROR") {
		t.Fatalf("expected migration to succeed with cold fallback, got error:\n%s", body)
	}

	// Should have MIGRATION_SUCCESS.
	if !strings.Contains(body, "MIGRATION_SUCCESS") {
		t.Fatalf("expected MIGRATION_SUCCESS in response body, got:\n%s", body)
	}

	// Should mention SSH pre-check failure and cold fallback.
	if !strings.Contains(body, "SSH pre-check failed") {
		t.Errorf("expected SSH pre-check failure message, got:\n%s", body)
	}
	if !strings.Contains(body, "falling back to cold migration") {
		t.Errorf("expected cold migration fallback message, got:\n%s", body)
	}

	// Should NOT mention live migration (it should have used cold path).
	if strings.Contains(body, "Starting live migration") {
		t.Errorf("expected cold migration, but saw live migration message:\n%s", body)
	}

	// Maintenance email should be sent (VM was rebooted via cold migration).
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
	case <-timer.C:
		t.Fatal("timed out waiting for maintenance email")
	}
}

// fakeStoppedSourceServer simulates a source exelet where the VM transitions
// from RUNNING to a failed state during migration. GetInstance returns RUNNING
// on the first call (pre-migration check), then the configured postMigrationState
// on subsequent calls (simulating the VM crashing during migration).
type fakeStoppedSourceServer struct {
	computeapi.UnimplementedComputeServiceServer
	postMigrationState computeapi.VMState
	getInstanceCalls   int
	events             []*computeapi.SendVMEvent
}

func (f *fakeStoppedSourceServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	f.getInstanceCalls++
	state := computeapi.VMState_RUNNING
	if f.getInstanceCalls > 1 {
		state = f.postMigrationState
	}
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   state,
			SSHPort: 22222,
		},
	}, nil
}

func (f *fakeStoppedSourceServer) StopInstance(_ context.Context, _ *computeapi.StopInstanceRequest) (*computeapi.StopInstanceResponse, error) {
	return &computeapi.StopInstanceResponse{}, nil
}

func (f *fakeStoppedSourceServer) StartInstance(_ context.Context, _ *computeapi.StartInstanceRequest) (*computeapi.StartInstanceResponse, error) {
	return &computeapi.StartInstanceResponse{}, nil
}

func (f *fakeStoppedSourceServer) DeleteInstance(_ context.Context, _ *computeapi.DeleteInstanceRequest) (*computeapi.DeleteInstanceResponse, error) {
	return &computeapi.DeleteInstanceResponse{}, nil
}

// InitSendVM simulates a live migration that sends TargetReady and Metadata,
// then fails with a Result error (simulating a target-side failure).
func (f *fakeStoppedSourceServer) InitSendVM(_ context.Context, _ *computeapi.InitSendVMRequest) (*computeapi.InitSendVMResponse, error) {
	f.events = []*computeapi.SendVMEvent{
		{
			Seq: 1,
			Type: &computeapi.SendVMEvent_TargetReady{
				TargetReady: &computeapi.SendVMTargetReady{
					TargetNetwork: &computeapi.NetworkInterface{
						IP: &computeapi.IPAddress{
							IPV4:      "10.0.0.2/24",
							GatewayV4: "10.0.0.1",
						},
					},
				},
			},
		},
		{
			Seq: 2,
			Type: &computeapi.SendVMEvent_Metadata{
				Metadata: &computeapi.SendVMMetadata{
					Instance: &computeapi.Instance{
						ID:    "test-instance",
						Image: "ubuntu:latest",
						VMConfig: &computeapi.VMConfig{
							NetworkInterface: &computeapi.NetworkInterface{
								IP: &computeapi.IPAddress{IPV4: "10.0.0.99/24"},
							},
						},
					},
				},
			},
		},
		{
			Seq: 3,
			Type: &computeapi.SendVMEvent_Result{
				Result: &computeapi.SendVMResult{
					Error: "target restore failed: simulated error",
				},
			},
		},
	}
	return &computeapi.InitSendVMResponse{SessionID: "test-session"}, nil
}

// PollSendVM returns events with seq > after_seq.
func (f *fakeStoppedSourceServer) PollSendVM(_ context.Context, req *computeapi.PollSendVMRequest) (*computeapi.PollSendVMResponse, error) {
	var result []*computeapi.SendVMEvent
	for _, e := range f.events {
		if e.Seq > req.AfterSeq {
			result = append(result, e)
		}
	}
	return &computeapi.PollSendVMResponse{Events: result, Completed: true}, nil
}

func (f *fakeStoppedSourceServer) AbortSendVM(_ context.Context, _ *computeapi.AbortSendVMRequest) (*computeapi.AbortSendVMResponse, error) {
	return &computeapi.AbortSendVMResponse{}, nil
}

func TestRestartSourceVMUpdatesDBStatusWhenVMStopped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		boxName string
		vmState computeapi.VMState
	}{
		{"vm_stopped", "restart-vmstopped", computeapi.VMState_STOPPED},
		{"vm_paused", "restart-vmpaused", computeapi.VMState_PAUSED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newTestServer(t)
			ctx := context.Background()

			// Set up source exelet that reports VM as stopped/paused.
			// First GetInstance call returns RUNNING (pre-check), subsequent calls
			// return the configured postMigrationState.
			sourceSrv := &fakeStoppedSourceServer{postMigrationState: tt.vmState}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("failed to listen: %v", err)
			}
			gs := grpc.NewServer()
			computeapi.RegisterComputeServiceServer(gs, sourceSrv)
			go gs.Serve(lis)
			t.Cleanup(gs.Stop)
			t.Cleanup(func() { _ = lis.Close() })

			sourceAddr := fmt.Sprintf("tcp://%s", lis.Addr().String())
			sourceClient, err := exeletclient.NewClient(sourceAddr, exeletclient.WithInsecure())
			if err != nil {
				t.Fatalf("failed to create exelet client: %v", err)
			}
			t.Cleanup(func() { sourceClient.Close() })

			server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
			server.exeletClients[sourceAddr].up.Store(true)

			// Create a box in "running" state.
			userID := createTestUser(t, server, tt.name+"@example.com")
			boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
				userID:        userID,
				ctrhost:       sourceAddr,
				name:          tt.boxName,
				image:         "ubuntu:latest",
				noShard:       true,
				region:        "pdx",
				allocatedCPUs: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			containerID := "container-" + tt.name
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

			// Verify it starts as "running".
			box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, tt.boxName)
			if err != nil {
				t.Fatal(err)
			}
			if box.Status != "running" {
				t.Fatalf("box status = %q, want running", box.Status)
			}

			// Simulate that GetInstance was already called once (pre-migration check)
			// so subsequent calls return the post-migration state.
			sourceSrv.getInstanceCalls = 1

			var messages []string
			progress := func(format string, args ...any) {
				messages = append(messages, fmt.Sprintf(format, args...))
			}

			// Call restartSourceVM simulating a failed live migration.
			server.restartSourceVM(ctx, server.exeletClients[sourceAddr], containerID,
				tt.boxName, sourceAddr, "", "migration failed",
				true, true, box.ID, progress)

			// Verify DB status was updated to "stopped".
			box, err = withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, tt.boxName)
			if err != nil {
				t.Fatal(err)
			}
			if box.Status != "stopped" {
				joined := strings.Join(messages, "\n")
				t.Fatalf("box status = %q, want stopped\nprogress:\n%s", box.Status, joined)
			}

			// Verify progress messages mention the DB update.
			joined := strings.Join(messages, "\n")
			if !strings.Contains(joined, "Updated DB status to stopped") {
				t.Errorf("expected DB update message in progress, got:\n%s", joined)
			}
		})
	}
}

// TestHandleDebugBoxMigrateFailedLiveUpdatesDB verifies that when a live
// migration fails and the source VM is stopped, handleDebugBoxMigrate
// updates the DB status to "stopped" instead of leaving it as "running".
func TestHandleDebugBoxMigrateFailedLiveUpdatesDB(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	// Source exelet: GetInstance returns RUNNING first (pre-migration check),
	// then STOPPED after migration fails. SendVM returns an error.
	sourceSrv := &fakeStoppedSourceServer{postMigrationState: computeapi.VMState_STOPPED}
	sourceAddr, sourceClient := startFakeComputeServer(t, sourceSrv)

	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)

	// Target exelet (the migration won't reach it, but handler looks it up).
	targetSrv := &fakeLiveMigrationServer{sshPort: 22222, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	// Create a box.
	const boxName = "failed-live-db"
	userID := createTestUser(t, server, boxName+"@example.com")
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

	// Migration should have failed.
	if !strings.Contains(body, "MIGRATION_ERROR") {
		t.Fatalf("expected MIGRATION_ERROR in response, got:\n%s", body)
	}

	// Verify DB status was updated to "stopped".
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		t.Fatal(err)
	}
	if box.Status != "stopped" {
		t.Fatalf("box status = %q, want \"stopped\" (DB was not updated after failed migration)\nresponse:\n%s", box.Status, body)
	}
}

func TestHandleDebugCancelMigration(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// No active migration — should 404.
	form := url.Values{"box_name": {"nonexistent-vm"}}
	req := httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}

	// Missing params — should 400.
	req = httptest.NewRequest("POST", "/debug/vms/cancel-migration", nil)
	w = httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	// Start a tracked migration and cancel it.
	ctx := server.liveMigrations.start(context.Background(), "cancel-test-box", "tcp://src:9080", "tcp://dst:9080", true)
	if ctx.Err() != nil {
		t.Fatal("expected context to be active")
	}

	form = url.Values{"box_name": {"cancel-test-box"}}
	req = httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if ctx.Err() == nil {
		t.Error("expected migration context to be cancelled")
	}
	if !server.liveMigrations.cancelled("cancel-test-box") {
		t.Error("expected migration to be marked cancelled")
	}

	// Cleanup.
	server.liveMigrations.finish("cancel-test-box")
}

func TestHandleDebugCancelBatchMigration(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// No active batch — should 404.
	form := url.Values{"user_id": {"no-such-user"}}
	req := httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}

	// Start a batch and cancel it.
	ctx := server.liveMigrations.startBatch(context.Background(), "user-456")
	if ctx.Err() != nil {
		t.Fatal("expected batch context to be active")
	}

	form = url.Values{"user_id": {"user-456"}}
	req = httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if ctx.Err() == nil {
		t.Error("expected batch context to be cancelled")
	}

	// Cleanup.
	server.liveMigrations.finishBatch("user-456")
}

// slowMigrationServer blocks during SendVM until the context is cancelled,
// allowing tests to exercise the cancel path.
type slowMigrationServer struct {
	computeapi.UnimplementedComputeServiceServer
	sshPort int32
}

func (f *slowMigrationServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   computeapi.VMState_RUNNING,
			SSHPort: f.sshPort,
		},
	}, nil
}

func (f *slowMigrationServer) StopInstance(_ context.Context, _ *computeapi.StopInstanceRequest) (*computeapi.StopInstanceResponse, error) {
	return &computeapi.StopInstanceResponse{}, nil
}

func (f *slowMigrationServer) StartInstance(_ context.Context, _ *computeapi.StartInstanceRequest) (*computeapi.StartInstanceResponse, error) {
	return &computeapi.StartInstanceResponse{}, nil
}

func (f *slowMigrationServer) DeleteInstance(_ context.Context, _ *computeapi.DeleteInstanceRequest) (*computeapi.DeleteInstanceResponse, error) {
	return &computeapi.DeleteInstanceResponse{}, nil
}

func (f *slowMigrationServer) InitSendVM(_ context.Context, _ *computeapi.InitSendVMRequest) (*computeapi.InitSendVMResponse, error) {
	return &computeapi.InitSendVMResponse{SessionID: "test-session"}, nil
}

// PollSendVM returns TargetReady + Metadata on the first call, then blocks forever
// (simulating a long transfer) until the context is cancelled.
func (f *slowMigrationServer) PollSendVM(ctx context.Context, req *computeapi.PollSendVMRequest) (*computeapi.PollSendVMResponse, error) {
	if req.AfterSeq == 0 {
		return &computeapi.PollSendVMResponse{
			Events: []*computeapi.SendVMEvent{
				{
					Seq: 1,
					Type: &computeapi.SendVMEvent_TargetReady{
						TargetReady: &computeapi.SendVMTargetReady{},
					},
				},
				{
					Seq: 2,
					Type: &computeapi.SendVMEvent_Metadata{
						Metadata: &computeapi.SendVMMetadata{
							Instance: &computeapi.Instance{
								ID:    "test-instance",
								Image: "ubuntu:latest",
							},
							BaseImageID: "sha256:fake",
						},
					},
				},
			},
		}, nil
	}
	// Block until context is done (simulating a long transfer).
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *slowMigrationServer) AbortSendVM(_ context.Context, _ *computeapi.AbortSendVMRequest) (*computeapi.AbortSendVMResponse, error) {
	return &computeapi.AbortSendVMResponse{}, nil
}

func TestCancelMigrationMidFlight(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 22222

	// Source that blocks during transfer.
	sourceSrv := &slowMigrationServer{sshPort: sshPort}
	sourceAddr, sourceClient := startFakeComputeServer(t, sourceSrv)

	server.exeletClients[sourceAddr] = &exeletClient{addr: sourceAddr, client: sourceClient}
	server.exeletClients[sourceAddr].up.Store(true)

	// We don't need a real target for this test since the source blocks before
	// contacting the target. Register a fake target address.
	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)
	server.exeletClients[targetAddr] = &exeletClient{addr: targetAddr, client: targetClient}
	server.exeletClients[targetAddr].up.Store(true)

	// Create a box.
	const boxName = "cancel-midflight"
	userID := createTestUser(t, server, boxName+"@example.com")
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

	// Start migration in a goroutine.
	done := make(chan string, 1)
	go func() {
		form := url.Values{
			"box_name":     {boxName},
			"target":       {targetAddr},
			"confirm_name": {boxName},
			"live":         {"false"},
		}
		req := httptest.NewRequest("POST", "/debug/vms/migrate", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		server.handleDebugBoxMigrate(w, req)
		done <- w.Body.String()
	}()

	// Wait for the migration to appear in the tracker.
	for i := 0; i < 100; i++ {
		if snap := server.liveMigrations.snapshot(); len(snap) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap := server.liveMigrations.snapshot(); len(snap) == 0 {
		t.Fatal("migration did not appear in tracker")
	}

	// Cancel it.
	if !server.liveMigrations.cancel(boxName) {
		t.Fatal("cancel returned false")
	}

	// Wait for the migration to complete.
	select {
	case body := <-done:
		if !strings.Contains(body, "MIGRATION_ERROR") {
			t.Errorf("expected MIGRATION_ERROR after cancel, got:\n%s", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for migration to finish after cancel")
	}
}

func TestHandleDebugBatchMigrate(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 44444

	// Set up source exelet in pdx.
	sourceSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "source"}
	sourceAddr, sourceClient := startFakeMigrationExelet(t, sourceSrv)
	sourceEC := &exeletClient{addr: sourceAddr, client: sourceClient}
	sourceEC.region, _ = region.ByCode("pdx")
	sourceEC.up.Store(true)
	server.exeletClients[sourceAddr] = sourceEC

	// Set up target exelet in lon.
	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)
	targetEC := &exeletClient{addr: targetAddr, client: targetClient}
	targetEC.region, _ = region.ByCode("lon")
	targetEC.up.Store(true)
	server.exeletClients[targetAddr] = targetEC

	// Create two users, both with region lon, each with a box on pdx.
	user1ID := createTestUser(t, server, "batch-user1@example.com")
	err := withTx1(server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		Region: "lon",
		UserID: user1ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	user2ID := createTestUser(t, server, "batch-user2@example.com")
	err = withTx1(server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		Region: "lon",
		UserID: user2ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create boxes for user1.
	box1ID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        user1ID,
		ctrhost:       sourceAddr,
		name:          "batch-vm1",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cid1 := "container-batch-vm1"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID: box1ID, ContainerID: &cid1, Status: "running",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create boxes for user2.
	box2ID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        user2ID,
		ctrhost:       sourceAddr,
		name:          "batch-vm2",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cid2 := "container-batch-vm2"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID: box2ID, ContainerID: &cid2, Status: "running",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// POST to the batch migrate endpoint.
	form := url.Values{
		"user_ids[]":  {user1ID, user2ID},
		"concurrency": {"2"},
	}
	req := httptest.NewRequest("POST", "/debug/migrations/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugBatchMigrate(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "BATCH_SUCCESS") {
		t.Errorf("expected BATCH_SUCCESS in response, got:\n%s", body)
	}
	if strings.Contains(body, "BATCH_ERROR") {
		t.Errorf("unexpected BATCH_ERROR in response:\n%s", body)
	}
	if !strings.Contains(body, "BATCH_ID:") {
		t.Errorf("expected BATCH_ID in response, got:\n%s", body)
	}
	if !strings.Contains(body, "VMs succeeded: 2") {
		t.Errorf("expected 2 VMs succeeded, got:\n%s", body)
	}

	// Verify both boxes were moved to the target.
	box1, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "batch-vm1")
	if err != nil {
		t.Fatal(err)
	}
	if box1.Ctrhost != targetAddr {
		t.Errorf("box1 ctrhost = %q, want %q", box1.Ctrhost, targetAddr)
	}
	if box1.Region != "lon" {
		t.Errorf("box1 region = %q, want %q", box1.Region, "lon")
	}

	box2, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "batch-vm2")
	if err != nil {
		t.Fatal(err)
	}
	if box2.Ctrhost != targetAddr {
		t.Errorf("box2 ctrhost = %q, want %q", box2.Ctrhost, targetAddr)
	}
	if box2.Region != "lon" {
		t.Errorf("box2 region = %q, want %q", box2.Region, "lon")
	}
}

func TestHandleDebugBatchMigrateCancel(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 44445

	// Source that blocks during transfer.
	sourceSrv := &slowMigrationServer{sshPort: sshPort}
	sourceAddr, sourceClient := startFakeComputeServer(t, sourceSrv)
	sourceEC := &exeletClient{addr: sourceAddr, client: sourceClient}
	sourceEC.region, _ = region.ByCode("pdx")
	sourceEC.up.Store(true)
	server.exeletClients[sourceAddr] = sourceEC

	// Target exelet.
	targetSrv := &fakeLiveMigrationServer{sshPort: sshPort, role: "target"}
	targetAddr, targetClient := startFakeMigrationExelet(t, targetSrv)
	targetEC := &exeletClient{addr: targetAddr, client: targetClient}
	targetEC.region, _ = region.ByCode("lon")
	targetEC.up.Store(true)
	server.exeletClients[targetAddr] = targetEC

	// Create a user with a box on pdx, region lon.
	userID := createTestUser(t, server, "batch-cancel@example.com")
	err := withTx1(server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		Region: "lon",
		UserID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       sourceAddr,
		name:          "batch-cancel-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cid := "container-batch-cancel"
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID: boxID, ContainerID: &cid, Status: "running",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start migration in a goroutine.
	done := make(chan string, 1)
	go func() {
		form := url.Values{
			"user_ids[]":  {userID},
			"concurrency": {"1"},
		}
		req := httptest.NewRequest("POST", "/debug/migrations/batch", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		server.handleDebugBatchMigrate(w, req)
		done <- w.Body.String()
	}()

	// Wait for the individual VM migration to appear in the tracker.
	// This ensures the gRPC stream is active before we cancel.
	for i := 0; i < 200; i++ {
		if snap := server.liveMigrations.snapshot(); len(snap) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap := server.liveMigrations.snapshot(); len(snap) == 0 {
		t.Fatal("migration did not appear in tracker")
	}

	// Find the batch ID and cancel it.
	var foundBatchID string
	server.liveMigrations.mu.Lock()
	for key := range server.liveMigrations.batchCancels {
		if strings.HasPrefix(key, "batch-") {
			foundBatchID = key
		}
	}
	server.liveMigrations.mu.Unlock()
	if foundBatchID == "" {
		t.Fatal("batch ID not found in tracker")
	}

	// Cancel the batch.
	if !server.liveMigrations.cancelBatch(foundBatchID) {
		t.Fatal("cancelBatch returned false")
	}

	// Wait for completion.
	select {
	case body := <-done:
		if !strings.Contains(body, "BATCH_CANCELLED") && !strings.Contains(body, "BATCH_ERROR") {
			t.Errorf("expected BATCH_CANCELLED or BATCH_ERROR after cancel, got:\n%s", body)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for batch to finish after cancel")
	}
}

func TestHandleDebugBatchMigrateNoUsers(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// POST with no user_ids should 400.
	req := httptest.NewRequest("POST", "/debug/migrations/batch", nil)
	w := httptest.NewRecorder()
	server.handleDebugBatchMigrate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleDebugCancelBatchByBatchID(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// No active batch — should 404.
	form := url.Values{"batch_id": {"batch-nonexistent"}}
	req := httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}

	// Start a batch and cancel it.
	batchCtx := server.liveMigrations.startBatch(context.Background(), "batch-test123")
	if batchCtx.Err() != nil {
		t.Fatal("expected batch context to be active")
	}

	form = url.Values{"batch_id": {"batch-test123"}}
	req = httptest.NewRequest("POST", "/debug/vms/cancel-migration", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugCancelMigration(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if batchCtx.Err() == nil {
		t.Error("expected batch context to be cancelled")
	}

	// Cleanup.
	server.liveMigrations.finishBatch("batch-test123")
}

func TestHandleDebugCancelAllMigrations(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Start two migrations and a batch.
	ctx1 := server.liveMigrations.start(context.Background(), "all-box-1", "tcp://s:1", "tcp://d:1", true)
	ctx2 := server.liveMigrations.start(context.Background(), "all-box-2", "tcp://s:2", "tcp://d:2", false)
	batchCtx := server.liveMigrations.startBatch(context.Background(), "user-all-test")

	req := httptest.NewRequest("POST", "/debug/migrations/cancel-all", nil)
	w := httptest.NewRecorder()
	server.handleDebugCancelAllMigrations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Verify response JSON.
	var resp map[string]int
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["migrations_cancelled"] != 2 {
		t.Errorf("migrations_cancelled = %d, want 2", resp["migrations_cancelled"])
	}
	if resp["batches_cancelled"] != 1 {
		t.Errorf("batches_cancelled = %d, want 1", resp["batches_cancelled"])
	}

	// All contexts should be cancelled.
	if ctx1.Err() == nil || ctx2.Err() == nil || batchCtx.Err() == nil {
		t.Error("expected all contexts cancelled")
	}

	server.liveMigrations.finish("all-box-1")
	server.liveMigrations.finish("all-box-2")
	server.liveMigrations.finishBatch("user-all-test")
}
