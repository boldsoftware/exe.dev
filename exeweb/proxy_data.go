package exeweb

import (
	"context"
	"time"

	"exe.dev/email"
)

// ProxyData is an interface for proxy authentication operations.
// Both exed and exeprox implement this interface, exeprox doing
// so by talking to exed.
type ProxyData interface {
	// BoxInfo returns information about a box.
	// The bool result reports whether the box exists.
	BoxInfo(ctx context.Context, boxName string) (BoxData, bool, error)

	// CookieInfo returns information about a cookie.
	// The bool result reports whether the cookie exists.
	CookieInfo(ctx context.Context, cookieValue, domain string) (CookieData, bool, error)

	// UserInfo returns information about a user.
	// The bool result reports whether the user exists.
	UserInfo(ctx context.Context, userID string) (UserData, bool, error)

	// IsUserLockedOut reports whether the user is locked out.
	IsUserLockedOut(ctx context.Context, userID string) (bool, error)

	// UserHasExeSudo reports whether a user has root support privileges.
	UserHasExeSudo(ctx context.Context, userID string) (bool, error)

	// CreateAuthCookie creates a new authentication cookie.
	CreateAuthCookie(ctx context.Context, userID, domain string) (string, error)

	// DeleteAuthCookie deletes an authentication cookie.
	DeleteAuthCookie(ctx context.Context, cookievalue string) error

	// UsedCookie is used to report that an authentication cookie was used.
	UsedCookie(ctx context.Context, cookieValue string)

	// HasUserAccessToBox reports whether a user has access
	// to a box based on box shares with the user's email.
	HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error)

	// IsBoxSharedWithUserTeam erports whether a box is shared
	// with a user's team.
	// TODO: Combine this with HasUserAccessToBox?
	IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error)

	// CheckShareLink reports whether a share link is valid.
	// If the share link is valid, it will be used,
	// so this method is also responsible for recording the use,
	// and for creating an email-based share for the user.
	CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error)

	// ValidateMagicSecret consumes and validates a magic secret
	// created by exed during the authentication flow.
	// TODO(ian): There should be a better approach,
	// one that does not require exeprox to reach back to exed.
	ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error)

	// GetSSHKeyByFingerprint uses the key fingerprint to fetch
	// the corresponding SSH key from the database.
	// It returns the user ID and SSH key.
	GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (userID, key string, err error)

	// HLLNoteEvents notes events for the HyperLogLog tracker.
	HLLNoteEvents(ctx context.Context, userID string, events []string)

	// CheckAndIncrementEmailQuota checks if the user is under
	// their daily limit, and increments if so. It returns a nil
	// error if they are under the limit.
	CheckAndIncrementEmailQuota(ctx context.Context, userID string) error

	// SendEmail sends an email message.
	SendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error
}

// BoxData is the information we need for a box.
type BoxData struct {
	ID                   int      // box ID
	Name                 string   // box name
	Status               string   // box status (running, stopped, ...)
	Ctrhost              string   // exelet name
	CreatedByUserID      string   // user ID that created the box
	Image                string   // image used to create box
	BoxRoute             BoxRoute // box routing configuration
	SSHServerIdentityKey []byte   // SSH server identity private key
	SSHClientPrivateKey  []byte   // box SSH private key
	SSHPort              int      // box SSH port, 0 if not set
	SSHUser              string   // box SSH user
	SupportAccessAllowed int      // root support can access box
}

// BoxRoute is a box routing configuration.
// This is exedb.Route, but we don't want to import exedb.
type BoxRoute struct {
	Port  int
	Share string
}

// CookieData is the information we keep for an authentication cookie.
type CookieData struct {
	CookieValue string    // cookie value
	Domain      string    // cookie host domain
	UserID      string    // user authenticated by cookie
	ExpiresAt   time.Time // expiration time
}

// UserData is the information we need for a user.
type UserData struct {
	UserID string // user ID
	Email  string // user email
}
