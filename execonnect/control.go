package execonnect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const maxControlStreamRetryDelay = 30 * time.Second

var errEndpointUpdatesClosed = errors.New("endpoint updates closed")

// EndpointUpdate is one complete local registry snapshot for publication.
type EndpointUpdate struct {
	Endpoints []Endpoint
}

// ControlEventKind identifies a control-stream lifecycle transition.
type ControlEventKind string

const (
	ControlConnected    ControlEventKind = "connected"
	ControlDisconnected ControlEventKind = "disconnected"
	ControlRetrying     ControlEventKind = "retrying"
	ControlSnapshotSent ControlEventKind = "snapshot_sent"
)

// ControlEvent reports one lifecycle transition to serve.
type ControlEvent struct {
	Kind     ControlEventKind
	Attempt  int
	Delay    time.Duration
	Terminal bool
	Err      error
}

// ControlEventHandler observes a control transition. Handler errors stop the connector session.
type ControlEventHandler func(ControlEvent) error

// ControlMessageHandler applies one control message received from exed.
type ControlMessageHandler func(context.Context, *connectapi.ControlMessage) error

type publishedEndpointSnapshot struct {
	snapshot *connectapi.EndpointSnapshot
}

// RunControlStream maintains the authenticated connector control stream,
// publishes complete endpoint snapshots, and replays the latest snapshot
// after a reconnect. Only transient transport failures are retried.
func RunControlStream(
	ctx context.Context,
	client connectapi.ExternalConnectionServiceClient,
	credentials Credentials,
	wireGuardPublicKey string,
	endpointUpdates <-chan EndpointUpdate,
	handleControl ControlMessageHandler,
	onEvent ControlEventHandler,
) error {
	if client == nil {
		return fmt.Errorf("nil External Connection client")
	}
	if err := credentials.validate(); err != nil {
		return fmt.Errorf("invalid credentials: %w", err)
	}
	if wireGuardPublicKey == "" || wireGuardPublicKey != strings.TrimSpace(wireGuardPublicKey) {
		return fmt.Errorf("WireGuard public key is required")
	}
	if endpointUpdates == nil {
		return fmt.Errorf("nil endpoint updates")
	}

	var latest *publishedEndpointSnapshot
	for attempt := 0; ; attempt++ {
		var err error
		latest, err = runControlStreamOnce(ctx, client, credentials, wireGuardPublicKey, endpointUpdates, latest, attempt, handleControl, onEvent)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errEndpointUpdatesClosed) {
			return err
		}
		transient := isTransientControlError(err)
		if eventErr := reportControlEvent(onEvent, ControlEvent{
			Kind:     ControlDisconnected,
			Attempt:  attempt,
			Terminal: !transient,
			Err:      err,
		}); eventErr != nil {
			return eventErr
		}
		if !transient {
			return err
		}

		delay := controlStreamRetryDelay(attempt)
		if eventErr := reportControlEvent(onEvent, ControlEvent{
			Kind:    ControlRetrying,
			Attempt: attempt + 1,
			Delay:   delay,
			Err:     err,
		}); eventErr != nil {
			return eventErr
		}
		latest, err = waitControlStreamRetry(ctx, delay, endpointUpdates, latest)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func runControlStreamOnce(
	ctx context.Context,
	client connectapi.ExternalConnectionServiceClient,
	credentials Credentials,
	wireGuardPublicKey string,
	endpointUpdates <-chan EndpointUpdate,
	latest *publishedEndpointSnapshot,
	attempt int,
	handleControl ControlMessageHandler,
	onEvent ControlEventHandler,
) (*publishedEndpointSnapshot, error) {
	streamContext, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+credentials.ConnectorSecret))
	defer cancel()

	stream, err := client.Connect(streamContext)
	if err != nil {
		return latest, fmt.Errorf("open control stream: %w", err)
	}
	defer stream.CloseSend()
	if err := stream.Send(&connectapi.ConnectorMessage{Payload: &connectapi.ConnectorMessage_Hello{Hello: &connectapi.ConnectorHello{
		ExternalConnectionID: credentials.ExternalConnectionID,
		WireguardPublicKey:   wireGuardPublicKey,
		CapabilityVersion:    connectapi.CurrentCapabilityVersion,
		BuildVersion:         BuildRevision(),
	}}}); err != nil {
		return latest, resolveInitialControlSendError(streamContext, stream, handleControl, fmt.Errorf("send connector hello: %w", err))
	}
	if err := reportControlEvent(onEvent, ControlEvent{Kind: ControlConnected, Attempt: attempt}); err != nil {
		return latest, err
	}
	if latest != nil {
		if err := sendEndpointSnapshot(stream.Send, latest.snapshot); err != nil {
			return latest, resolveInitialControlSendError(streamContext, stream, handleControl, err)
		}
		if err := reportControlEvent(onEvent, ControlEvent{Kind: ControlSnapshotSent}); err != nil {
			return latest, err
		}
	}

	receiveResult := make(chan error, 1)
	go func() {
		receiveResult <- receiveControlMessages(streamContext, stream.Recv, handleControl)
	}()

	for {
		select {
		case <-ctx.Done():
			return latest, nil
		case err := <-receiveResult:
			return latest, fmt.Errorf("receive control stream: %w", err)
		case update, ok := <-endpointUpdates:
			if !ok {
				return latest, errEndpointUpdatesClosed
			}
			snapshot, err := BuildEndpointSnapshot(update.Endpoints)
			if err != nil {
				return latest, fmt.Errorf("build endpoint snapshot: %w", err)
			}
			latest = &publishedEndpointSnapshot{snapshot: snapshot}
			if err := sendEndpointSnapshot(stream.Send, latest.snapshot); err != nil {
				if errors.Is(err, io.EOF) {
					select {
					case receiveErr := <-receiveResult:
						return latest, fmt.Errorf("receive control stream: %w", receiveErr)
					case <-ctx.Done():
						return latest, nil
					}
				}
				return latest, err
			}
			if err := reportControlEvent(onEvent, ControlEvent{Kind: ControlSnapshotSent}); err != nil {
				return latest, err
			}
		}
	}
}

