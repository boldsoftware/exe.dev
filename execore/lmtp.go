package execore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"mvdan.cc/sh/v3/syntax"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/sshpool2"
	"exe.dev/tracing"
)

// LMTPServer handles incoming emails via LMTP protocol.
type LMTPServer struct {
	server   *Server
	smtp     *smtp.Server
	listener net.Listener
	sockPath string
}

// NewLMTPServer creates a new LMTP server.
// socketPath specifies where to create the Unix socket; empty means disabled.
func NewLMTPServer(s *Server, socketPath string) *LMTPServer {
	return &LMTPServer{
		server:   s,
		sockPath: socketPath,
	}
}

// Start starts the LMTP server listening on a Unix socket.
func (l *LMTPServer) Start(ctx context.Context) error {
	// Ensure the socket directory exists
	sockDir := filepath.Dir(l.sockPath)
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Remove any stale socket file
	_ = os.Remove(l.sockPath)

	// Create Unix socket listener
	ln, err := net.Listen("unix", l.sockPath)
	if err != nil {
		return fmt.Errorf("failed to listen on LMTP socket: %w", err)
	}
	l.listener = ln

	// Set permissions on the socket
	if err := os.Chmod(l.sockPath, 0o660); err != nil {
		l.server.slog().WarnContext(ctx, "failed to set socket permissions", "error", err)
	}

	// Create SMTP server in LMTP mode
	be := &lmtpBackend{server: l.server}

	l.smtp = smtp.NewServer(be)
	l.smtp.LMTP = true
	l.smtp.Domain = l.server.env.BoxHost
	l.smtp.ReadTimeout = 60 * time.Second
	l.smtp.WriteTimeout = 60 * time.Second
	l.smtp.MaxMessageBytes = 1024 * 1024 // 1MB
	l.smtp.MaxRecipients = 10
	l.smtp.AllowInsecureAuth = true // Unix socket is local-only
	l.smtp.EnableSMTPUTF8 = true

	l.server.slog().InfoContext(ctx, "LMTP server starting", "socket", l.sockPath)
	go func() {
		if err := l.smtp.Serve(ln); err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			l.server.slog().ErrorContext(ctx, "LMTP server error", "error", err)
		}
	}()

	return nil
}

// Stop stops the LMTP server.
func (l *LMTPServer) Stop(ctx context.Context) error {
	var errs []error
	if l.smtp != nil {
		if err := l.smtp.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing LMTP server: %w", err))
		}
	}
	if err := os.Remove(l.sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("removing LMTP socket: %w", err))
	}
	return errors.Join(errs...)
}

// SocketPath returns the path to the LMTP socket.
func (l *LMTPServer) SocketPath() string {
	return l.sockPath
}

// lmtpBackend implements smtp.Backend for LMTP.
type lmtpBackend struct {
	server *Server
}

func (b *lmtpBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &lmtpSession{backend: b}, nil
}

// lmtpSession implements smtp.Session for LMTP.
type lmtpSession struct {
	backend *lmtpBackend

	from       string
	recipients []recipientInfo
}

type recipientInfo struct {
	address string
	box     exedb.Box
}

func (s *lmtpSession) AuthPlain(username, password string) error {
	// LMTP doesn't require authentication (local socket only)
	return nil
}

func (s *lmtpSession) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

