package compute

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	exeletclient "exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type receiveVMTarget interface {
	Init(ctx context.Context, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error)
	ResumeToken(ctx context.Context) (token, sidebandAddr string, err error)
	AdvancePhase(ctx context.Context, last bool) (sidebandAddr string, err error)
	UploadSnapshot(ctx context.Context, filename string, data []byte, compressed, isLastChunk bool) error
	Complete(ctx context.Context, checksum string) (*api.CompleteReceiveVMResponse, error)
	Abort(ctx context.Context, reason string) error
	RestartFresh(ctx context.Context) (*api.InitReceiveVMResponse, error)
	ConsumeUpdatedReady() *api.InitReceiveVMResponse
	Close()
	SidebandAddr() string
	SetSidebandAddr(addr string)
	Resumable() bool
	SetFaultInjection(afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool)
	FaultInjection() (afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool)
}

type unaryReceiveVMTarget struct {
	client *exeletclient.Client

	sessionID string
	startReq  *api.InitReceiveVMRequest
	ready     *api.InitReceiveVMResponse
	updated   *api.InitReceiveVMResponse

	sidebandAddr string
	resumable    bool

	sidebandFaultAfterBytes *atomic.Int64
	sidebandFaultSkipCount  *atomic.Int64
	sidebandFaultKillGRPC   *atomic.Bool
}

type streamReceiveVMTarget struct {
	client     *exeletclient.Client
	stream     api.ComputeService_ReceiveVMClient
	cancelFunc context.CancelFunc
	targetAddr string
	startReq   *api.InitReceiveVMRequest
	updated    *api.InitReceiveVMResponse

	sidebandAddr string
	resumable    bool

	sidebandFaultAfterBytes *atomic.Int64
	sidebandFaultSkipCount  *atomic.Int64
	sidebandFaultKillGRPC   *atomic.Bool
}

func newReceiveVMTarget(ctx context.Context, targetAddress string, req *api.InitReceiveVMRequest) (receiveVMTarget, *api.InitReceiveVMResponse, error) {
	client, err := exeletclient.NewClient(targetAddress, exeletclient.WithInsecure())
	if err != nil {
		return nil, nil, fmt.Errorf("dial target exelet %s: %w", targetAddress, err)
	}
	unary := &unaryReceiveVMTarget{client: client}
	ready, err := unary.Init(ctx, req)
	if err == nil {
		return unary, ready, nil
	}
	if status.Code(err) != codes.Unimplemented {
		client.Close()
		return nil, nil, err
	}
	streamTarget, ready, streamErr := newStreamReceiveVMTarget(ctx, client, targetAddress, req)
	if streamErr != nil {
		client.Close()
		return nil, nil, streamErr
	}
	return streamTarget, ready, nil
}

func newStreamReceiveVMTarget(ctx context.Context, client *exeletclient.Client, targetAddress string, req *api.InitReceiveVMRequest) (*streamReceiveVMTarget, *api.InitReceiveVMResponse, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := client.ReceiveVM(streamCtx)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("open ReceiveVM stream to %s: %w", targetAddress, err)
	}
	t := &streamReceiveVMTarget{client: client, stream: stream, cancelFunc: cancel, targetAddr: targetAddress}
	ready, err := t.Init(ctx, req)
	if err != nil {
		t.Close()
		return nil, nil, err
	}
	return t, ready, nil
}

func (t *unaryReceiveVMTarget) Init(ctx context.Context, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error) {
	resp, err := t.client.InitReceiveVM(ctx, req)
	if err != nil {
		return nil, err
	}
	t.sessionID = resp.SessionID
	t.startReq = proto.Clone(req).(*api.InitReceiveVMRequest)
	t.ready = resp
	t.sidebandAddr = resp.SidebandAddr
	t.resumable = resp.Resumable
	return resp, nil
}

func (t *unaryReceiveVMTarget) reinit(ctx context.Context, discardOrphan bool) (*api.InitReceiveVMResponse, error) {
	freshReq := proto.Clone(t.startReq).(*api.InitReceiveVMRequest)
	freshReq.DiscardOrphan = discardOrphan
	resp, err := t.client.InitReceiveVM(ctx, freshReq)
	if err != nil {
		return nil, err
	}
	t.sessionID = resp.SessionID
	t.ready = resp
	t.updated = resp
	t.sidebandAddr = resp.SidebandAddr
	t.resumable = resp.Resumable
	return resp, nil
}

func (t *unaryReceiveVMTarget) ResumeToken(ctx context.Context) (string, string, error) {
	resp, err := t.client.GetReceiveVMResumeToken(ctx, &api.GetReceiveVMResumeTokenRequest{SessionID: t.sessionID})
	if status.Code(err) == codes.NotFound {
		ready, err := t.reinit(ctx, false)
		if err != nil {
			return "", "", err
		}
		return ready.ResumeToken, ready.SidebandAddr, nil
	}
	if err != nil {
		return "", "", err
	}
	t.sidebandAddr = resp.SidebandAddr
	return resp.Token, resp.SidebandAddr, nil
}

