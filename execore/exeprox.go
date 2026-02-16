package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"exe.dev/exedb"
	"exe.dev/llmgateway"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"
	"exe.dev/tracing"
	"exe.dev/wildcardcert"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	grpclogging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// registerExeproxMetrics arranges to record grpc metrics for the
// exeprox grpc service.
func registerExeproxMetrics(metricsRegistry *prometheus.Registry) *grpcprom.ServerMetrics {
	grpcMetrics := grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.01, 0.1, 0.3, 0.6, 1, 1.4, 2, 3, 6, 9, 20, 30, 60, 90}),
		),
	)
	metricsRegistry.MustRegister(grpcMetrics)
	return grpcMetrics
}

// setupExeproxServer sets up the exeprox grpc service.
func (s *Server) setupExeproxServer() {
	// Adapter to convert slog.Logger to logging.Logger.
	loggerFunc := func(ctx context.Context, lvl grpclogging.Level, msg string, fields ...any) {
		s.slog().Log(ctx, slog.Level(lvl), msg, fields...)
	}

	unaryServerInterceptors := []grpc.UnaryServerInterceptor{
		tracing.UnaryServerInterceptor(),
		s.exeproxServiceMetrics.UnaryServerInterceptor(),
		grpclogging.UnaryServerInterceptor(grpclogging.LoggerFunc(loggerFunc)),
	}
	streamServerInterceptors := []grpc.StreamServerInterceptor{
		tracing.StreamServerInterceptor(),
		s.exeproxServiceMetrics.StreamServerInterceptor(),
		grpclogging.StreamServerInterceptor(grpclogging.LoggerFunc(loggerFunc)),
	}

	grpcOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryServerInterceptors...),
		grpc.ChainStreamInterceptor(streamServerInterceptors...),
	}

	grpcServer := grpc.NewServer(grpcOpts...)

	es := &exeproxServer{
		s: s,
	}

	proxyapi.RegisterProxyInfoServiceServer(grpcServer, es)

	s.exeproxServiceServer = grpcServer
}

// exeproxServer implements the exeprox.proto service.
type exeproxServer struct {
	proxyapi.UnimplementedProxyInfoServiceServer
	s *Server
}

