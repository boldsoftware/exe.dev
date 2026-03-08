package execore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/region"
	"google.golang.org/grpc"
)

func TestHandleDebugUserMigrateRegion(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()
	userID := createTestUser(t, server, "migrate-region@example.com")

	// Verify user starts with default region.
	user, err := withRxRes1(server, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	defaultRegion := region.Default().Code
	if user.Region != defaultRegion {
		t.Fatalf("user region = %q, want %q", user.Region, defaultRegion)
	}

	// Migrate to lon.
	form := url.Values{
		"user_id": {userID},
		"region":  {"lon"},
	}
	req := httptest.NewRequest("POST", "/debug/user/migrate-region", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugUserMigrateRegion(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	// Verify region was updated.
	user, err = withRxRes1(server, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Region != "lon" {
		t.Errorf("user region = %q, want %q", user.Region, "lon")
	}

	// Verify GLB default was enabled.
	defaults, err := withRxRes1(server, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.GlobalLoadBalancer == nil || *defaults.GlobalLoadBalancer != 1 {
		t.Errorf("GlobalLoadBalancer = %v, want 1", defaults.GlobalLoadBalancer)
	}
}

func TestHandleDebugUserMigrateRegionCanonicalizesCode(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()
	userID := createTestUser(t, server, "migrate-region-canon@example.com")

	// Send uppercase region code.
	form := url.Values{
		"user_id": {userID},
		"region":  {"LON"},
	}
	req := httptest.NewRequest("POST", "/debug/user/migrate-region", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugUserMigrateRegion(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}

	// Verify region was stored as lowercase.
	user, err := withRxRes1(server, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Region != "lon" {
		t.Errorf("user region = %q, want %q (should be canonicalized to lowercase)", user.Region, "lon")
	}
}

func TestHandleDebugUserMigrateRegionInvalidRegion(t *testing.T) {
	server := newTestServer(t)
	userID := createTestUser(t, server, "migrate-region-invalid@example.com")

	form := url.Values{
		"user_id": {userID},
		"region":  {"mars"},
	}
	req := httptest.NewRequest("POST", "/debug/user/migrate-region", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugUserMigrateRegion(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleDebugUserMigrateVMs(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 44444
	const boxName = "region-migrate-vm"

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

	// Create user and set region to lon.
	userID := createTestUser(t, server, "vm-migrate@example.com")
	err := withTx1(server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		Region: "lon",
		UserID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a box on the source (pdx) exelet.
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

	// POST to migrate-vms with correct confirmation.
	form := url.Values{
		"user_id": {userID},
		"confirm": {"1"},
	}
	req := httptest.NewRequest("POST", "/debug/user/migrate-vms", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugUserMigrateVMs(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "MIGRATION_SUCCESS") {
		t.Errorf("expected MIGRATION_SUCCESS in response, got:\n%s", body)
	}
	if strings.Contains(body, "MIGRATION_ERROR") {
		t.Errorf("unexpected MIGRATION_ERROR in response:\n%s", body)
	}
	if !strings.Contains(body, "Succeeded: 1, Failed: 0") {
		t.Errorf("expected 1 succeeded, got:\n%s", body)
	}

	// Verify box was moved to the target.
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		t.Fatal(err)
	}
	if box.Ctrhost != targetAddr {
		t.Errorf("box ctrhost = %q, want %q", box.Ctrhost, targetAddr)
	}
	if box.Region != "lon" {
		t.Errorf("box region = %q, want %q", box.Region, "lon")
	}
}

func TestHandleDebugUserMigrateVMsRequiresConfirmation(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 44445

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

	// Create user and set region to lon.
	userID := createTestUser(t, server, "vm-migrate-confirm@example.com")
	err := withTx1(server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		Region: "lon",
		UserID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a box on the source (pdx) exelet.
	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       sourceAddr,
		name:          "confirm-test-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	containerID := "container-confirm-test"
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

	// POST without confirm — should fail.
	form := url.Values{
		"user_id": {userID},
	}
	req := httptest.NewRequest("POST", "/debug/user/migrate-vms", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugUserMigrateVMs(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "MIGRATION_ERROR") {
		t.Errorf("expected MIGRATION_ERROR when confirm is missing, got:\n%s", body)
	}

	// POST with wrong confirm — should fail.
	form.Set("confirm", "99")
	req = httptest.NewRequest("POST", "/debug/user/migrate-vms", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleDebugUserMigrateVMs(w, req)

	body = w.Body.String()
	if !strings.Contains(body, "MIGRATION_ERROR") {
		t.Errorf("expected MIGRATION_ERROR when confirm is wrong, got:\n%s", body)
	}

	// Verify box was NOT moved.
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "confirm-test-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.Ctrhost != sourceAddr {
		t.Errorf("box should still be on source after failed confirm, ctrhost = %q, want %q", box.Ctrhost, sourceAddr)
	}
}

func TestRestartSourceVM(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	const sshPort int32 = 55555

	t.Run("noop_when_not_running", func(t *testing.T) {
		srv := &fakeLiveMigrationServer{sshPort: sshPort}
		_, client := startFakeMigrationExelet(t, srv)
		ec := &exeletClient{client: client}

		var messages []string
		progress := func(format string, args ...any) {
			messages = append(messages, fmt.Sprintf(format, args...))
		}

		server.restartSourceVM(ctx, ec, "ctr-1", "box1", "source1", "test", false, false, progress)

		joined := strings.Join(messages, "\n")
		if !strings.Contains(joined, "already stopped") {
			t.Errorf("expected 'already stopped' message, got:\n%s", joined)
		}
	})

	t.Run("noop_when_still_running", func(t *testing.T) {
		// fakeLiveMigrationServer.GetInstance always returns RUNNING.
		srv := &fakeLiveMigrationServer{sshPort: sshPort}
		_, client := startFakeMigrationExelet(t, srv)
		ec := &exeletClient{client: client}

		var messages []string
		progress := func(format string, args ...any) {
			messages = append(messages, fmt.Sprintf(format, args...))
		}

		server.restartSourceVM(ctx, ec, "ctr-1", "box1", "source1", "test", true, false, progress)

		joined := strings.Join(messages, "\n")
		if !strings.Contains(joined, "still running") {
			t.Errorf("expected 'still running' message, got:\n%s", joined)
		}
	})

	t.Run("restarts_stopped_vm", func(t *testing.T) {
		// Use a server that returns STOPPED for GetInstance.
		srv := &fakeStoppedServer{sshPort: sshPort}
		_, client := startFakeComputeServer(t, srv)
		ec := &exeletClient{client: client}

		var messages []string
		progress := func(format string, args ...any) {
			messages = append(messages, fmt.Sprintf(format, args...))
		}

		server.restartSourceVM(ctx, ec, "ctr-1", "box1", "source1", "migration failed", true, false, progress)

		joined := strings.Join(messages, "\n")
		if !strings.Contains(joined, "VM restarted on source") {
			t.Errorf("expected restart message, got:\n%s", joined)
		}
	})
}

// startFakeComputeServer starts a gRPC server with the given ComputeServiceServer and
// returns the address and client. This is a more generic version of
// startFakeMigrationExelet that accepts any ComputeServiceServer implementation.
func startFakeComputeServer(t *testing.T, srv computeapi.ComputeServiceServer) (string, *exeletclient.Client) {
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

// fakeStoppedServer returns STOPPED for GetInstance and succeeds for Start/Stop.
type fakeStoppedServer struct {
	computeapi.UnimplementedComputeServiceServer
	sshPort int32
}

func (f *fakeStoppedServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   computeapi.VMState_STOPPED,
			SSHPort: f.sshPort,
		},
	}, nil
}

func (f *fakeStoppedServer) StartInstance(_ context.Context, _ *computeapi.StartInstanceRequest) (*computeapi.StartInstanceResponse, error) {
	return &computeapi.StartInstanceResponse{}, nil
}

func (f *fakeStoppedServer) StopInstance(_ context.Context, _ *computeapi.StopInstanceRequest) (*computeapi.StopInstanceResponse, error) {
	return &computeapi.StopInstanceResponse{}, nil
}

func (f *fakeStoppedServer) DeleteInstance(_ context.Context, _ *computeapi.DeleteInstanceRequest) (*computeapi.DeleteInstanceResponse, error) {
	return &computeapi.DeleteInstanceResponse{}, nil
}
