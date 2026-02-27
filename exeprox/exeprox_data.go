package exeprox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"

	"exe.dev/email"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"
	"exe.dev/publicips"
	"exe.dev/wildcardcert"
)

// ExeproxData is an interface for fetching data needed by exeprox.
// Currently the normal mechanism in production is to fetch
// data from exed.
//
// In the future we might instead replicate the exed database to the exeprox
// machines, and change this interface to read from the local replica.
//
// Where possible and appropriate, this interface matches [exeweb.ProxyData].
type ExeproxData interface {
	// BoxInfo returns information about a box.
	// The bool result reports whether the box exists.
	BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error)

	// CookieInfo returns information about a cookie.
	// The bool result reports whether the cookie exists.
	CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error)

	// UserInfo returns information about a user.
	// The bool result reports whether the user exists.
	UserInfo(ctx context.Context, userID string) (userData, bool, error)

	// PublicIPs returns a map of private (local address) IPs
	// to public IP / domain / shard.
	// It also returns the IP of the lobby, aka ssh exe.dev.
	PublicIPs(context.Context) (map[netip.Addr]publicips.PublicIP, netip.Addr, error)

	// CertForDomain returns the wildcard cert for a subdomain.
	CertForDomain(ctx context.Context, serverName string) (*tls.Certificate, error)

	// LLMGateway returns the data to use for the LLM gateway functions.
	LLMGateway() llmgateway.GatewayData

	// CreateAuthCookie creates an authentication cookie.
	CreateAuthCookie(ctx context.Context, userID, domain string) (string, error)

	// DeleteAuthCookie deletes an authentication cookie.
	DeleteAuthCookie(ctx context.Context, cookievalue string) error

	// UsedCookie is used to report that an authentication cookie was used.
	UsedCookie(ctx context.Context, cookieValue string)

	// HasUserAccessToBox reports whether a user has access
	// to a box based on box shares with the user's email.
	HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error)

	// IsBoxSharedWithUserTeam reports whether a user is a
	// member of a team that has access to a box.
	IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error)

	// CheckShareLink reports whether a share link is valid.
	// If the share link is valid, it will be used,
	// so this method is also responsible for recording the use,
	// and for creating an email-based share for the user.
	CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error)

	// SSHKeyByFingerprint fetches an SSH key using the fingerprint.
	// The bool result reports whether the key exists.
	SSHKeyByFingerprint(ctx context.Context, fingerprint string) (sshKeyData, bool, error)

	// ValidateMagicSecret consumes and validates a magic secret
	// created by exed during the authentication flow.
	// TODO(ian): There should be a better approach,
	// one that does not require exeprox to reach back to exed.
	ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error)

	// HLLNoteEvents notes events for the HyperLogLog tracker.
	HLLNoteEvents(ctx context.Context, userID string, events []string)

	// CheckAndIncrementEmailQuota checks if the user is under
	// their daily limit, and increments if so. It returns a nil
	// error if they are under the limit.
	CheckAndIncrementEmailQuota(ctx context.Context, userID string) error

	// SendEmail sends an email message.
	SendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error

	// CheckAndDebitVMEMailCredit checks if a box has email
	// credit available, and debits 1 email.
	// If there is no credit available, the error is
	// [ErrVMEmailRateLimited].
	CheckAndDebitVMEmailCredit(ctx context.Context, boxID int) error
}

// grpcExeproxData is an implementation of Exeproxdata that uses a gRPC
// connection to exed to fetch data.
type grpcExeproxData struct {
	client proxyapi.ProxyInfoServiceClient
	lg     *slog.Logger
	boxes  *boxesData
	users  *usersData
}

// newGRPCExeproxData returns an ExeproxData that uses gRPC.
func newGRPCExeproxData(client proxyapi.ProxyInfoServiceClient, lg *slog.Logger, boxes *boxesData, users *usersData) ExeproxData {
	return &grpcExeproxData{
		client: client,
		lg:     lg,
		boxes:  boxes,
		users:  users,
	}
}