// Rcpt validates the recipient address and rejects invalid destinations early.
//
// This is the primary security gate for inbound email. All validation happens here,
// BEFORE the DATA phase, so invalid recipients are rejected cheaply without reading
// the message body. The sender receives a proper 550 bounce.
//
// Protections (in order, all return 550 errors):
//  1. Invalid email syntax - malformed addresses rejected immediately
//  2. Wrong domain suffix - only *.{BoxHost} domains accepted (e.g., *.exe.xyz)
//  3. Nested subdomains - only single-level subdomains (boxname.exe.xyz, not a.b.exe.xyz)
//  4. Box not found - database lookup for box name
//  5. Email receive disabled - box must have email_receive_enabled=1
//
// These checks run before maddy reads the message body, minimizing resource usage
// for spam/invalid destinations. See ops/maddy/maddy.conf for why we can't do this
// filtering in maddy itself.
func (s *lmtpSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	// Protection 1: Parse the recipient address (rejects invalid syntax)
	syntax := emailVerifier.ParseAddress(to)
	if !syntax.Valid {
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 1},
			Message:      "Invalid recipient address",
		}
	}
	to = strings.ToLower(syntax.Username + "@" + syntax.Domain)
	domain := strings.ToLower(syntax.Domain)

	// Protection 2: Check domain ends with .{BoxHost} (e.g., .exe.xyz)
	// Rejects external domains like gmail.com, otherdomain.net
	suffix := "." + s.backend.server.env.BoxHost
	if !strings.HasSuffix(domain, suffix) {
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 2},
			Message:      "Invalid domain",
		}
	}

	// Protection 3: Only single-level subdomains allowed
	// Accepts: boxname.exe.xyz
	// Rejects: a.b.exe.xyz (nested), .exe.xyz (empty boxname)
	boxName := strings.TrimSuffix(domain, suffix)
	if boxName == "" || strings.Contains(boxName, ".") {
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 2},
			Message:      "Invalid domain",
		}
	}

	// Protection 4 & 5: Box must exist AND have email_receive_enabled=1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	box, err := exedb.WithRxRes1(s.backend.server.db, ctx, (*exedb.Queries).GetBoxByNameWithEmailReceiveEnabled, boxName)
	if err != nil {
		s.backend.server.slog().DebugContext(ctx, "LMTP recipient rejected", "to", to, "box", boxName, "reason", "not found or email disabled")
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 1, 1},
			Message:      "Mailbox not found",
		}
	}

	s.recipients = append(s.recipients, recipientInfo{
		address: to,
		box:     box,
	})
	return nil
}

// Data implements smtp.Session. Required by the interface but we use LMTPData.
func (s *lmtpSession) Data(r io.Reader) error {
	s.backend.server.slog().Error("lmtpSession.Data called; expected LMTPData for LMTP mode")
	return &smtp.SMTPError{
		Code:         451,
		EnhancedCode: smtp.EnhancedCode{4, 3, 0},
		Message:      "Internal server error",
	}
}

// LMTPData implements smtp.LMTPSession for per-recipient status reporting.
func (s *lmtpSession) LMTPData(r io.Reader, status smtp.StatusCollector) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Attach a trace ID so all log lines for this delivery share it and we can
	// surface it to owners when we email them about a delivery failure.
	ctx = tracing.ContextWithTraceID(ctx, tracing.GenerateTraceID())

	const maxSize = 1024 * 1024 // 1MB

	// Read the entire message
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 3, 0},
			Message:      "Failed to read message",
		}
	}

	if len(data) > maxSize {
		return &smtp.SMTPError{
			Code:         552,
			EnhancedCode: smtp.EnhancedCode{5, 3, 4},
			Message:      "Message too large",
		}
	}

	// Deliver to each recipient with per-recipient status
	for _, rcpt := range s.recipients {
		if err := deliverEmailToBox(ctx, s.backend.server.sshPool, &rcpt.box, rcpt.address, data); err != nil {
			s.backend.server.slog().ErrorContext(ctx, "LMTP delivery failed",
				"to", rcpt.address, "box", rcpt.box.Name, "error", err)
			s.recordAndMaybeNotifyDeliveryFailure(ctx, &rcpt.box, err)
			status.SetStatus(rcpt.address, &smtp.SMTPError{
				Code:         451,
				EnhancedCode: smtp.EnhancedCode{4, 3, 0},
				Message:      "Temporary delivery failure",
			})
		} else {
			s.backend.server.slog().InfoContext(ctx, "LMTP delivery succeeded", "from", s.from, "to", rcpt.address, "box", rcpt.box.Name)
			status.SetStatus(rcpt.address, nil)

			// Clear any accumulated failure rows so the notification throttle
			// is per-incident. Without this, a box that recovers between
			// incidents (disk full → user frees space → succeeds → disk full
			// again days later, still within lmtpDeliveryNotifyInterval) would
			// silently skip the second notification.
			//
			// Read-check first: the overwhelming majority of boxes have zero
			// failure rows, so a blind DELETE would take a write transaction
			// on the hot path for a no-op. HasLMTPDeliveryFailures runs under
			// a read transaction (no write lock in WAL mode).
			has, err := exedb.WithRxRes1(s.backend.server.db, ctx, (*exedb.Queries).HasLMTPDeliveryFailures, rcpt.box.ID)
			if err != nil {
				s.backend.server.slog().ErrorContext(ctx, "failed to check LMTP delivery failures", "box", rcpt.box.Name, "error", err)
			} else if has != 0 {
				if err := exedb.WithTx1(s.backend.server.db, ctx, (*exedb.Queries).ClearLMTPDeliveryFailures, rcpt.box.ID); err != nil {
					s.backend.server.slog().ErrorContext(ctx, "failed to clear LMTP delivery failures", "box", rcpt.box.Name, "error", err)
				}
			}

			// Check email count and auto-disable if over limit
			s.checkAndEnforceEmailLimit(ctx, &rcpt.box)
		}
	}

	return nil
}

