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
	Tag  string   `json:"tag,omitempty"`  // "" = not restricted to a tag
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
	if len(p.Cmds) == 0 && p.VM == "" && p.Tag == "" && p.Exp == nil {
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

// AllowsBoxByTag reports whether a tag-scoped key permits access to the given box.
// Returns true if the key has no tag restriction, or the box carries the required tag.
func (p *SSHKeyPerms) AllowsBoxByTag(boxTags []string) bool {
	if p == nil || p.Tag == "" {
		return true
	}
	return slices.Contains(boxTags, p.Tag)
}

// RequiredTag returns the tag restriction, or "" if none.
func (p *SSHKeyPerms) RequiredTag() string {
	if p == nil {
		return ""
	}
	return p.Tag
}

type sshKeyPermsContextKey struct{}

func withSSHKeyPerms(ctx context.Context, perms *SSHKeyPerms) context.Context {
	return context.WithValue(ctx, sshKeyPermsContextKey{}, perms)
}

func getSSHKeyPerms(ctx context.Context) *SSHKeyPerms {
	p, _ := ctx.Value(sshKeyPermsContextKey{}).(*SSHKeyPerms)
	return p
}

// sshKeyPermsSessionKey stores the SSH key permissions that were resolved at
// session start. This value never changes for the lifetime of the session and
// serves as a fallback when the key is deleted mid-session (preventing a
// privilege-escalation attack where a restricted key removes itself from the
// DB to shed its restrictions).
type sshKeyPermsSessionKey struct{}

func withSessionSSHKeyPerms(ctx context.Context, perms *SSHKeyPerms) context.Context {
	return context.WithValue(ctx, sshKeyPermsSessionKey{}, perms)
}

func getSessionSSHKeyPerms(ctx context.Context) *SSHKeyPerms {
	p, _ := ctx.Value(sshKeyPermsSessionKey{}).(*SSHKeyPerms)
	return p
}

// enforceTagScope checks whether the current SSH key's tag scope allows
// access to the given box. Returns a user-facing error if denied, nil if OK.
func enforceTagScope(ctx context.Context, box *exedb.Box) error {
	perms := getSSHKeyPerms(ctx)
	if perms == nil || perms.Tag == "" {
		return nil
	}
	if !perms.AllowsBoxByTag(box.GetTags()) {
		return fmt.Errorf("SSH key is restricted to VMs with tag %q", perms.Tag)
	}
	return nil
}

// inheritCallerRestrictions merges the calling key's restrictions into the
// new key's permissions map. The new key must be at least as restricted as
// the caller:
//   - tag: inherited if the new key doesn't set one; error if it sets a different one
//   - vm: inherited if the new key doesn't set one; error if it sets a different one
//   - exp: inherited if the new key has no expiry or a later one
//   - cmds: intersected with the caller's cmds (new key can only narrow)
func inheritCallerRestrictions(callerPerms *SSHKeyPerms, newPerms map[string]any) error {
	if callerPerms == nil {
		return nil
	}

	// Tag restriction.
	if callerPerms.Tag != "" {
		if existing, ok := newPerms["tag"].(string); ok && existing != "" {
			if existing != callerPerms.Tag {
				return fmt.Errorf("calling key is scoped to tag %q; cannot create key with tag %q", callerPerms.Tag, existing)
			}
		} else {
			newPerms["tag"] = callerPerms.Tag
		}
	}

	// VM restriction.
	if callerPerms.VM != "" {
		if existing, ok := newPerms["vm"].(string); ok && existing != "" {
			if existing != callerPerms.VM {
				return fmt.Errorf("calling key is scoped to VM %q; cannot create key with VM %q", callerPerms.VM, existing)
			}
		} else {
			newPerms["vm"] = callerPerms.VM
		}
	}

	// Expiry: new key must not outlive the caller.
	if callerPerms.Exp != nil {
		if newExp, ok := newPerms["exp"]; ok {
			// newExp could be int64 or float64 depending on source.
			var newExpVal int64
			switch v := newExp.(type) {
			case int64:
				newExpVal = v
			case float64:
				newExpVal = int64(v)
			}
			if newExpVal > *callerPerms.Exp {
				newPerms["exp"] = *callerPerms.Exp
			}
		} else {
			newPerms["exp"] = *callerPerms.Exp
		}
	}

	// Command restriction: if the caller has a cmds list, the new key's cmds
	// must be a subset. If the new key specifies no cmds, inherit the caller's.
	if callerPerms.Cmds != nil {
		if newCmdsRaw, ok := newPerms["cmds"]; ok {
			newCmds, ok := newCmdsRaw.([]string)
			if !ok {
				// Might be []any from JSON round-trip.
				if arr, ok2 := newCmdsRaw.([]any); ok2 {
					for _, v := range arr {
						if s, ok3 := v.(string); ok3 {
							newCmds = append(newCmds, s)
						}
					}
				}
			}
			for _, cmd := range newCmds {
				if !slices.Contains(callerPerms.Cmds, cmd) {
					return fmt.Errorf("calling key does not allow command %q; cannot grant it to new key", cmd)
				}
			}
		} else {
			newPerms["cmds"] = callerPerms.Cmds
		}
	}

	return nil
}

// errSSHKeyNotFound is returned by getSSHKeyPermsByPublicKey when the public
// key does not exist in the ssh_keys table. Callers use this to distinguish
// "key was never registered" (e.g. ephemeral proxy keys) from "key was
// deleted mid-session" — the latter must be denied to prevent a privilege-
// escalation attack where a restricted key removes itself from the DB to
// shed its restrictions.
var errSSHKeyNotFound = errors.New("SSH key not found")

// getSSHKeyPermsByPublicKey looks up the SSH key's permissions from the database.
// Returns (nil, nil) for unrestricted keys (no permissions set).
// Returns errSSHKeyNotFound when the key is not in the ssh_keys table.
// Returns a non-nil error on DB or parse failures; callers must deny access on error
// to avoid fail-open.
func (s *Server) getSSHKeyPermsByPublicKey(ctx context.Context, publicKey string) (*SSHKeyPerms, error) {
	if publicKey == "" {
		return nil, nil
	}
	permsJSON, err := withRxRes1(s, ctx, (*exedb.Queries).GetSSHKeyPermissionsByPublicKey, publicKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errSSHKeyNotFound
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