func resolveInitialControlSendError(
	ctx context.Context,
	stream grpc.BidiStreamingClient[connectapi.ConnectorMessage, connectapi.ControlMessage],
	handle ControlMessageHandler,
	sendErr error,
) error {
	if !errors.Is(sendErr, io.EOF) {
		return sendErr
	}
	if err := receiveControlMessages(ctx, stream.Recv, handle); err != nil {
		return fmt.Errorf("receive control stream after send closed: %w", err)
	}
	return sendErr
}

func receiveControlMessages(
	ctx context.Context,
	receive func() (*connectapi.ControlMessage, error),
	handle ControlMessageHandler,
) error {
	for {
		message, err := receive()
		if err != nil {
			return err
		}
		if message == nil {
			return io.ErrUnexpectedEOF
		}
		if handle != nil {
			if err := handle(ctx, message); err != nil {
				return fmt.Errorf("handle control message: %w", err)
			}
		}
	}
}

func sendEndpointSnapshot(send func(*connectapi.ConnectorMessage) error, snapshot *connectapi.EndpointSnapshot) error {
	if err := send(&connectapi.ConnectorMessage{Payload: &connectapi.ConnectorMessage_EndpointSnapshot{EndpointSnapshot: snapshot}}); err != nil {
		return fmt.Errorf("send endpoint snapshot: %w", err)
	}
	return nil
}

func waitControlStreamRetry(
	ctx context.Context,
	delay time.Duration,
	endpointUpdates <-chan EndpointUpdate,
	latest *publishedEndpointSnapshot,
) (*publishedEndpointSnapshot, error) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return latest, nil
		case <-timer.C:
			return latest, nil
		case update, ok := <-endpointUpdates:
			if !ok {
				return latest, errEndpointUpdatesClosed
			}
			snapshot, err := BuildEndpointSnapshot(update.Endpoints)
			if err != nil {
				return latest, fmt.Errorf("build endpoint snapshot: %w", err)
			}
			latest = &publishedEndpointSnapshot{snapshot: snapshot}
		}
	}
}

func controlStreamRetryDelay(retry int) time.Duration {
	delay := time.Second
	for range min(retry, 5) {
		delay *= 2
	}
	return min(delay, maxControlStreamRetryDelay)
}

func isTransientControlError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return true
		}
		type temporaryError interface {
			Temporary() bool
		}
		if temporary, ok := any(networkError).(temporaryError); ok && temporary.Temporary() {
			return true
		}
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted, codes.Internal:
		return true
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.FailedPrecondition:
		return false
	default:
		return false
	}
}

func reportControlEvent(handler ControlEventHandler, event ControlEvent) error {
	if handler == nil {
		return nil
	}
	if err := handler(event); err != nil {
		return fmt.Errorf("record control event %q: %w", event.Kind, err)
	}
	return nil
}
