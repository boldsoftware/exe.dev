package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/exeweb"
)

// exe.dev provides a "magic" proxy for user's boxes. When a user requests https://vmname.exe.dev/,
// we terminate TLS, and send that request on to the box using HTTP. This allows users to serve
// web sites without dealing with, for example, TLS. The port we go to is determined by the "route" command.
// We also provide some basic auth. By default, you have to have access to the box (which we do via
// a redirect dance) to have access to the proxy, but we also let you mark it public.
//
// If you have multiple web servers, for certain ports, we also redirect those requests. So,
// https://vmname.exe.dev:8080/ will go to port 8080 on the box. These non-default ports are always
// private.

// handleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches box.exe.dev or box.exe.local
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleProxyRequest(w, r)
}

// isProxyRequest reports whether a request to host should be handled by the proxy.
// The proxy handles requests to VMs, which are can single subdomains of the box domain,
// or third party domains pointing here.
func (s *Server) isProxyRequest(host string) bool {
	return exeweb.IsProxyRequest(&s.env, s.tsDomain, host)
}

// isShelleyRequest determines if a request is for a Shelley subdomain (vm.shelley.exe.xyz)
func (s *Server) isShelleyRequest(host string) bool {
	return exeweb.IsShelleyRequest(&s.env, host)
}

// getAuthenticatedUserID checks if the user is authenticated and returns their userID
// Returns (userID, true) if authenticated, ("", false) if not authenticated.
// It may be called multiple times while handling a single request,
// so it should not mutate r or have other side-effects.
// Note: This only checks cookie-based auth. For full auth including tokens, use getProxyAuth.
func (s *Server) getAuthenticatedUserID(r *http.Request) (string, bool) {
	if userID, err := s.validateProxyAuthCookie(r); err == nil {
		return userID, true
	}
	return "", false
}

// getProxyAuth checks if the user is authenticated for the proxy and returns the auth result.
// Supports three authentication methods, tried in this order:
//  1. Bearer token auth (Authorization: Bearer <token>)
//  2. Basic auth with token as password (for git HTTPS, etc.)
//  3. Cookie-based auth (login-with-exe-* cookies)
//
// For token-based auth, the namespace must be "v0@VMNAME.BOXHOST".
// Returns nil if not authenticated.
func (s *Server) getProxyAuth(r *http.Request, box exedb.Box) *exeweb.ProxyAuthResult {
	return s.proxyServer().GetProxyAuth(r, box.Name)
}

func (s *Server) webBaseURLNoRequest() string {
	scheme := s.bestScheme()
	port := s.bestURLPort()
	if s.env.BehindTLSProxy {
		scheme = "https"
		port = s.httpURLPort()
	}
	return fmt.Sprintf("%s://%s%s", scheme, s.env.WebHost, port)
}

