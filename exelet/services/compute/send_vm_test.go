package compute

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// fakeReplicationSuspender implements services.ReplicationSuspender for testing.
type fakeReplicationSuspender struct {
	active bool
}

func (f *fakeReplicationSuspender) SuspendVolume(string)                   {}
func (f *fakeReplicationSuspender) ResumeVolume(string)                    {}
func (f *fakeReplicationSuspender) IsVolumeActive(string) bool             { return f.active }
func (f *fakeReplicationSuspender) WaitVolumeIdle(context.Context, string) {}

// startRealSendVMServer creates a real compute Service wired to a gRPC server
// and returns a client stream factory. The service has no instances on disk, so
// SendVM will fail after the replication-suspend phase with "instance not found".
// This is intentional: we only need to observe what the server sends on the
// stream before that error.
func startRealSendVMServer(t *testing.T, rs services.ReplicationSuspender) api.ComputeServiceClient {
	t.Helper()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.ExeletConfig{
		Name:    "test",
		DataDir: t.TempDir(),
	}

	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create compute service: %v", err)
	}
	computeSvc := svc.(*Service)
	computeSvc.context = &services.ServiceContext{
		ReplicationSuspender: rs,
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	gs := grpc.NewServer()
	api.RegisterComputeServiceServer(gs, computeSvc)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return api.NewComputeServiceClient(conn)
}

// TestSendVMStatusSuppressedWithoutAcceptStatus verifies that the real SendVM
// implementation does NOT send a SendVMStatus frame when the client omits
// AcceptStatus, even when replication is active. This is the backward-
// compatibility guarantee that prevents old clients from breaking during
// rolling upgrades.
func TestSendVMStatusSuppressedWithoutAcceptStatus(t *testing.T) {
	client := startRealSendVMServer(t, &fakeReplicationSuspender{active: true})

	ctx := context.Background()
	stream, err := client.SendVM(ctx)
	if err != nil {
		t.Fatalf("failed to start SendVM stream: %v", err)
	}

	// Send start request WITHOUT AcceptStatus (old client).
	if err := stream.Send(&api.SendVMRequest{
		Type: &api.SendVMRequest_Start{
			Start: &api.SendVMStartRequest{
				InstanceID:         "nonexistent-instance",
				TargetHasBaseImage: true,
				// AcceptStatus not set — old client behavior
			},
		},
	}); err != nil {
		t.Fatalf("failed to send start request: %v", err)
	}

	// The server should NOT send any status frame. The instance doesn't
	// exist, so the next thing on the stream is a gRPC error (NotFound).
	// If the server incorrectly sent a status frame, we'd see that instead.
	resp, err := stream.Recv()
	if err == nil {
		// We got a message instead of an error — this means a status frame leaked.
		t.Fatalf("old client received unexpected message before error: %T (%v)", resp.Type, resp)
	}
	// The expected error is "instance not found" — any error here is fine,
	// the important thing is we didn't get a status frame first.
}

// TestSendVMStatusSentWithAcceptStatus verifies that the real SendVM
// implementation sends a SendVMStatus frame when the client sets AcceptStatus
// and replication is active.
func TestSendVMStatusSentWithAcceptStatus(t *testing.T) {
	client := startRealSendVMServer(t, &fakeReplicationSuspender{active: true})

	ctx := context.Background()
	stream, err := client.SendVM(ctx)
	if err != nil {
		t.Fatalf("failed to start SendVM stream: %v", err)
	}

	// Send start request WITH AcceptStatus (new client).
	if err := stream.Send(&api.SendVMRequest{
		Type: &api.SendVMRequest_Start{
			Start: &api.SendVMStartRequest{
				InstanceID:         "nonexistent-instance",
				TargetHasBaseImage: true,
				AcceptStatus:       true,
			},
		},
	}); err != nil {
		t.Fatalf("failed to send start request: %v", err)
	}

	// The server should send a status frame before the error.
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected status frame, got error: %v", err)
	}
	st := resp.GetStatus()
	if st == nil {
		t.Fatalf("expected SendVMStatus, got %T", resp.Type)
	}
	if st.Message != "waiting for storage replication to complete" {
		t.Errorf("unexpected status message: %q", st.Message)
	}
}