// BoxInfo fetches information about a box using a grpc client.
func (ged *grpcExeproxData) BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error) {
	ged.lg.DebugContext(ctx, "fetching box info", "boxName", boxName)
	resp, err := ged.client.BoxInfo(ctx, &proxyapi.BoxInfoRequest{
		BoxName: boxName,
	})
	if err != nil {
		return exeweb.BoxData{}, false, err
	}
	if !resp.BoxExists {
		ged.lg.DebugContext(ctx, "fetching box info does not exist", "boxName", boxName)
		return exeweb.BoxData{}, false, nil
	}

	ged.lg.DebugContext(ctx, "fetching box info exists", "boxName", boxName, "boxID", resp.BoxID)
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

// CookieInfo returns information about a cookie.
func (ged *grpcExeproxData) CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	ged.lg.DebugContext(ctx, "fetching cookie info", "cookieValue", cookieValue, "domain", domain)
	resp, err := ged.client.CookieInfo(ctx, &proxyapi.CookieInfoRequest{
		CookieValue: cookieValue,
		Domain:      domain,
	})
	if err != nil {
		return exeweb.CookieData{}, false, err
	}
	if !resp.CookieExists {
		ged.lg.DebugContext(ctx, "fetching cookie info does not exist", "cookieValue", cookieValue, "domain", domain)
		return exeweb.CookieData{}, false, nil
	}

	ged.lg.DebugContext(ctx, "fetching cookie info exists", "cookieValue", cookieValue, "domain", domain, "userID", resp.UserID)
	cd := exeweb.CookieData{
		CookieValue: resp.CookieValue,
		UserID:      resp.UserID,
		Domain:      resp.Domain,
		ExpiresAt:   resp.ExpiresAt.AsTime(),
	}
	return cd, true, nil
}

// UserInfo returns information about a user.
func (ged *grpcExeproxData) UserInfo(ctx context.Context, userID string) (userData, bool, error) {
	ged.lg.DebugContext(ctx, "fetching user info", "userID", userID)
	resp, err := ged.client.UserInfo(ctx, &proxyapi.UserInfoRequest{
		UserID: userID,
	})
	if err != nil {
		return userData{}, false, err
	}
	if !resp.UserExists {
		return userData{}, false, nil
	}
	ud := userData{
		userID:      userID,
		email:       resp.UserInfo.Email,
		rootSupport: resp.UserInfo.RootSupport,
		isLockedOut: resp.UserInfo.IsLockedOut,
		accountID:   resp.UserInfo.AccountID,
	}
	return ud, true, nil
}

// PublicIPs fetches the public IPs using a grpc client.
func (ged *grpcExeproxData) PublicIPs(ctx context.Context) (map[netip.Addr]publicips.PublicIP, netip.Addr, error) {
	stream, err := ged.client.GetPublicIPs(ctx, &proxyapi.GetPublicIPsRequest{})
	if err != nil {
		return nil, netip.Addr{}, err
	}

	m := make(map[netip.Addr]publicips.PublicIP)
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, netip.Addr{}, err
		}

		privateIP, err := netip.ParseAddr(resp.Addr)
		if err != nil {
			return nil, netip.Addr{}, err
		}

		publicIP, err := netip.ParseAddr(resp.PublicIP.IP)
		if err != nil {
			return nil, netip.Addr{}, err
		}

		m[privateIP] = publicips.PublicIP{
			IP:     publicIP,
			Domain: resp.PublicIP.Domain,
			Shard:  int(resp.PublicIP.Shard),
		}
	}

	resp, err := ged.client.GetLobbyIP(ctx, &proxyapi.GetLobbyIPRequest{})
	if err != nil {
		return nil, netip.Addr{}, err
	}
	lobbyIP, err := netip.ParseAddr(resp.IP)
	if err != nil {
		return nil, netip.Addr{}, err
	}

	return m, lobbyIP, nil
}

// CertForDomain gets the wildcard certificate for a subdomain.
func (ged *grpcExeproxData) CertForDomain(ctx context.Context, serverName string) (*tls.Certificate, error) {
	resp, err := ged.client.CertForDomain(ctx, &proxyapi.CertForDomainRequest{
		ServerName: serverName,
	})
	if err != nil {
		return nil, err
	}
	cert, err := wildcardcert.DecodeCertificate([]byte(resp.Cert))
	if err != nil {
		return nil, fmt.Errorf("CertForDomain decode failed: %v", err)
	}
	return cert, nil
}

// LLMGateway returns the data to use for the llmgateway functions.
func (ged *grpcExeproxData) LLMGateway() llmgateway.GatewayData {
	return &ProxyLLMGatewayData{
		client: ged.client,
		data:   ged,
		boxes:  ged.boxes,
		users:  ged.users,
	}
}

// CreateAuthCookie creates an authentication cookie.
func (ged *grpcExeproxData) CreateAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	resp, err := ged.client.CreateAuthCookie(ctx, &proxyapi.CreateAuthCookieRequest{
		UserID: userID,
		Domain: domain,
	})
	if err != nil {
		return "", err
	}
	return resp.CookieValue, nil
}

