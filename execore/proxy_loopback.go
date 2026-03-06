package execore

import (
	"context"
	"errors"

	"exe.dev/email"
	"exe.dev/exeweb"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupLoopbackProxyData creates a gRPC client connected to the
// in-process ProxyInfoService and stores a loopback [exeweb.ProxyData]
// on s. This must be called after [Server.setupExeproxServer].
func (s *Server) setupLoopbackProxyData() {
	conn, err := grpc.NewClient(s.exeproxServiceLn.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		// NewClient only fails on invalid arguments; the address
		// is already validated by startListener.
		panic("loopback grpc client: " + err.Error())
	}
	client := proxyapi.NewProxyInfoServiceClient(conn)
	s.loopbackProxyData = NewLoopbackProxyData(client, s.validateAppToken)
}

// loopbackProxyData implements [exeweb.ProxyData] and
// [exeweb.AppTokenValidator] by making gRPC calls to the in-process
// ProxyInfoService server. This exercises the full gRPC serialization
// and server-side handler path while staying in the same process.
//
// App token validation has no corresponding gRPC RPC, so it is
// handled via a direct in-process callback.
type loopbackProxyData struct {
	client           proxyapi.ProxyInfoServiceClient
	validateAppToken func(ctx context.Context, token string) (string, error)
}

// NewLoopbackProxyData returns a [exeweb.ProxyData] that delegates to
// the given gRPC client. The client is expected to connect to the
// ProxyInfoService running in the same process.
//
// validateAppToken is called for [exeweb.AppTokenValidator]; it may
// be nil if app-token auth is not needed.
func NewLoopbackProxyData(client proxyapi.ProxyInfoServiceClient, validateAppToken func(ctx context.Context, token string) (string, error)) exeweb.ProxyData {
	return &loopbackProxyData{client: client, validateAppToken: validateAppToken}
}

// ValidateAppToken implements [exeweb.AppTokenValidator].
func (lb *loopbackProxyData) ValidateAppToken(ctx context.Context, token string) (string, error) {
	if lb.validateAppToken == nil {
		return "", errors.New("app token validation not supported")
	}
	return lb.validateAppToken(ctx, token)
}

func (lb *loopbackProxyData) BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error) {
	resp, err := lb.client.BoxInfo(ctx, &proxyapi.BoxInfoRequest{
		BoxName: boxName,
	})
	if err != nil {
		return exeweb.BoxData{}, false, err
	}
	if !resp.BoxExists {
		return exeweb.BoxData{}, false, nil
	}
	bd := exeweb.BoxData{
		ID:              int(resp.BoxID),
		Name:            boxName,
		Status:          resp.Status,
		Ctrhost:         resp.Ctrhost,
		CreatedByUserID: resp.CreatedByUserID,
		Image:           resp.Image,
		BoxRoute: exeweb.BoxRoute{
			Port:  int(resp.Route.Port),
			Share: resp.Route.Share,
		},
		SSHServerIdentityKey: []byte(resp.SSHServerIdentityKey),
		SSHClientPrivateKey:  []byte(resp.SSHClientPrivateKey),
		SSHPort:              int(resp.SSHPort),
		SSHUser:              resp.SSHUser,
		SupportAccessAllowed: int(resp.SupportAccessAllowed),
	}
	return bd, true, nil
}

func (lb *loopbackProxyData) CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	resp, err := lb.client.CookieInfo(ctx, &proxyapi.CookieInfoRequest{
		CookieValue: cookieValue,
		Domain:      domain,
	})
	if err != nil {
		return exeweb.CookieData{}, false, err
	}
	if !resp.CookieExists {
		return exeweb.CookieData{}, false, nil
	}
	cd := exeweb.CookieData{
		CookieValue: resp.CookieValue,
		UserID:      resp.UserID,
		Domain:      resp.Domain,
		ExpiresAt:   resp.ExpiresAt.AsTime(),
	}
	return cd, true, nil
}

func (lb *loopbackProxyData) UserInfo(ctx context.Context, userID string) (exeweb.UserData, bool, error) {
	resp, err := lb.client.UserInfo(ctx, &proxyapi.UserInfoRequest{
		UserID: userID,
	})
	if err != nil {
		return exeweb.UserData{}, false, err
	}
	if !resp.UserExists {
		return exeweb.UserData{}, false, nil
	}
	return exeweb.UserData{
		UserID: userID,
		Email:  resp.UserInfo.Email,
	}, true, nil
}

func (lb *loopbackProxyData) IsUserLockedOut(ctx context.Context, userID string) (bool, error) {
	resp, err := lb.client.UserInfo(ctx, &proxyapi.UserInfoRequest{
		UserID: userID,
	})
	if err != nil {
		return false, err
	}
	if !resp.UserExists {
		return false, errors.New("user not found")
	}
	return resp.UserInfo.IsLockedOut, nil
}

func (lb *loopbackProxyData) UserHasExeSudo(ctx context.Context, userID string) (bool, error) {
	resp, err := lb.client.UserInfo(ctx, &proxyapi.UserInfoRequest{
		UserID: userID,
	})
	if err != nil {
		return false, err
	}
	if !resp.UserExists {
		return false, errors.New("user not found")
	}
	return resp.UserInfo.RootSupport == 1, nil
}

