package exeweb

import (
	"context"
)

// ProxyData is an interface for proxy authentication operations.
// Both exed and exeprox implement this interface, exeprox doing
// so by talking to exed.
type ProxyData interface {
	// BoxInfo returns information about a box.
	// The bool result reports whether the box exists.
	BoxInfo(ctx context.Context, boxName string) (BoxData, bool, error)

	// UserInfo returns information about a user.
	// The bool result reports whether the user exists.
	UserInfo(ctx context.Context, userID string) (UserData, bool, error)

	// CreateAuthCookie creates a new authentication cookie.
	CreateAuthCookie(ctx context.Context, userID, domain string) (string, error)
}

// BoxData is the information we need for a box.
type BoxData struct {
	ID                   int      // box ID
	Name                 string   // box name
	Ctrhost              string   // exelet name
	CreatedByUserID      string   // user ID that created the box
	Image                string   // image used to create box
	BoxRoute             BoxRoute // box routing configuration
	SSHServerIdentityKey []byte   // SSH server identity private key
	SSHClientPrivateKey  []byte   // box SSH private key
	SSHPort              int      // box SSH port, 0 if not set
	SSHUser              string   // box SSH user
}

// BoxRoute is a box routing configuration.
// This is exedb.Route, but we don't want to import exedb.
type BoxRoute struct {
	Port  int
	Share string
}

// UserData is the information we need for a user.
type UserData struct {
	UserID string // user ID
	Email  string // user email
}
