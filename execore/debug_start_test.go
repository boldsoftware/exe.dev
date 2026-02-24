package execore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	exeletclient "exe.dev/exelet/client"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"google.golang.org/grpc"
)

// fakeComputeServer implements a minimal ComputeServiceServer for testing.
// It tracks whether StartInstance has been called so GetInstance can return
// the appropriate state (STOPPED before start, RUNNING after).
type fakeComputeServer struct {
	computeapi.UnimplementedComputeServiceServer
	sshPort int32
	started atomic.Bool
}

func (f *fakeComputeServer) StartInstance(_ context.Context, _ *computeapi.StartInstanceRequest) (*computeapi.StartInstanceResponse, error) {
	f.started.Store(true)
	return &computeapi.StartInstanceResponse{}, nil
}

func (f *fakeComputeServer) StopInstance(_ context.Context, _ *computeapi.StopInstanceRequest) (*computeapi.StopInstanceResponse, error) {
	f.started.Store(false)
	return &computeapi.StopInstanceResponse{}, nil
}

func (f *fakeComputeServer) GetInstance(_ context.Context, _ *computeapi.GetInstanceRequest) (*computeapi.GetInstanceResponse, error) {
	state := computeapi.VMState_STOPPED
	if f.started.Load() {
		state = computeapi.VMState_RUNNING
	}
	return &computeapi.GetInstanceResponse{
		Instance: &computeapi.Instance{
			State:   state,
			SSHPort: f.sshPort,
		},
	}, nil
}

// startFakeExelet starts a gRPC server with the fakeComputeServer and returns
// the exeletclient.Client address (tcp://host:port) and a cleanup function.
func startFakeExelet(t *testing.T, sshPort int32) (string, *exeletclient.Client) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	gs := grpc.NewServer()
	computeapi.RegisterComputeServiceServer(gs, &fakeComputeServer{sshPort: sshPort})

	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	addr := fmt.Sprintf("tcp://%s", lis.Addr().String())
	client, err := exeletclient.NewClient(addr, exeletclient.WithInsecure())
	if err != nil {
		t.Fatalf("failed to create exelet client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return addr, client
}

// setupMigratedBox creates a test server with a fake exelet and a box that
// simulates a post-migration stopped VM (has container_id, SSHPort is NULL).
// Returns the server, exelet address, box name, and user ID.
func setupMigratedBox(t *testing.T, boxName string, sshPort int32) (*Server, string, string) {
	t.Helper()
	server := newTestServer(t)
	ctx := context.Background()

	addr, client := startFakeExelet(t, sshPort)

	server.exeletClients[addr] = &exeletClient{
		addr:   addr,
		client: client,
	}
	server.exeletClients[addr].up.Store(true)

	userID := createTestUser(t, server, boxName+"@example.com")

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
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
			Status:      "stopped",
			SSHPort:     nil,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	return server, addr, userID
}

// TestDebugStartSyncsSSHPort verifies that starting a stopped box with no SSH
// port (as happens after migrating a stopped VM) fetches the port from the
// exelet and writes it back to the database.
func TestDebugStartSyncsSSHPort(t *testing.T) {
	const wantSSHPort int32 = 12345
	server, _, _ := setupMigratedBox(t, "migrated-vm", wantSSHPort)
	ctx := context.Background()

	// Verify SSHPort is nil before start.
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "migrated-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.SSHPort != nil {
		t.Fatalf("expected SSHPort to be nil before start, got %d", *box.SSHPort)
	}

	// POST to the debug start handler.
	form := url.Values{"box_name": {"migrated-vm"}}
	req := httptest.NewRequest("POST", "/debug/boxes/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	server.handleDebugBoxStart(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the SSH port was synced to the database.
	box, err = withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "migrated-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.SSHPort == nil {
		t.Fatal("expected SSHPort to be set after start, got nil")
	}
	if *box.SSHPort != int64(wantSSHPort) {
		t.Fatalf("expected SSHPort=%d, got %d", wantSSHPort, *box.SSHPort)
	}
}

// TestDebugStartPreservesExistingSSHPort verifies that starting a box that
// already has an SSH port does NOT overwrite it.
func TestDebugStartPreservesExistingSSHPort(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	const existingPort int64 = 54321
	const exeletPort int32 = 99999 // different port to prove we don't overwrite
	addr, client := startFakeExelet(t, exeletPort)

	server.exeletClients[addr] = &exeletClient{
		addr:   addr,
		client: client,
	}
	server.exeletClients[addr].up.Store(true)

	userID := createTestUser(t, server, "normal-start@example.com")

	boxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        userID,
		ctrhost:       addr,
		name:          "normal-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	containerID := "container-normal-456"
	existingPortVal := existingPort
	err = server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ID:          boxID,
			ContainerID: &containerID,
			Status:      "stopped",
			SSHPort:     &existingPortVal,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"box_name": {"normal-vm"}}
	req := httptest.NewRequest("POST", "/debug/boxes/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	server.handleDebugBoxStart(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}

	// SSH port should remain the original value, not the exelet's port.
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "normal-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.SSHPort == nil {
		t.Fatal("expected SSHPort to remain set")
	}
	if *box.SSHPort != existingPort {
		t.Fatalf("expected SSHPort=%d (unchanged), got %d", existingPort, *box.SSHPort)
	}
}

// TestRestartCommandSyncsSSHPort verifies that the SSH "restart" command syncs
// the SSH port from the exelet when the database has SSHPort=NULL (post-migration).
func TestRestartCommandSyncsSSHPort(t *testing.T) {
	const wantSSHPort int32 = 22345
	server, _, userID := setupMigratedBox(t, "restart-vm", wantSSHPort)
	ctx := context.Background()

	// Verify SSHPort is nil before restart.
	box, err := withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "restart-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.SSHPort != nil {
		t.Fatalf("expected SSHPort to be nil before restart, got %d", *box.SSHPort)
	}

	// Invoke handleRestartCommand via the SSHServer.
	ss := &SSHServer{server: server}
	output := &MockOutput{}
	cc := &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: userID, Email: "restart-vm@example.com"},
		Args:   []string{"restart-vm"},
		Output: output,
	}

	if err := ss.handleRestartCommand(ctx, cc); err != nil {
		t.Fatalf("handleRestartCommand failed: %v", err)
	}

	// Verify the SSH port was synced to the database.
	box, err = withRxRes1(server, ctx, (*exedb.Queries).BoxNamed, "restart-vm")
	if err != nil {
		t.Fatal(err)
	}
	if box.SSHPort == nil {
		t.Fatal("expected SSHPort to be set after restart, got nil")
	}
	if *box.SSHPort != int64(wantSSHPort) {
		t.Fatalf("expected SSHPort=%d, got %d", wantSSHPort, *box.SSHPort)
	}
}