func (lb *loopbackProxyData) CreateAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	resp, err := lb.client.CreateAuthCookie(ctx, &proxyapi.CreateAuthCookieRequest{
		UserID: userID,
		Domain: domain,
	})
	if err != nil {
		return "", err
	}
	return resp.CookieValue, nil
}

func (lb *loopbackProxyData) DeleteAuthCookie(ctx context.Context, cookieValue string) error {
	_, err := lb.client.DeleteAuthCookie(ctx, &proxyapi.DeleteAuthCookieRequest{
		CookieValue: cookieValue,
	})
	return err
}

func (lb *loopbackProxyData) UsedCookie(ctx context.Context, cookieValue string) {
	lb.client.UsedCookie(ctx, &proxyapi.UsedCookieRequest{
		CookieValue: cookieValue,
	})
}

func (lb *loopbackProxyData) HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	resp, err := lb.client.HasUserAccessToBox(ctx, &proxyapi.HasUserAccessToBoxRequest{
		BoxID:            int64(boxID),
		BoxName:          boxName,
		SharedWithUserID: userID,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

func (lb *loopbackProxyData) IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	resp, err := lb.client.IsBoxSharedWithUserTeam(ctx, &proxyapi.IsBoxSharedWithUserTeamRequest{
		BoxID:            int64(boxID),
		BoxName:          boxName,
		SharedWithUserID: userID,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

func (lb *loopbackProxyData) IsBoxShelleySharedWithTeamMember(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	resp, err := lb.client.IsBoxShelleySharedWithTeamMember(ctx, &proxyapi.IsBoxShelleySharedWithTeamMemberRequest{
		BoxID:   int64(boxID),
		BoxName: boxName,
		UserID:  userID,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

func (lb *loopbackProxyData) CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	resp, err := lb.client.CheckShareLink(ctx, &proxyapi.CheckShareLinkRequest{
		BoxID:      int64(boxID),
		BoxName:    boxName,
		UserID:     userID,
		ShareToken: shareToken,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

func (lb *loopbackProxyData) ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error) {
	resp, err := lb.client.ValidateMagicSecret(ctx, &proxyapi.ValidateMagicSecretRequest{
		Secret: secret,
	})
	if err != nil {
		return "", "", "", err
	}
	if resp.ErrorMessage != "" {
		return "", "", "", errors.New(resp.ErrorMessage)
	}
	return resp.UserID, resp.BoxName, resp.RedirectUrl, nil
}

func (lb *loopbackProxyData) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (userID, key string, err error) {
	resp, err := lb.client.SSHKeyByFingerprint(ctx, &proxyapi.SSHKeyByFingerprintRequest{
		Fingerprint: fingerprint,
	})
	if err != nil {
		return "", "", err
	}
	if !resp.KeyExists {
		return "", "", errors.New("invalid token")
	}
	return resp.UserID, resp.PublicKey, nil
}

func (lb *loopbackProxyData) ResolveExe1Token(ctx context.Context, exe1Token string) (string, error) {
	resp, err := lb.client.ResolveExe1Token(ctx, &proxyapi.ResolveExe1TokenRequest{
		Exe1Token: exe1Token,
	})
	if err != nil {
		return "", err
	}
	if !resp.TokenExists {
		return "", errors.New("invalid token")
	}
	return resp.Exe0Token, nil
}

func (lb *loopbackProxyData) HLLNoteEvents(ctx context.Context, userID string, events []string) {
	lb.client.HLLNoteEvents(ctx, &proxyapi.HLLNoteEventsRequest{
		UserID: userID,
		Events: events,
	})
}

func (lb *loopbackProxyData) CheckAndIncrementEmailQuota(ctx context.Context, userID string) error {
	_, err := lb.client.CheckAndIncrementEmailQuota(ctx, &proxyapi.CheckAndIncrementEmailQuotaRequest{
		UserID: userID,
	})
	return err
}

func (lb *loopbackProxyData) SendEmail(ctx context.Context, emailType email.Type, to, subject, body, userID, fromName string) error {
	_, err := lb.client.SendEmail(ctx, &proxyapi.SendEmailRequest{
		EmailType: string(emailType),
		To:        to,
		Subject:   subject,
		Body:      body,
		UserID:    userID,
		FromName:  fromName,
	})
	return err
}

func (lb *loopbackProxyData) CheckAndDebitVMEmailCredit(ctx context.Context, boxID int) error {
	resp, err := lb.client.CheckAndDebitVMEmailCredit(ctx, &proxyapi.CheckAndDebitVMEmailCreditRequest{
		BoxID: int64(boxID),
	})
	if err != nil {
		return err
	}
	if !resp.Ok {
		if resp.RateLimitExceeded {
			return exeweb.ErrVMEmailRateLimited
		}
		return errors.New("CheckAndDebitVMEmailCredit failed for an unknown reason")
	}
	return nil
}