// getProxyPorts returns the list of ports that should be used for proxying.
// TEST_PROXY_PORTS env var overrides the stage config (used by e1e tests).
func (s *Server) getProxyPorts() []int {
	if testPorts := os.Getenv("TEST_PROXY_PORTS"); testPorts != "" {
		var ports []int
		for portStr := range strings.SplitSeq(testPorts, ",") {
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
		return ports
	}
	return s.env.ProxyPorts
}

// getBoxForUser retrieves a box for the given user/team/name
func (s *Server) getBoxForUser(ctx context.Context, publicKey, boxName string) (*exedb.Box, error) {
	user, err := s.getUserByPublicKey(ctx, publicKey)
	if err != nil || user == nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.boxForNameUserID(ctx, boxName, user.UserID)
}

func (s *Server) boxForNameUserID(ctx context.Context, boxName, userID string) (*exedb.Box, error) {
	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByNameAndAlloc, exedb.GetBoxByNameAndAllocParams{
		Name:            boxName,
		CreatedByUserID: userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("VM '%s' not found or access denied", boxName)
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &box, nil
}

// proxyToContainer proxies the HTTP request to a container via SSH port forwarding
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, box *exedb.Box, route exedb.Route, authResult *exeweb.ProxyAuthResult) error {
	// Convert to exeweb data formats.
	// This code is temporary until we move more to exeweb.
	exewebBox := dbBoxToExewebBox(box)

	exewebRoute := exeweb.BoxRoute{
		Port:  route.Port,
		Share: route.Share,
	}

	return s.proxyServer().ProxyToContainer(w, r, &exewebBox, exewebRoute, authResult)
}

// createSSHTunnelTransport creates an HTTP transport that
// tunnels through SSH to a container.
func (s *Server) createSSHTunnelTransport(sshHost string, box *exedb.Box, sshKey ssh.Signer) *http.Transport {
	// Convert to exeweb data formats.
	// This code is temporary until we move more to exeweb.
	exewebBox := dbBoxToExewebBox(box)

	return s.proxyServer().CreateSSHTunnelTransport(sshHost, &exewebBox, sshKey)
}

// hasUserAccessToBox reports whether a box is shared with a user.
func (s *Server) hasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	// Try to resolve any pending shares for this user
	// before checking access.
	// This is a defensive measure to catch any edge cases
	// where pending shares weren't resolved during login
	// (e.g., if we miss a login path in the future).
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err == nil && user.Email != "" {
		if err := s.resolvePendingShares(ctx, user.Email, userID); err != nil {
			return false, fmt.Errorf("resolve pending shares: %w", err)
		}
		if err := s.resolvePendingTeamInvites(ctx, user.Email, userID); err != nil {
			return false, fmt.Errorf("resolve pending team invites: %w", err)
		}
	}

	hasAccess, err := withRxRes1(s, ctx, (*exedb.Queries).HasUserAccessToBox, exedb.HasUserAccessToBoxParams{
		BoxID:            int64(boxID),
		SharedWithUserID: userID,
	})
	return hasAccess, err
}

// isBoxSharedWithUserTeam reports whether the user is a member
// of a team that has access to the box.
func (s *Server) isBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	isTeamShared, err := withRxRes1(s, ctx, (*exedb.Queries).IsBoxSharedWithUserTeam, exedb.IsBoxSharedWithUserTeamParams{
		BoxID:  int64(boxID),
		UserID: userID,
	})
	return isTeamShared, err
}

// checkShareLink reports whether a share link is valid.
// If the share link is valid, it will be used,
// so this method is also responsible for recording the use,
// and for creating an email-based share for the user.
func (s *Server) checkShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	if shareToken == "" {
		return false, nil
	}
	valid, err := s.validateShareLinkForBox(ctx, shareToken, boxID)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, nil
	}

	// The share link is valid. Record that we used it,
	// and auto-create an email-based share for the user.
	// The email-based share allows the user to access the
	// box even if the share link is later revoked.
	if err := s.incrementShareLinkUsage(ctx, shareToken); err != nil {
		// Report the error but don't return it:
		// the share link is still valid.
		s.slog().ErrorContext(ctx, "error incrementing share link usage counter", "shareToken", shareToken, "error", err)
	}
	if err := s.autoCreateShareFromLink(ctx, userID, boxID, shareToken); err != nil {
		// Report the error but don't return it:
		// the share link is still valid.
		s.slog().ErrorContext(ctx, "error auto-creating email share from share link", "userID", userID, "boxID", boxID, "boxName", boxName, "shareToken", shareToken, "error", err)
	}

	return true, nil
}

// incrementShareLinkUsage increments the usage counter for a share link
func (s *Server) incrementShareLinkUsage(ctx context.Context, shareToken string) error {
	return withTx1(s, ctx, (*exedb.Queries).IncrementShareLinkUsage, shareToken)
}

