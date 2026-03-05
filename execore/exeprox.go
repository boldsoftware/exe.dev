package execore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"
	"exe.dev/sshkey"
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
		BoxExists:            true,
		BoxID:                int64(box.ID),
		Status:               box.Status,
		Ctrhost:              box.Ctrhost,
		CreatedByUserID:      box.CreatedByUserID,
		Image:                box.Image,
		SSHServerIdentityKey: string(box.SSHServerIdentityKey),
		SSHClientPrivateKey:  string(box.SSHClientPrivateKey),
		SupportAccessAllowed: int64(box.SupportAccessAllowed),
	}
	route := box.GetRoute()
	ret.Route = &proxyapi.BoxRoute{
		Port:  int32(route.Port),
		Share: route.Share,
	}
	if box.SSHPort != nil {
		ret.SSHPort = int32(*box.SSHPort)
	}
	if box.SSHUser != nil {
		ret.SSHUser = *box.SSHUser
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

// UserInfo takes a user ID and returns information about that user.
func (es *exeproxServer) UserInfo(ctx context.Context, req *proxyapi.UserInfoRequest) (*proxyapi.UserInfoResponse, error) {
	user, exists, when, err := es.s.userInfoForExeprox(ctx, req.UserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !exists {
		ret := &proxyapi.UserInfoResponse{
			UserExists: false,
			When:       timestamppb.New(when),
		}
		return ret, nil
	}
	ret := &proxyapi.UserInfoResponse{
		UserExists: true,
		When:       timestamppb.New(when),
		UserInfo:   user,
	}
	return ret, nil
}

// userInfoForExeprox builds a [proxyapi.UserInfo] from the database.
// This reports whether the user exists.
// This returns the time the information was retrieved,
// so that exeprox can avoid races.
func (s *Server) userInfoForExeprox(ctx context.Context, userID string) (*proxyapi.UserInfo, bool, time.Time, error) {
	now := time.Now()
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, now, nil
		}
		return nil, false, now, err
	}
	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	accountID := ""
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// user does not have accounting ID. OK for now.
		} else {
			return nil, false, now, err
		}
	} else {
		accountID = account.ID
	}
	// Look up team billing_owner's account ID (best-effort, empty if none).
	teamBillingAccountID := ""
	if tbID, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamBillingOwnerAccountID, userID); err == nil {
		teamBillingAccountID = tbID
	}

	ret := &proxyapi.UserInfo{
		UserID:               userID,
		Email:                user.Email,
		RootSupport:          user.RootSupport,
		IsLockedOut:          user.IsLockedOut,
		AccountID:            accountID,
		TeamBillingAccountID: teamBillingAccountID,
	}
	return ret, true, now, nil
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

