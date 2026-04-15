package compute

import (
	"context"
	"fmt"
	"sync/atomic"

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

func newReceiveVMTarget(ctx context.Context, targetAddress string, req *api.InitReceiveVMRequest) (receiveVMTarget, *api.InitReceiveVMResponse, error) {
	client, err := exeletclient.NewClient(targetAddress, exeletclient.WithInsecure())
	if err != nil {
		return nil, nil, fmt.Errorf("dial target exelet %s: %w", targetAddress, err)
	}
	unary := &unaryReceiveVMTarget{client: client}
	ready, err := unary.Init(ctx, req)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return unary, ready, nil
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