func (s *lmtpSession) Reset() {
	s.from = ""
	s.recipients = nil
}

func (s *lmtpSession) Logout() error {
	return nil
}

// deliverEmailToBox delivers an email to a box via SSH using the connection pool.
// It uses a content-addressable filename based on the SHA256 hash of the data.
// The maildir directories are created when email receiving is enabled via the
// share receive-email command.
// A Delivered-To header is prepended to identify the envelope recipient.
func deliverEmailToBox(ctx context.Context, pool *sshpool2.Pool, box *exedb.Box, recipient string, data []byte) error {
	if box.EmailMaildirPath == "" {
		return fmt.Errorf("maildir path not configured")
	}

	// Prepend Delivered-To header
	header := fmt.Appendf(nil, "Delivered-To: %s\r\n", recipient)

	// Compute hash without concatenating
	h := sha256.New()
	h.Write(header)
	h.Write(data)
	hash := h.Sum(nil)

	filename := hex.EncodeToString(hash) + ".eml"
	remotePath := box.EmailMaildirPath + "/new/" + filename
	reader := io.MultiReader(bytes.NewReader(header), bytes.NewReader(data))
	return scpToBox(ctx, pool, box, reader, remotePath, 0o644)
}

// checkAndEnforceEmailLimit checks if the number of emails in the maildir exceeds the limit.
// If so, it disables email receiving for the box and sends a notification to the owner.
func (s *lmtpSession) checkAndEnforceEmailLimit(ctx context.Context, box *exedb.Box) {
	srv := s.backend.server

	// Count emails in Maildir/new
	count, err := s.countMaildirEmails(ctx, box)
	if err != nil {
		srv.slog().WarnContext(ctx, "failed to count maildir emails", "box", box.Name, "error", err)
		return
	}

	if count < srv.env.MaxMaildirEmails {
		return
	}

	srv.slog().WarnContext(ctx, "email limit exceeded, disabling receive",
		"box", box.Name, "count", count, "limit", srv.env.MaxMaildirEmails)

	// Disable email receiving in the database (clear maildir path too)
	if err := exedb.WithTx1(srv.db, ctx, (*exedb.Queries).SetBoxEmailReceive, exedb.SetBoxEmailReceiveParams{
		EmailReceiveEnabled: 0,
		EmailMaildirPath:    "",
		ID:                  box.ID,
	}); err != nil {
		srv.slog().ErrorContext(ctx, "failed to disable email receive", "box", box.Name, "error", err)
		return
	}

	// Notify the box owner
	s.notifyOwnerEmailLimitExceeded(ctx, box.Name, count)
}

// countMaildirEmails counts the number of files in the maildir's new directory.
func (s *lmtpSession) countMaildirEmails(ctx context.Context, box *exedb.Box) (int, error) {
	if box.EmailMaildirPath == "" {
		return 0, fmt.Errorf("maildir path not configured")
	}
	// Count files in Maildir/new using find with -printf for accurate counting.
	// The path is quoted to handle spaces safely.
	quotedPath, err := syntax.Quote(box.EmailMaildirPath+"/new", syntax.LangBash)
	if err != nil {
		return 0, fmt.Errorf("failed to quote maildir path: %w", err)
	}
	cmd := fmt.Sprintf("find %s -maxdepth 1 -type f -printf '.' 2>/dev/null | wc -c", quotedPath)
	output, err := runCommandOnBox(ctx, s.backend.server.sshPool, box, cmd)
	if err != nil {
		return 0, fmt.Errorf("failed to count emails: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse email count: %w", err)
	}
	return count, nil
}

// lmtpDeliveryNotifyInterval is the minimum time between notification emails
// sent to a box owner for the same (box, error class) pair.
const lmtpDeliveryNotifyInterval = 3 * 24 * time.Hour

// lmtpErrorClass categorizes LMTP delivery errors so we can group failures and
// tailor the notification copy. The string value is also persisted.
type lmtpErrorClass string

const (
	lmtpErrDiskFull       lmtpErrorClass = "disk_full"
	lmtpErrMaildirMissing lmtpErrorClass = "maildir_missing"
	lmtpErrOther          lmtpErrorClass = "other"
)