// autoCreateShareFromLink creates an email-based share for a user who accessed via share link
// This allows the user to retain access even if the share link is later revoked
func (s *Server) autoCreateShareFromLink(ctx context.Context, userID string, boxID int, shareToken string) error {
	// Get the share link to find who created it
	shareLink, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxShareLinkByTokenAndBoxID, exedb.GetBoxShareLinkByTokenAndBoxIDParams{
		ShareToken: shareToken,
		BoxID:      int64(boxID),
	})
	if err != nil {
		return err
	}

	// Create email-based share (will fail silently if already exists due to UNIQUE constraint)
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_, err := queries.CreateBoxShare(ctx, exedb.CreateBoxShareParams{
			BoxID:            int64(boxID),
			SharedWithUserID: userID,
			SharedByUserID:   shareLink.CreatedByUserID,
			Message:          nil, // No message for auto-created shares
		})
		// Ignore duplicate errors
		if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil
		}
		return err
	})
}

// proxyServer returns an exeweb.ProxyServer that refers to s.
func (s *Server) proxyServer() *exeweb.ProxyServer {
	ps := &exeweb.ProxyServer{
		Data:            &proxyData{s: s},
		Lg:              s.slog(),
		Env:             &s.env,
		PiperdPort:      s.piperdPort,
		SSHPool:         s.sshPool,
		HTTPMetrics:     s.httpMetrics,
		Templates:       s.templates,
		LobbyIP:         s.LobbyIP,
		PublicIPs:       s.PublicIPs,
		LookupCNAMEFunc: s.lookupCNAMEFunc,
		LookupAFunc:     s.lookupAFunc,
	}
	if s.servingHTTP() {
		ps.HTTPPort = s.httpLn.tcp.Port
	}
	if s.servingHTTPS() {
		ps.HTTPSPort = s.httpsLn.tcp.Port
	}
	return ps
}

// proxyData implements exeweb.ProxyData using a Server.
type proxyData struct {
	s *Server
}

// BoxInfo implements [exeweb.ProxyData.BoxInfo].
func (pd *proxyData) BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error) {
	box, err := exedb.WithRxRes1(pd.s.db, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exeweb.BoxData{}, false, nil
		}
		return exeweb.BoxData{}, false, err
	}
	return dbBoxToExewebBox(&box), true, nil
}

// dbBoxToExewebBox converts a [exedb.Box] to a [exeweb.BoxData].
func dbBoxToExewebBox(box *exedb.Box) exeweb.BoxData {
	exewebBox := exeweb.BoxData{
		ID:                   box.ID,
		Name:                 box.Name,
		Status:               box.Status,
		Ctrhost:              box.Ctrhost,
		CreatedByUserID:      box.CreatedByUserID,
		Image:                box.Image,
		SSHServerIdentityKey: box.SSHServerIdentityKey,
		SSHClientPrivateKey:  box.SSHClientPrivateKey,
		SupportAccessAllowed: int(box.SupportAccessAllowed),
	}
	if box.SSHPort != nil {
		exewebBox.SSHPort = int(*box.SSHPort)
	}
	if box.SSHUser != nil {
		exewebBox.SSHUser = *box.SSHUser
	}
	r := box.GetRoute()
	exewebBox.BoxRoute = exeweb.BoxRoute{
		Port:  r.Port,
		Share: r.Share,
	}
	return exewebBox
}

// CookieInfo implements [exeweb.ProxyData.CookieInfo].
func (pd *proxyData) CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	cookie, err := withRxRes1(pd.s, ctx, (*exedb.Queries).GetAuthCookieInfo, exedb.GetAuthCookieInfoParams{
		CookieValue: cookieValue,
		Domain:      domain,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exeweb.CookieData{}, false, nil
		}
		return exeweb.CookieData{}, false, err
	}
	cd := exeweb.CookieData{
		CookieValue: cookieValue,
		Domain:      domain,
		UserID:      cookie.UserID,
		ExpiresAt:   cookie.ExpiresAt,
	}
	return cd, true, nil
}