// TopLevelCert returns a certificate for a top level domain.
func (es *exeproxServer) TopLevelCert(ctx context.Context, req *proxyapi.TopLevelCertRequest) (*proxyapi.TopLevelCertResponse, error) {
	if es.s.certManager == nil {
		return nil, status.Error(codes.Internal, "no certificate manager configured")
	}

	// We construct an incomplete ClientHelloInfo that we
	// can pass to autocert. Fortunately autocert doesn't need
	// all the fields, but this is definitely a hack.

	hello := &tls.ClientHelloInfo{
		CipherSuites:      fromUint32[uint16](req.CipherSuites),
		ServerName:        req.ServerName,
		SupportedCurves:   fromUint32[tls.CurveID](req.SupportedCurves),
		SupportedPoints:   fromUint32[uint8](req.SupportedPoints),
		SignatureSchemes:  fromUint32[tls.SignatureScheme](req.SignatureSchemes),
		SupportedProtos:   req.SupportedProtos,
		SupportedVersions: fromUint32[uint16](req.SupportedVersions),
		Extensions:        fromUint32[uint16](req.Extensions),
	}

	// Note that behind the scenes it is possible that this
	// will trigger a request to some exeprox to verify
	// a token. The exeprox will forward that request back to us,
	// we will hand off to the certManager, and the right
	// thing will happen. In other words, this might be a recursive
	// call here.

	cert, err := es.s.certManager.GetCertificate(hello)
	if err != nil {
		es.s.slog().WarnContext(ctx, "getting certificate failed in TopLevelCert", "serverName", req.ServerName, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	bytes, err := wildcardcert.EncodeCertificate(cert)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.TopLevelCertResponse{
		Cert: string(bytes),
	}
	return ret, nil
}

// fromUint32 is a helper for TopLevelCert.
func fromUint32[E ~uint16 | ~uint8](s []uint32) []E {
	r := make([]E, len(s))
	for i, v := range s {
		r[i] = E(v)
	}
	return r
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

// LLMUseCredits applies a credit usage entry and returns remaining credir.
// See [llmgateway.GatewayData.UseCredits].
func (es *exeproxServer) LLMUseCredits(ctx context.Context, req *proxyapi.LLMUseCreditsRequest) (*proxyapi.LLMUseCreditsResponse, error) {
	val, err := (&billing.Manager{DB: es.s.db}).SpendCredits(ctx, req.AccountID, int(req.Quantity), tender.Mint(0, req.Microcents))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.LLMUseCreditsResponse{
		Microcents: val.Microcents(),
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

// CreateAuthCookie creates a new authentication cookie.
func (es *exeproxServer) CreateAuthCookie(ctx context.Context, req *proxyapi.CreateAuthCookieRequest) (*proxyapi.CreateAuthCookieResponse, error) {
	cookieValue, err := es.s.createAuthCookie(ctx, req.UserID, req.Domain)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.CreateAuthCookieResponse{
		CookieValue: cookieValue,
	}
	return ret, nil
}

// DeleteAuthCookie deletes an authentication cookie.
func (es *exeproxServer) DeleteAuthCookie(ctx context.Context, req *proxyapi.DeleteAuthCookieRequest) (*proxyapi.DeleteAuthCookieResponse, error) {
	es.s.deleteAuthCookie(ctx, req.CookieValue)
	return &proxyapi.DeleteAuthCookieResponse{}, nil
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

// HasUserAccessToBox reports whether a box is shared with a user.
func (es *exeproxServer) HasUserAccessToBox(ctx context.Context, req *proxyapi.HasUserAccessToBoxRequest) (*proxyapi.HasUserAccessToBoxResponse, error) {
	ok, err := es.s.hasUserAccessToBox(ctx, int(req.BoxID), req.BoxName, req.SharedWithUserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.HasUserAccessToBoxResponse{
		Ok: ok,
	}
	return ret, nil
}

// IsBoxSharedWithUserTeam reports whether a user is a member
// of a team that has access to a box.
func (es *exeproxServer) IsBoxSharedWithUserTeam(ctx context.Context, req *proxyapi.IsBoxSharedWithUserTeamRequest) (*proxyapi.IsBoxSharedWithUserTeamResponse, error) {
	isTeamShared, err := es.s.isBoxSharedWithUserTeam(ctx, int(req.BoxID), req.BoxName, req.SharedWithUserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.IsBoxSharedWithUserTeamResponse{
		Ok: isTeamShared,
	}
	return ret, nil
}

// IsBoxShelleySharedWithTeamMember reports whether a box has team_shelley
// sharing enabled and the user is in the same team as the box creator.
func (es *exeproxServer) IsBoxShelleySharedWithTeamMember(ctx context.Context, req *proxyapi.IsBoxShelleySharedWithTeamMemberRequest) (*proxyapi.IsBoxShelleySharedWithTeamMemberResponse, error) {
	isShared, err := es.s.isBoxShelleySharedWithTeamMember(ctx, int(req.BoxID), req.BoxName, req.UserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &proxyapi.IsBoxShelleySharedWithTeamMemberResponse{Ok: isShared}, nil
}

// CheckShareLink reports whether a share link is valid.
// If the share link is valid, it will be used,
// so this method is also responsible for recording the use,
// and for creating an email-based share for the user.
func (es *exeproxServer) CheckShareLink(ctx context.Context, req *proxyapi.CheckShareLinkRequest) (*proxyapi.CheckShareLinkResponse, error) {
	ok, err := es.s.checkShareLink(ctx, int(req.BoxID), req.BoxName, req.UserID, req.ShareToken)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.CheckShareLinkResponse{
		Ok: ok,
	}
	return ret, nil
}

// SSHKeyByFingerprint fetches an SSH key by its fingerprint.
func (es *exeproxServer) SSHKeyByFingerprint(ctx context.Context, req *proxyapi.SSHKeyByFingerprintRequest) (*proxyapi.SSHKeyByFingerprintResponse, error) {
	userID, publicKey, err := es.s.getSSHKeyByFingerprint(ctx, req.Fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ret := &proxyapi.SSHKeyByFingerprintResponse{
				KeyExists: false,
			}
			return ret, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.SSHKeyByFingerprintResponse{
		KeyExists: true,
		UserID:    userID,
		PublicKey: publicKey,
	}
	return ret, nil
}

// ResolveExe1Token resolves an exe1 token to its exe0 equivalent.
func (es *exeproxServer) ResolveExe1Token(ctx context.Context, req *proxyapi.ResolveExe1TokenRequest) (*proxyapi.ResolveExe1TokenResponse, error) {
	if !sshkey.ValidExe1Token(req.Exe1Token) {
		return &proxyapi.ResolveExe1TokenResponse{TokenExists: false}, nil
	}
	exe0, err := withRxRes1(es.s, ctx, (*exedb.Queries).GetExe1Token, exedb.GetExe1TokenParams{
		Exe1:      req.Exe1Token,
		ExpiresAt: time.Now().Truncate(time.Second),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ret := &proxyapi.ResolveExe1TokenResponse{
				TokenExists: false,
			}
			return ret, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	ret := &proxyapi.ResolveExe1TokenResponse{
		TokenExists: true,
		Exe0Token:   exe0,
	}
	return ret, nil
}

// ValidateMagicSecret consumes and validates a magic secret.
func (es *exeproxServer) ValidateMagicSecret(ctx context.Context, req *proxyapi.ValidateMagicSecretRequest) (*proxyapi.ValidateMagicSecretResponse, error) {
	ms, err := es.s.magicSecrets.Validate(req.Secret)
	if err != nil {
		// This is an error like "invalid secret".
		// We return it as an error message,
		// not as a gRPC error.
		ret := &proxyapi.ValidateMagicSecretResponse{
			ErrorMessage: err.Error(),
		}
		return ret, nil
	}
	ret := &proxyapi.ValidateMagicSecretResponse{
		UserID:      ms.UserID,
		BoxName:     ms.BoxName,
		RedirectUrl: ms.RedirectURL,
	}
	return ret, nil
}

// HLLNoteEvents notes events for the HyperLogLog tracker.
func (es *exeproxServer) HLLNoteEvents(ctx context.Context, req *proxyapi.HLLNoteEventsRequest) (*proxyapi.HLLNoteEventsResponse, error) {
	if es.s.hllTracker != nil {
		for _, event := range req.Events {
			es.s.hllTracker.NoteEvent(event, req.UserID)
		}
	}
	return &proxyapi.HLLNoteEventsResponse{}, nil
}

// CheckAndIncrementEmailQuota checks if the user is under
// their daily limit, and increments if so. It returns a nil
// error if they are under the limit.
func (es *exeproxServer) CheckAndIncrementEmailQuota(ctx context.Context, req *proxyapi.CheckAndIncrementEmailQuotaRequest) (*proxyapi.CheckAndIncrementEmailQuotaResponse, error) {
	err := es.s.checkAndIncrementEmailQuota(ctx, req.UserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &proxyapi.CheckAndIncrementEmailQuotaResponse{}, nil
}

// SendEmail sends an email message.
func (es *exeproxServer) SendEmail(ctx context.Context, req *proxyapi.SendEmailRequest) (*proxyapi.SendEmailResponse, error) {
	var attrs []slog.Attr
	if req.UserID != "" {
		attrs = append(attrs, slog.String("user_id", req.UserID))
	}
	err := es.s.sendEmail(ctx, email.Type(req.EmailType), req.To, req.Subject, req.Body, attrs...)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &proxyapi.SendEmailResponse{}, nil
}

// CheckAndDebitVMEMailCredit debits an email from a box quota.
func (es *exeproxServer) CheckAndDebitVMEmailCredit(ctx context.Context, req *proxyapi.CheckAndDebitVMEmailCreditRequest) (*proxyapi.CheckAndDebitVMEmailCreditResponse, error) {
	err := es.s.checkAndDebitVMEmailCredit(ctx, req.BoxID)
	if err != nil {
		if errors.Is(err, exeweb.ErrVMEmailRateLimited) {
			resp := &proxyapi.CheckAndDebitVMEmailCreditResponse{
				Ok:                false,
				RateLimitExceeded: true,
			}
			return resp, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &proxyapi.CheckAndDebitVMEmailCreditResponse{
		Ok:                true,
		RateLimitExceeded: false,
	}
	return resp, nil
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

// proxyChangeDeletedSSHKey sends a notification about a deleted SSH key.
func proxyChangeDeletedSSHKey(id int, userID, publicKey, fingerprint string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedSSHKey{
			DeletedSSHKey: &proxyapi.DeletedSSHKey{
				ID:          int64(id),
				UserID:      userID,
				PublicKey:   publicKey,
				Fingerprint: fingerprint,
			},
		},
	})
}

// sendProxyUserChange sends a notification about a changed user.
func (s *Server) sendProxyUserChange(ctx context.Context, userID string) {
	ctx = context.WithoutCancel(ctx)
	user, exists, when, err := s.userInfoForExeprox(ctx, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "error retrieving user information", "userID", userID, "error", err)
		return
	}
	if !exists {
		sendProxyChange(&proxyapi.ChangesResponse{
			Action: &proxyapi.ChangesResponse_UserChanged{
				UserChanged: &proxyapi.UserChanged{
					UserExists: false,
					When:       timestamppb.New(when),
					UserInfo: &proxyapi.UserInfo{
						UserID: userID,
					},
				},
			},
		})
	} else {
		sendProxyChange(&proxyapi.ChangesResponse{
			Action: &proxyapi.ChangesResponse_UserChanged{
				UserChanged: &proxyapi.UserChanged{
					UserExists: true,
					When:       timestamppb.New(when),
					UserInfo:   user,
				},
			},
		})
	}
}

// proxyChangeDeletedTeamMember sends a notification about deleting
// a user from a team.
func proxyChangeDeletedTeamMember(teamID, userID string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedTeamMember{
			DeletedTeamMember: &proxyapi.DeletedTeamMember{
				TeamID: teamID,
				UserID: userID,
			},
		},
	})
}

// proxyChangeDeletedBoxShareTeam sends a notification about
// deleting a box share from a team.
func proxyChangeDeletedBoxShareTeam(teamID string, boxID int, boxName string) {
	sendProxyChange(&proxyapi.ChangesResponse{
		Action: &proxyapi.ChangesResponse_DeletedBoxShareTeam{
			DeletedBoxShareTeam: &proxyapi.DeletedBoxShareTeam{
				TeamID:  teamID,
				BoxID:   int64(boxID),
				BoxName: boxName,
			},
		},
	})
}