// classifyLMTPError inspects err's message for known markers and returns a
// coarse category. Matching is case-insensitive against substrings so it
// tolerates wrapped errors that embed remote stderr (see scpToBox).
func classifyLMTPError(err error) lmtpErrorClass {
	if err == nil {
		return lmtpErrOther
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no space left on device"),
		strings.Contains(msg, "disk full"),
		strings.Contains(msg, "quota exceeded"):
		return lmtpErrDiskFull
	case strings.Contains(msg, "maildir path not configured"),
		// The remote Maildir directory (or its "new" subdir) has been
		// deleted after email receiving was enabled. scpToBox fails at the
		// mv step; the destination path embedded in the wrapped cmd= includes
		// "Maildir/new", and the stderr output contains "No such file or
		// directory". Requiring both substrings keeps the classifier from
		// over-matching generic path errors unrelated to the maildir.
		strings.Contains(msg, "no such file or directory") && strings.Contains(msg, "maildir"):
		return lmtpErrMaildirMissing
	default:
		return lmtpErrOther
	}
}

// recordAndMaybeNotifyDeliveryFailure persists the failure in
// lmtp_delivery_failures and, for user-actionable error classes, dispatches a
// background notification if the lmtpDeliveryNotifyInterval has elapsed since
// the last one. Only disk_full and maildir_missing are surfaced to owners;
// other classes (SSH timeouts, unclassified errors) are recorded for
// operators but not emailed, since they are often transient (e.g. a stopped
// VM) and the user has nothing actionable to do.
func (s *lmtpSession) recordAndMaybeNotifyDeliveryFailure(ctx context.Context, box *exedb.Box, deliveryErr error) {
	srv := s.backend.server
	class := classifyLMTPError(deliveryErr)
	// Cap the stored error message so one pathological error doesn't bloat the row.
	lastErr := deliveryErr.Error()
	if len(lastErr) > 1000 {
		lastErr = lastErr[:1000]
	}

	if err := exedb.WithTx1(srv.db, ctx, (*exedb.Queries).RecordLMTPDeliveryFailure, exedb.RecordLMTPDeliveryFailureParams{
		BoxID:      box.ID,
		ErrorClass: string(class),
		LastError:  lastErr,
	}); err != nil {
		srv.slog().ErrorContext(ctx, "failed to record LMTP delivery failure",
			"box", box.Name, "error_class", class, "error", err)
		return
	}

	if class != lmtpErrDiskFull && class != lmtpErrMaildirMissing {
		return
	}

	// Dispatch the notification asynchronously so the LMTP response is not
	// held hostage by the Postmark call (which can be slow). The claim is
	// atomic in SQL, so concurrent deliveries for the same (box, class) produce
	// at most one email per interval; if the subsequent send fails we drop the
	// notification rather than retry, preferring zero emails to two.
	go s.claimAndNotifyDeliveryFailure(context.WithoutCancel(ctx), box, class)
}

// claimAndNotifyDeliveryFailure atomically claims the notification slot for
// (box, class) and, on success, emails the owner. Called from a goroutine;
// not tied to the LMTP context deadline.
func (s *lmtpSession) claimAndNotifyDeliveryFailure(ctx context.Context, box *exedb.Box, class lmtpErrorClass) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	srv := s.backend.server

	_, err := exedb.WithTxRes1(srv.db, ctx, (*exedb.Queries).ClaimLMTPDeliveryFailureNotification, exedb.ClaimLMTPDeliveryFailureNotificationParams{
		BoxID:           box.ID,
		ErrorClass:      string(class),
		IntervalSeconds: int64(lmtpDeliveryNotifyInterval.Seconds()),
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Another delivery already claimed the slot, or the interval has not
		// elapsed. Nothing to do.
		return
	}
	if err != nil {
		srv.slog().ErrorContext(ctx, "failed to claim LMTP delivery failure notification",
			"box", box.Name, "error_class", class, "error", err)
		return
	}

	// Best-effort: if the send below fails, we do not roll back the claim.
	// Zero emails on a transient Postmark hiccup is preferable to two emails.
	s.notifyOwnerLMTPDeliveryFailed(ctx, box, class)
}