// UserInfo implements [exeweb.ProxyData.UserInfo].
func (pd *proxyData) UserInfo(ctx context.Context, userID string) (exeweb.UserData, bool, error) {
	email, err := withRxRes1(pd.s, ctx, (*exedb.Queries).GetEmailByUserID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exeweb.UserData{}, false, nil
		}
		return exeweb.UserData{}, false, err
	}
	userData := exeweb.UserData{
		UserID: userID,
		Email:  email,
	}
	return userData, true, nil
}

// IsUserLockedOut implements [exeweb.ProxyData.IsUserLockedOut].
func (pd *proxyData) IsUserLockedOut(ctx context.Context, userID string) (bool, error) {
	return pd.s.isUserLockedOut(ctx, userID)
}

// UserHasExeSudo implements [exeweb.ProxyData.UserHasExeSudo].
func (pd *proxyData) UserHasExeSudo(ctx context.Context, userID string) (bool, error) {
	valid := pd.s.UserHasExeSudo(ctx, userID)
	return valid, nil
}

// CreateAuthCookie implements [exeweb.ProxyData.CreateAuthCookie].
func (pd *proxyData) CreateAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	return pd.s.createAuthCookie(ctx, userID, domain)
}

// DeleteAuthCookie implements [exeweb.ProxyData.DeleteAuthCookie].
func (pd *proxyData) DeleteAuthCookie(ctx context.Context, cookieValue string) error {
	pd.s.deleteAuthCookie(ctx, cookieValue)
	// Any error was already logged by deleteAuthCookie.
	// There is no useful error to return here.
	return nil
}

// UsedCookie implements [exeweb.ProxyData.UsedCookie].
func (pd *proxyData) UsedCookie(ctx context.Context, cookieValue string) {
	withTx1(pd.s, ctx, (*exedb.Queries).UpdateAuthCookieLastUsed, cookieValue)
}

// HasUserAccessToBox implements [exeweb.ProxyData.HasUserAccessToBox].
func (pd *proxyData) HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return pd.s.hasUserAccessToBox(ctx, boxID, boxName, userID)
}

// IsBoxSharedWithUserTeam implements [exeweb.ProxyData.IsBoxSharedWithUserTeam].
func (pd *proxyData) IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return pd.s.isBoxSharedWithUserTeam(ctx, boxID, boxName, userID)
}

// CheckShareLink implements [exeweb.ProxyData.CheckShareLink].
func (pd *proxyData) CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	return pd.s.checkShareLink(ctx, boxID, boxName, userID, shareToken)
}

// ValidateMagicSecret consumes and validates a magic secret.
func (pd *proxyData) ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error) {
	ms, err := pd.s.magicSecrets.Validate(secret)
	if err != nil {
		return "", "", "", err
	}
	return ms.UserID, ms.BoxName, ms.RedirectURL, nil
}

// GetSSHKeyByFingerprint implements [exeweb.ProxyData.GetSSHKeyByFingerprint].
func (pd *proxyData) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (userID, key string, err error) {
	return pd.s.getSSHKeyByFingerprint(ctx, fingerprint)
}

// HLLNoteEvents implements [exeweb.ProxyData.HLLNoteEvents].
func (pd *proxyData) HLLNoteEvents(ctx context.Context, userID string, events []string) {
	if pd.s.hllTracker == nil {
		return
	}
	for _, event := range events {
		pd.s.hllTracker.NoteEvent(event, userID)
	}
}

// CheckAndIncrementEmailQuota implements [exeweb.ProxyData.CheckAndIncrementEmailQuota].
func (pd *proxyData) CheckAndIncrementEmailQuota(ctx context.Context, userID string) error {
	return pd.s.checkAndIncrementEmailQuota(ctx, userID)
}

// SendEmail implements [exeweb.ProxyData.SendEmail].
func (pd *proxyData) SendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error {
	return pd.s.sendEmail(ctx, emailType, to, subject, body)
}
