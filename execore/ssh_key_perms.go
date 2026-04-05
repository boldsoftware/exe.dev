package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"exe.dev/exedb"
)

// SSHKeyPerms represents parsed permissions for an SSH key.
// An nil *SSHKeyPerms means unrestricted (the common case).
type SSHKeyPerms struct {
	Cmds []string `json:"cmds,omitempty"` // nil = all commands allowed
	VM   string   `json:"vm,omitempty"`   // "" = not restricted to a VM
	Exp  *int64   `json:"exp,omitempty"`  // nil = no expiry
}

// parseSSHKeyPerms parses the permissions JSON stored on an SSH key.
// Returns nil (unrestricted) for empty strings or "{}".
func parseSSHKeyPerms(permsJSON string) (*SSHKeyPerms, error) {
	if permsJSON == "" || permsJSON == "{}" {
		return nil, nil
	}
	var p SSHKeyPerms
	if err := json.Unmarshal([]byte(permsJSON), &p); err != nil {
		return nil, err
	}
	// If all fields are zero, treat as unrestricted.
	if len(p.Cmds) == 0 && p.VM == "" && p.Exp == nil {
		return nil, nil
	}
	return &p, nil
}

// IsExpired reports whether the key has expired.
func (p *SSHKeyPerms) IsExpired() bool {
	if p == nil || p.Exp == nil {
		return false
	}
	return time.Now().Unix() > *p.Exp
}

// AllowsCommand reports whether the key permits the given resolved command.
// For SSH keys, nil Cmds means "*" (all commands).
func (p *SSHKeyPerms) AllowsCommand(resolvedCmd string) bool {
	if p == nil || p.Cmds == nil {
		return true
	}
	if resolvedCmd == "" {
		return false
	}
	return slices.Contains(p.Cmds, resolvedCmd)
}

// AllowsVM reports whether the key permits access to the given VM.
// An empty VM restriction means all VMs are allowed.
func (p *SSHKeyPerms) AllowsVM(vmName string) bool {
	if p == nil || p.VM == "" {
		return true
	}
	return p.VM == vmName
}

type sshKeyPermsContextKey struct{}

func withSSHKeyPerms(ctx context.Context, perms *SSHKeyPerms) context.Context {
	return context.WithValue(ctx, sshKeyPermsContextKey{}, perms)
}

func getSSHKeyPerms(ctx context.Context) *SSHKeyPerms {
	p, _ := ctx.Value(sshKeyPermsContextKey{}).(*SSHKeyPerms)
	return p
}

// getSSHKeyPermsByPublicKey looks up the SSH key's permissions from the database.
// Returns (nil, nil) for unrestricted keys (no permissions set).
// Returns a non-nil error on DB or parse failures; callers must deny access on error
// to avoid fail-open.
func (s *Server) getSSHKeyPermsByPublicKey(ctx context.Context, publicKey string) (*SSHKeyPerms, error) {
	if publicKey == "" {
		return nil, nil
	}
	permsJSON, err := withRxRes1(s, ctx, (*exedb.Queries).GetSSHKeyPermissionsByPublicKey, publicKey)
	if errors.Is(err, sql.ErrNoRows) {
		// Key not in ssh_keys table (e.g. ephemeral proxy keys used by piper).
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up SSH key permissions: %w", err)
	}
	p, err := parseSSHKeyPerms(permsJSON)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key permissions: %w", err)
	}
	return p, nil
}