// notifyOwnerLMTPDeliveryFailed sends an email to the box owner informing
// them that inbound email delivery is failing. The caller is responsible for
// having atomically claimed the notification slot beforehand.
func (s *lmtpSession) notifyOwnerLMTPDeliveryFailed(ctx context.Context, box *exedb.Box, class lmtpErrorClass) {
	srv := s.backend.server

	boxInfo, err := exedb.WithRxRes1(srv.db, ctx, (*exedb.Queries).GetBoxWithOwnerEmailByID, box.ID)
	if err != nil {
		srv.slog().ErrorContext(ctx, "failed to get box owner email for LMTP delivery failure", "box", box.Name, "error", err)
		return
	}

	env := srv.env
	subject := fmt.Sprintf("Email delivery failing for %s.%s", box.Name, env.BoxHost)

	var reason string
	switch class {
	case lmtpErrDiskFull:
		reason = fmt.Sprintf(`%s.%s is out of disk space. Delivery will resume automatically once there is free space. You can resize %s.%s's disk by running:

    ssh %s resize %s --disk=<size>`, box.Name, env.BoxHost, box.Name, env.BoxHost, env.ReplHost, box.Name)
	case lmtpErrMaildirMissing:
		reason = fmt.Sprintf(`The Maildir directory is not available on %s.%s (it may have been deleted). Re-create it and re-enable inbound email by running:

    ssh %s share receive-email %s on`, box.Name, env.BoxHost, env.ReplHost, box.Name)
	default:
		// Defense in depth: the caller gates on the two classes above, so
		// reaching here means the gate and this switch have drifted. Log and
		// bail rather than send an email with an empty reason paragraph.
		srv.slog().ErrorContext(ctx, "unhandled LMTP error class in notification", "box", box.Name, "error_class", class)
		return
	}

	body := fmt.Sprintf(`Inbound email delivery to %s.%s is failing.

%s

This message will repeat once every 3 days, as long as the issue continues.

You may disable inbound emails by running:

    ssh %s share receive-email %s off

Trace ID: %s
`, box.Name, env.BoxHost, reason, env.ReplHost, box.Name, tracing.TraceIDFromContext(ctx))

	if err := srv.sendEmail(ctx, sendEmailParams{
		emailType: email.TypeLMTPDeliveryFailure,
		to:        boxInfo.OwnerEmail,
		subject:   subject,
		body:      body,
		fromName:  "",
		replyTo:   "",
		attrs: []slog.Attr{
			slog.String("user_id", boxInfo.CreatedByUserID),
			slog.String("box", box.Name),
			slog.String("error_class", string(class)),
		},
	}); err != nil {
		srv.slog().ErrorContext(ctx, "failed to send LMTP delivery failure notification",
			"box", box.Name, "to", boxInfo.OwnerEmail, "error_class", class, "error", err)
		return
	}

	srv.slog().InfoContext(ctx, "sent LMTP delivery failure notification",
		"box", box.Name, "to", boxInfo.OwnerEmail, "error_class", class)
}

// notifyOwnerEmailLimitExceeded sends an email to the box owner informing them
// that inbound email has been disabled due to exceeding the limit.
func (s *lmtpSession) notifyOwnerEmailLimitExceeded(ctx context.Context, boxName string, count int) {
	srv := s.backend.server
	if srv.emailSenders == nil || srv.emailSenders.Any() == nil {
		srv.slog().WarnContext(ctx, "cannot send email limit notification: no email sender configured", "box", boxName)
		return
	}

	// Get box owner's email
	boxInfo, err := exedb.WithRxRes1(srv.db, ctx, (*exedb.Queries).GetBoxWithOwnerEmail, boxName)
	if err != nil {
		srv.slog().ErrorContext(ctx, "failed to get box owner email", "box", boxName, "error", err)
		return
	}

	sender := srv.emailSenders.Any()
	env := srv.env
	from := fmt.Sprintf("support@%s", env.WebHost)
	subject := fmt.Sprintf("Inbound email disabled for %s.%s", boxName, env.BoxHost)
	body := fmt.Sprintf(`Hi,

Inbound email for %s.%s has been automatically disabled because the number of emails in ~/Maildir/new (%d) exceeds the limit (%d).

To re-enable inbound email:

1. Move or delete some emails from ~/Maildir/new
2. ssh %s share receive-email %s on

Regards,
%s
`, boxName, env.BoxHost, count, env.MaxMaildirEmails, env.ReplHost, boxName, env.WebHost)

	if err := sender.Send(ctx, email.Message{
		Type:    email.TypeEmailLimitExceeded,
		From:    from,
		To:      boxInfo.OwnerEmail,
		Subject: subject,
		Body:    body,
		ReplyTo: "",
		Attrs:   []slog.Attr{slog.String("user_id", boxInfo.CreatedByUserID)},
	}); err != nil {
		srv.slog().ErrorContext(ctx, "failed to send email limit notification", "box", boxName, "to", boxInfo.OwnerEmail, "error", err)
	} else {
		srv.slog().InfoContext(ctx, "sent email limit notification", "box", boxName, "to", boxInfo.OwnerEmail)
	}
}