// GetPublicIPS returns the current public IPs mapping.
func (es *exeproxServer) GetPublicIPs(req *proxyapi.GetPublicIPsRequest, stream proxyapi.ProxyInfoService_GetPublicIPsServer) error {
	for addr, publicIP := range es.s.PublicIPs {
		val := &proxyapi.GetPublicIPsResponse{
			Addr: addr.String(),
			PublicIP: &proxyapi.PublicIP{
				IP:     publicIP.IP.String(),
				Domain: publicIP.Domain,
				Shard:  int32(publicIP.Shard),
			},
		}
		if err := stream.Send(val); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
	return nil
}

// GetLobbyIP returns the lobby IP.
func (es *exeproxServer) GetLobbyIP(ctx context.Context, req *proxyapi.GetLobbyIPRequest) (*proxyapi.GetLobbyIPResponse, error) {
	ret := &proxyapi.GetLobbyIPResponse{
		IP: es.s.LobbyIP.String(),
	}
	return ret, nil
}

// BoxInfo takes a box name and returns information about it.
func (es *exeproxServer) BoxInfo(ctx context.Context, req *proxyapi.BoxInfoRequest) (*proxyapi.BoxInfoResponse, error) {
	box, err := exedb.WithRxRes1(es.s.db, ctx, (*exedb.Queries).BoxNamed, req.BoxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ret := &proxyapi.BoxInfoResponse{
				BoxExists: false,
			}
			return ret, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.BoxInfoResponse{
		BoxExists:       true,
		BoxID:           int64(box.ID),
		CreatedByUserID: box.CreatedByUserID,
		Image:           box.Image,
	}
	route := box.GetRoute()
	ret.Route = &proxyapi.BoxRoute{
		Port:  int32(route.Port),
		Share: route.Share,
	}
	return ret, nil
}

// CookieInfo takes a cookie value and returns information about it.
func (es *exeproxServer) CookieInfo(ctx context.Context, req *proxyapi.CookieInfoRequest) (*proxyapi.CookieInfoResponse, error) {
	cookie, err := withRxRes1(es.s, ctx, (*exedb.Queries).GetAuthCookieInfo, exedb.GetAuthCookieInfoParams{
		CookieValue: req.CookieValue,
		Domain:      req.Domain,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ret := &proxyapi.CookieInfoResponse{
				CookieExists: false,
			}
			return ret, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.CookieInfoResponse{
		CookieExists: true,
		CookieValue:  req.CookieValue,
		UserID:       cookie.UserID,
		Domain:       req.Domain,
		ExpiresAt:    timestamppb.New(cookie.ExpiresAt),
	}
	return ret, nil
}

// CertForDomain returns a certificate for a wildcard domain.
func (es *exeproxServer) CertForDomain(ctx context.Context, req *proxyapi.CertForDomainRequest) (*proxyapi.CertForDomainResponse, error) {
	if es.s.wildcardCertManager == nil {
		return nil, status.Error(codes.InvalidArgument, "no wildcard certificate manager")
	}
	cert, err := es.s.wildcardCertManager.GetCertificate(req.ServerName)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	bytes, err := wildcardcert.EncodeCertificate(cert)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.CertForDomainResponse{
		Cert: string(bytes),
	}
	return ret, nil
}

// CheckAndRefreshLLMCredit takes a user ID and checks if the user
// has any LLM credit available (after refresh).
// See [llmgateway.CreditManager.CheckAndRefreshCredit].
func (es *exeproxServer) CheckAndRefreshLLMCredit(ctx context.Context, req *proxyapi.CheckAndRefreshLLMCreditRequest) (*proxyapi.CheckAndRefreshLLMCreditResponse, error) {
	ci, err := llmgateway.CheckAndRefreshCreditDB(ctx, es.s.db, req.UserID, time.Now())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.CheckAndRefreshLLMCreditResponse{
		CreditInfo: gatewayCreditInfoToProto(ci),
	}
	return ret, nil
}

// TopUpOnLLMBillingUpgrade tops up a user's LLM credit to their
// new plan maximum. See [llmgateway.CreditManager.TopUpOnBillingUpgrade].
func (es *exeproxServer) TopUpOnLLMBillingUpgrade(ctx context.Context, req *proxyapi.TopUpOnLLMBillingUpgradeRequest) (*proxyapi.TopUpOnLLMBillingUpgradeResponse, error) {
	err := llmgateway.TopUpOnBillingUpgradeDB(ctx, es.s.db, req.UserID, time.Now())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &proxyapi.TopUpOnLLMBillingUpgradeResponse{}, nil
}

// LLMDebitCredit subtracts the given cost (in USD)
// from the user's LLM credit.
// See [llmgateway.CreditManager.LLMDebitCredit].
func (es *exeproxServer) LLMDebitCredit(ctx context.Context, req *proxyapi.LLMDebitCreditRequest) (*proxyapi.LLMDebitCreditResponse, error) {
	ci, err := llmgateway.DebitCreditDB(ctx, es.s.db, req.UserID, req.CostUsd, time.Now())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.LLMDebitCreditResponse{
		CreditInfo: gatewayCreditInfoToProto(ci),
	}
	return ret, nil
}

// gatewayCreditInfoToProto converts  an [llmgateway.CreditInfo]
// to a [proxyapi.Creditinfo].
func gatewayCreditInfoToProto(ciIn *llmgateway.CreditInfo) *proxyapi.CreditInfo {
	var ciOut proxyapi.CreditInfo
	ciOut.Available = ciIn.Available
	ciOut.Max = ciIn.Max
	ciOut.RefreshPerHour = ciIn.RefreshPerHour
	ciOut.LastRefresh = timestamppb.New(ciIn.LastRefresh)
	ciOut.Plan.Name = ciIn.Plan.Name
	ciOut.Plan.MaxCredit = ciIn.Plan.MaxCredit
	ciOut.Plan.RefreshPerHour = ciIn.Plan.RefreshPerHour
	ciOut.Plan.CreditExhaustedError = ciIn.Plan.CreditExhaustedError
	return &ciOut
}

// UsedCookie is used to report that an authentication cookie was used.
func (es *exeproxServer) UsedCookie(ctx context.Context, req *proxyapi.UsedCookieRequest) (*proxyapi.UsedCookieResponse, error) {
	err := withTx1(es.s, ctx, (*exedb.Queries).UpdateAuthCookieLastUsed, req.CookieValue)
	if err != nil {
		es.s.slog().ErrorContext(ctx, "UpdateAuthCookieLastUsed failed in exeproxServer.UsedCookie", "error", err)
		// Don't bother to return the error,
		// there is nothing the caller can do.
	}
	return &proxyapi.UsedCookieResponse{}, nil
}

// UsedBoxShareLink is used to report that a box share link was used.
func (es *exeproxServer) UsedBoxShareLink(ctx context.Context, req *proxyapi.UsedBoxShareLinkRequest) (*proxyapi.UsedBoxShareLinkResponse, error) {
	err := es.s.incrementShareLinkUsage(ctx, req.ShareToken)
	if err != nil {
		es.s.slog().ErrorContext(ctx, "incrementShareLinkUsage failed in exeproxServer.UsedBoxShareLink", "error", err)
		// Don't bother to return the error,
		// there is nothing the caller can do.
	}
	return &proxyapi.UsedBoxShareLinkResponse{}, nil
}

// Changes returns a stream of changes that exeprox cares about:
// changes to boxes, cookies, box shares, and box share links.
func (es *exeproxServer) Changes(req *proxyapi.ChangesRequest, stream proxyapi.ProxyInfoService_ChangesServer) error {
	// This method has been called in a new goroutine.
	// Register the stream with the proxyChanges goroutine.
	// That goroutine will handle sending data.
	// We pass it a channel to receive errors.
	ch := make(chan error)
	registerProxyChangesStream(stream, ch, es.s.slog())
	return <-ch
}

// proxyChangesStream is one stream to receive notifications of changes.
// We will send events on the stream until it gives an error,
// at which point we will report the error on the channel.
type proxyChangesStream struct {
	grpcStream proxyapi.ProxyInfoService_ChangesServer
	ch         chan error
	lg         *slog.Logger
}

var (
	// proxyChangesMu protects proxyChangeStreams.
	proxyChangesMu sync.Mutex

	// proxyChangesStreams is the list of streams to receive
	// notifications of changes exeprox cares about.
	proxyChangesStreams []proxyChangesStream
)

// proxyChangesChan receives changes that should be sent on the streams.
var proxyChangesChan = make(chan *proxyapi.ChangesResponse, 16)

// startProxyChangesSender starts the goroutine that sends
// changes on the streams.
var startProxyChangesSender = sync.OnceFunc(func() { go streamProxyChanges() })

// registerProxyChangesStream registers a grpc stream that should
// receive change messages.
func registerProxyChangesStream(stream proxyapi.ProxyInfoService_ChangesServer, ch chan error, lg *slog.Logger) {
	startProxyChangesSender()

	proxyChangesMu.Lock()
	defer proxyChangesMu.Unlock()

	proxyChangesStreams = append(proxyChangesStreams,
		proxyChangesStream{stream, ch, lg},
	)
}

// unregisterProxyChangeStream removes an entry from proxyChangesStreams.
// We find the entry based on the channel, as that is a unique pointer.
func unregisterProxyChangesStream(ch chan error) {
	proxyChangesMu.Lock()
	defer proxyChangesMu.Unlock()

	proxyChangesStreams = slices.DeleteFunc(proxyChangesStreams,
		func(acs proxyChangesStream) bool {
			return acs.ch == ch
		},
	)
}

// stopProxyChangesStream is called by test code.
func stopProxyChangesStream() error {
	proxyChangesMu.Lock()

	c := len(proxyChangesStreams)
	var ch chan error
	if c == 1 {
		ch = proxyChangesStreams[0].ch
	}

	proxyChangesMu.Unlock()

	if c != 1 {
		return fmt.Errorf("stopProxyChangesStream: invalid call: %d streams", c)
	}

	// Unregister the stream before signaling it to stop,
	// so that no concurrent streamProxyChanges iteration
	// can send on the dead stream.
	unregisterProxyChangesStream(ch)

	// Send a nil error on the channel to make the call to
	// exeproxServer.Changes return.
	select {
	case ch <- nil:
	default:
	}

	return nil
}

// sendProxyChange sends a proxy change report on all the streams.
func sendProxyChange(proxyChange *proxyapi.ChangesResponse) {
	startProxyChangesSender()

	proxyChangesChan <- proxyChange
}

// streamProxyChanges runs in a goroutine that never exits.
// It sends proxy changes to all the registered streams.
func streamProxyChanges() {
	for {
		proxyChange := <-proxyChangesChan

		proxyChangesMu.Lock()
		for _, stream := range proxyChangesStreams {
			go streamProxyChange(stream, proxyChange)
		}
		proxyChangesMu.Unlock()
	}
}

// streamProxyChange streams a single proxy change report to a single stream.
func streamProxyChange(stream proxyChangesStream, proxyChange *proxyapi.ChangesResponse) {
	if err := stream.grpcStream.Send(proxyChange); err != nil {
		stream.lg.InfoContext(stream.grpcStream.Context(), "failed to send proxy change on stream", "error", err)

		// Do a non-blocking send of the error,
		// as exeproxServer.Changes will stop reading
		// and return on the first error it receives.
		select {
		case stream.ch <- err:
		default:
		}

		unregisterProxyChangesStream(stream.ch)
	}
}

// proxyChangeDeletedBox sends a notification about a deleted box.
func proxyChangeDeletedBox(boxName string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedBox{
			DeletedBox: &proxyapi.DeletedBox{
				BoxName: boxName,
			},
		},
	})
}

// proxyChangeRenamedBox sends a notification about a renamed box.
func proxyChangeRenamedBox(oldName, newName string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_RenamedBox{
			RenamedBox: &proxyapi.RenamedBox{
				OldBoxName: oldName,
				NewBoxName: newName,
			},
		},
	})
}