func (t *unaryReceiveVMTarget) AdvancePhase(ctx context.Context, last bool) (string, error) {
	resp, err := t.client.AdvanceReceiveVMPhase(ctx, &api.AdvanceReceiveVMPhaseRequest{SessionID: t.sessionID, Last: last})
	if err != nil {
		return "", err
	}
	t.sidebandAddr = resp.SidebandAddr
	return resp.SidebandAddr, nil
}

func (t *unaryReceiveVMTarget) UploadSnapshot(ctx context.Context, filename string, data []byte, compressed, isLastChunk bool) error {
	_, err := t.client.UploadReceiveVMSnapshot(ctx, &api.UploadReceiveVMSnapshotRequest{
		SessionID:   t.sessionID,
		Filename:    filename,
		Data:        data,
		Compressed:  compressed,
		IsLastChunk: isLastChunk,
	})
	return err
}

func (t *unaryReceiveVMTarget) Complete(ctx context.Context, checksum string) (*api.CompleteReceiveVMResponse, error) {
	return t.client.CompleteReceiveVM(ctx, &api.CompleteReceiveVMRequest{SessionID: t.sessionID, Checksum: checksum})
}

func (t *unaryReceiveVMTarget) Abort(ctx context.Context, reason string) error {
	if t.sessionID == "" {
		return nil
	}
	_, err := t.client.AbortReceiveVM(ctx, &api.AbortReceiveVMRequest{SessionID: t.sessionID, Reason: reason})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

func (t *unaryReceiveVMTarget) RestartFresh(ctx context.Context) (*api.InitReceiveVMResponse, error) {
	_ = t.Abort(ctx, "restart fresh after stale resume token")
	return t.reinit(ctx, true)
}

func (t *unaryReceiveVMTarget) ConsumeUpdatedReady() *api.InitReceiveVMResponse {
	ready := t.updated
	t.updated = nil
	return ready
}

func (t *unaryReceiveVMTarget) Close()                      { t.client.Close() }
func (t *unaryReceiveVMTarget) SidebandAddr() string        { return t.sidebandAddr }
func (t *unaryReceiveVMTarget) SetSidebandAddr(addr string) { t.sidebandAddr = addr }
func (t *unaryReceiveVMTarget) Resumable() bool             { return t.resumable }
func (t *unaryReceiveVMTarget) SetFaultInjection(afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool) {
	t.sidebandFaultAfterBytes = afterBytes
	t.sidebandFaultSkipCount = skipCount
	t.sidebandFaultKillGRPC = killGRPC
}

func (t *unaryReceiveVMTarget) FaultInjection() (afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool) {
	return t.sidebandFaultAfterBytes, t.sidebandFaultSkipCount, t.sidebandFaultKillGRPC
}

func (t *streamReceiveVMTarget) reopen(ctx context.Context, discardOrphan bool) (*api.InitReceiveVMResponse, error) {
	t.cancelFunc()
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := t.client.ReceiveVM(streamCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ReceiveVM stream to %s: %w", t.targetAddr, err)
	}
	t.stream = stream
	t.cancelFunc = cancel

	freshReq := proto.Clone(t.startReq).(*api.InitReceiveVMRequest)
	freshReq.DiscardOrphan = discardOrphan
	resp, err := t.Init(ctx, freshReq)
	if err != nil {
		return nil, err
	}
	t.updated = resp
	return resp, nil
}

func (t *streamReceiveVMTarget) Init(ctx context.Context, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error) {
	t.startReq = proto.Clone(req).(*api.InitReceiveVMRequest)
	if err := t.stream.Send(&api.ReceiveVMRequest{Type: &api.ReceiveVMRequest_Start{Start: &api.ReceiveVMStartRequest{
		InstanceID:     req.InstanceID,
		SourceInstance: req.SourceInstance,
		BaseImageID:    req.BaseImageID,
		Encrypted:      req.Encrypted,
		EncryptionKey:  req.EncryptionKey,
		GroupID:        req.GroupID,
		Live:           req.Live,
		DiscardOrphan:  req.DiscardOrphan,
	}}}); err != nil {
		return nil, fmt.Errorf("send start to %s: %w", t.targetAddr, err)
	}
	resp, err := t.stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("receive ready from %s: %w", t.targetAddr, err)
	}
	ready := resp.GetReady()
	if ready == nil {
		return nil, fmt.Errorf("expected ready from target, got %T", resp.Type)
	}
	initResp := &api.InitReceiveVMResponse{
		HasBaseImage:   ready.HasBaseImage,
		TargetNetwork:  ready.TargetNetwork,
		SidebandAddr:   ready.SidebandAddr,
		Resumable:      ready.Resumable,
		ResumeToken:    ready.ResumeToken,
		SkipIpReconfig: ready.SkipIpReconfig,
	}
	t.sidebandAddr = ready.SidebandAddr
	t.resumable = ready.Resumable
	return initResp, nil
}