// DeleteAuthCookie deletes an authentication cookie.
func (ged *grpcExeproxData) DeleteAuthCookie(ctx context.Context, cookieValue string) error {
	_, err := ged.client.DeleteAuthCookie(ctx, &proxyapi.DeleteAuthCookieRequest{
		CookieValue: cookieValue,
	})
	return err
}

// UsedCookie reports that a cookie was used.
func (ged *grpcExeproxData) UsedCookie(ctx context.Context, cookieValue string) {
	_, err := ged.client.UsedCookie(ctx, &proxyapi.UsedCookieRequest{
		CookieValue: cookieValue,
	})
	if err != nil {
		ged.lg.ErrorContext(ctx, "failed to report used cookie", "cookieValue", cookieValue, "error", err)
	}
}

// HasUserAccessToBox reports whether a box is shared with a user.
func (ged *grpcExeproxData) HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	resp, err := ged.client.HasUserAccessToBox(ctx, &proxyapi.HasUserAccessToBoxRequest{
		BoxID:            int64(boxID),
		BoxName:          boxName,
		SharedWithUserID: userID,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

// IsBoxSharedWithUserTeam reports whether a user is a member
// of a team that has access to a box.
func (ged *grpcExeproxData) IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	resp, err := ged.client.IsBoxSharedWithUserTeam(ctx, &proxyapi.IsBoxSharedWithUserTeamRequest{
		BoxID:            int64(boxID),
		BoxName:          boxName,
		SharedWithUserID: userID,
	})
	if err != nil {
		return false, err
	}
	return resp.Ok, nil
}

// CheckShareLink reports whether a share link is valid.
// If the share link is valid, it will be used,
// so this call is also responsible for recording the use,
// and for creating an email-based share for the user.
func (ged *grpcExeproxData) CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	resp, err := ged.client.CheckShareLink(ctx, &proxyapi.CheckShareLinkRequest{
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

// SSHKeyByFingerprint fetches an SSH key from its fingerprint.
func (ged *grpcExeproxData) SSHKeyByFingerprint(ctx context.Context, fingerprint string) (sshKeyData, bool, error) {
	resp, err := ged.client.SSHKeyByFingerprint(ctx, &proxyapi.SSHKeyByFingerprintRequest{
		Fingerprint: fingerprint,
	})
	if err != nil {
		return sshKeyData{}, false, err
	}
	if !resp.KeyExists {
		return sshKeyData{}, false, nil
	}
	skd := sshKeyData{
		fingerprint: fingerprint,
		userID:      resp.UserID,
		publicKey:   resp.PublicKey,
	}
	return skd, true, nil
}

// ValidateMagicSecret consumes and validates a magic secret.
func (ged *grpcExeproxData) ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error) {
	resp, err := ged.client.ValidateMagicSecret(ctx, &proxyapi.ValidateMagicSecretRequest{
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

// HLLNoteEvents notes events for the HyperLogLog tracker.
func (ged *grpcExeproxData) HLLNoteEvents(ctx context.Context, userID string, events []string) {
	_, err := ged.client.HLLNoteEvents(ctx, &proxyapi.HLLNoteEventsRequest{
		UserID: userID,
		Events: events,
	})
	if err != nil {
		ged.lg.ErrorContext(ctx, "failed to report HLL event", "userID", userID, "events", events, "error", err)
	}
}

// CheckAndIncrementEmailQuota checks if the user is under
// their daily limit, and increments if so. It returns a nil
// error if they are under the limit.
func (ged *grpcExeproxData) CheckAndIncrementEmailQuota(ctx context.Context, userID string) error {
	_, err := ged.client.CheckAndIncrementEmailQuota(ctx, &proxyapi.CheckAndIncrementEmailQuotaRequest{
		UserID: userID,
	})
	return err
}

// SendEmail sends an email message.
func (ged *grpcExeproxData) SendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error {
	_, err := ged.client.SendEmail(ctx, &proxyapi.SendEmailRequest{
		EmailType: string(emailType),
		To:        to,
		Subject:   subject,
		Body:      body,
	})
	return err
}

// CheckAndDebitVMEmailCredit debits an email credit for a box.
func (ged *grpcExeproxData) CheckAndDebitVMEmailCredit(ctx context.Context, boxID int) error {
	resp, err := ged.client.CheckAndDebitVMEmailCredit(ctx, &proxyapi.CheckAndDebitVMEmailCreditRequest{
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