// proxyChangeUpdatedBoxRoute sends a notification about a
// change to a box routing configuration.
func proxyChangeUpdatedBoxRoute(boxName, createdByUserID string, port int, share string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_UpdatedBoxRoute{
			UpdatedBoxRoute: &proxyapi.UpdatedBoxRoute{
				BoxName:         boxName,
				CreatedByUserID: createdByUserID,
				Route: &proxyapi.BoxRoute{
					Port:  int32(port),
					Share: share,
				},
			},
		},
	})
}

// proxyChangeDeletedCookie sends a notification about a deleted cookie.
func proxyChangeDeletedCookie(cookieValue string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedCookie{
			DeletedCookie: &proxyapi.DeletedCookie{
				Key: &proxyapi.DeletedCookie_CookieValue{
					CookieValue: cookieValue,
				},
			},
		},
	})
}

// proxyChangeDeletedCookiesForUser sends a notification about deleting
// all cookies for a user.
func proxyChangeDeletedCookiesForUser(userID string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedCookie{
			DeletedCookie: &proxyapi.DeletedCookie{
				Key: &proxyapi.DeletedCookie_UserID{
					UserID: userID,
				},
			},
		},
	})
}

// proxyChangeDeletedBoxShare sends a notification about a deleted box share.
func proxyChangeDeletedBoxShare(boxName, sharedWithUserID string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedBoxShare{
			DeletedBoxShare: &proxyapi.DeletedBoxShare{
				BoxName:          boxName,
				SharedWithUserID: sharedWithUserID,
			},
		},
	})
}

// proxyChangeDeletedBoxShareLink sends a notification about
// a deleted box share link.
func proxyChangeDeletedBoxShareLink(boxName, sharedToken string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedBoxShareLink{
			DeletedBoxShareLink: &proxyapi.DeletedBoxShareLink{
				BoxName:    boxName,
				ShareToken: sharedToken,
			},
		},
	})
}