func (t *streamReceiveVMTarget) ResumeToken(ctx context.Context) (string, string, error) {
	if err := t.stream.Send(&api.ReceiveVMRequest{Type: &api.ReceiveVMRequest_ResumeTokenRequest{ResumeTokenRequest: &api.ReceiveVMResumeTokenRequest{}}}); err != nil {
		ready, reconnErr := t.reopen(ctx, false)
		if reconnErr != nil {
			return "", "", fmt.Errorf("reconnect failed: %w (original error: %v)", reconnErr, err)
		}
		return ready.ResumeToken, ready.SidebandAddr, nil
	}
	resp, err := t.stream.Recv()
	if err != nil {
		ready, reconnErr := t.reopen(ctx, false)
		if reconnErr != nil {
			return "", "", fmt.Errorf("reconnect failed: %w (original error: %v)", reconnErr, err)
		}
		return ready.ResumeToken, ready.SidebandAddr, nil
	}
	rt := resp.GetResumeToken()
	if rt == nil {
		return "", "", fmt.Errorf("expected resume token response, got %T", resp.Type)
	}
	t.sidebandAddr = rt.SidebandAddr
	return rt.Token, rt.SidebandAddr, nil
}

func (t *streamReceiveVMTarget) AdvancePhase(ctx context.Context, last bool) (string, error) {
	if err := t.stream.Send(&api.ReceiveVMRequest{Type: &api.ReceiveVMRequest_PhaseComplete{PhaseComplete: &api.ReceiveVMPhaseComplete{Last: last}}}); err != nil {
		return "", err
	}
	if last {
		t.sidebandAddr = ""
		return "", nil
	}
	resp, err := t.stream.Recv()
	if err != nil {
		return "", err
	}
	pr := resp.GetPhaseReady()
	if pr == nil {
		return "", fmt.Errorf("expected PhaseReady from target, got %T", resp.Type)
	}
	t.sidebandAddr = pr.SidebandAddr
	return pr.SidebandAddr, nil
}

func (t *streamReceiveVMTarget) UploadSnapshot(ctx context.Context, filename string, data []byte, compressed, isLastChunk bool) error {
	return t.stream.Send(&api.ReceiveVMRequest{Type: &api.ReceiveVMRequest_SnapshotData{SnapshotData: &api.ReceiveVMSnapshotChunk{
		Filename:    filename,
		Data:        data,
		Compressed:  compressed,
		IsLastChunk: isLastChunk,
	}}})
}

func (t *streamReceiveVMTarget) Complete(ctx context.Context, checksum string) (*api.CompleteReceiveVMResponse, error) {
	if err := t.stream.Send(&api.ReceiveVMRequest{Type: &api.ReceiveVMRequest_Complete{Complete: &api.ReceiveVMComplete{Checksum: checksum}}}); err != nil {
		return nil, err
	}
	if err := t.stream.CloseSend(); err != nil {
		return nil, err
	}
	var result *api.ReceiveVMResult
	for {
		resp, err := t.stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if r := resp.GetResult(); r != nil {
			result = r
			break
		}
	}
	if result == nil {
		return nil, fmt.Errorf("no result from target")
	}
	return &api.CompleteReceiveVMResponse{Instance: result.Instance, Error: result.Error, ColdBooted: result.ColdBooted}, nil
}

func (t *streamReceiveVMTarget) Abort(ctx context.Context, reason string) error {
	t.cancelFunc()
	return nil
}

func (t *streamReceiveVMTarget) RestartFresh(ctx context.Context) (*api.InitReceiveVMResponse, error) {
	return t.reopen(ctx, true)
}

func (t *streamReceiveVMTarget) ConsumeUpdatedReady() *api.InitReceiveVMResponse {
	ready := t.updated
	t.updated = nil
	return ready
}

func (t *streamReceiveVMTarget) Close() {
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
	t.client.Close()
}
func (t *streamReceiveVMTarget) SidebandAddr() string        { return t.sidebandAddr }
func (t *streamReceiveVMTarget) SetSidebandAddr(addr string) { t.sidebandAddr = addr }
func (t *streamReceiveVMTarget) Resumable() bool             { return t.resumable }
func (t *streamReceiveVMTarget) SetFaultInjection(afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool) {
	t.sidebandFaultAfterBytes = afterBytes
	t.sidebandFaultSkipCount = skipCount
	t.sidebandFaultKillGRPC = killGRPC
}

func (t *streamReceiveVMTarget) FaultInjection() (afterBytes, skipCount *atomic.Int64, killGRPC *atomic.Bool) {
	return t.sidebandFaultAfterBytes, t.sidebandFaultSkipCount, t.sidebandFaultKillGRPC
}
