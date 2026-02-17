package execore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/crypto/ssh"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sshkey"
)

// nextSSHKeyComment increments the user's next_ssh_key_number and returns the
// generated comment (e.g., "key-1", "key-2").
func nextSSHKeyComment(ctx context.Context, queries *exedb.Queries, userID string) (string, error) {
	keyNumber, err := queries.GetAndIncrementNextSSHKeyNumber(ctx, userID)
	if err != nil {
		return "", err
	}
	return sshkey.GeneratedComment(keyNumber), nil
}

// resolveSSHKey resolves a key identifier (name, fingerprint, or public key) to matching SSH keys.
// Returns all matching keys for the given user. An empty identifier never matches anything.
func (ss *SSHServer) resolveSSHKey(ctx context.Context, userID, identifier string) ([]exedb.SSHKey, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, nil
	}

	// First, try to parse as a full public key
	if parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(identifier)); err == nil {
		// Compute fingerprint and look up by that (more efficient than fetching all keys)
		fingerprint := sshkey.FingerprintForKey(parsedKey)
		keys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByFingerprint, exedb.GetSSHKeysForUserByFingerprintParams{
			UserID:      userID,
			Fingerprint: fingerprint,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return keys, nil
	}

	// Try as fingerprint (with or without SHA256: prefix)
	fingerprint := identifier
	fingerprint = strings.TrimPrefix(fingerprint, "SHA256:")
	fingerprint = strings.TrimPrefix(fingerprint, "sha256:")
	// SHA256 fingerprints are 32 bytes base64-encoded without padding = 43 characters
	if len(fingerprint) == 43 && !strings.Contains(fingerprint, " ") {
		keys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByFingerprint, exedb.GetSSHKeysForUserByFingerprintParams{
			UserID:      userID,
			Fingerprint: fingerprint,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(keys) > 0 {
			return keys, nil
		}
	}

	// Try as name (comment)
	keys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByComment, exedb.GetSSHKeysForUserByCommentParams{
		UserID:  userID,
		Comment: identifier,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return keys, nil
}

// resolveSSHKeyByNameOrFingerprint resolves a key identifier to matching SSH keys.
// Only accepts names or SHA256:-prefixed fingerprints (the fingerprint support is
// undocumented, for edge cases like renaming keys with empty names via the web UI).
func (ss *SSHServer) resolveSSHKeyByNameOrFingerprint(ctx context.Context, userID, identifier string) ([]exedb.SSHKey, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, nil
	}

	// Check for SHA256:-prefixed fingerprint (undocumented escape hatch)
	if fingerprint, ok := strings.CutPrefix(identifier, "SHA256:"); ok {
		keys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByFingerprint, exedb.GetSSHKeysForUserByFingerprintParams{
			UserID:      userID,
			Fingerprint: fingerprint,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return keys, nil
	}

	// Look up by name (comment)
	keys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByComment, exedb.GetSSHKeysForUserByCommentParams{
		UserID:  userID,
		Comment: identifier,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return keys, nil
}

// sshKeyCommand returns the command definition for the ssh-key command
func (ss *SSHServer) sshKeyCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "ssh-key",
		Description: "Manage SSH keys for your account",
		Usage:       "ssh-key <subcommand> [args...]",
		Handler:     ss.handleSSHKeyHelp,
		FlagSetFunc: jsonOnlyFlags("ssh-key"),
		Subcommands: []*exemenu.Command{
			{
				Name:              "list",
				Description:       "List all SSH keys associated with your account",
				Usage:             "ssh-key list",
				Handler:           ss.handleSSHKeyListCmd,
				FlagSetFunc:       jsonOnlyFlags("ssh-key-list"),
				HasPositionalArgs: false,
			},
			{
				Name:              "add",
				Description:       "Add a new SSH key to your account",
				Usage:             "ssh-key add <public-key>",
				Handler:           ss.handleSSHKeyAddCmd,
				FlagSetFunc:       jsonOnlyFlags("ssh-key-add"),
				HasPositionalArgs: true,
				Examples: []string{
					"ssh-key add 'ssh-ed25519 AAAA... my-laptop'",
					"",
					"To generate a new key locally:",
					"  ssh-keygen -t ed25519 -C \"mnemonic-for-this-key\" -f ~/.ssh/id_exe",
					"",
					"The -C flag sets a name for the key.",
					"",
					"Then add the public key from your local shell:",
					"  cat ~/.ssh/id_exe.pub | ssh exe.dev ssh-key add",
					"",
					"Or from the exe.dev shell:",
					"  ssh-key add 'ssh-ed25519 AAAA... my-laptop'",
				},
			},
			{
				Name:              "remove",
				Description:       "Remove an SSH key from your account",
				Usage:             "ssh-key remove <name|fingerprint|public-key>",
				Handler:           ss.handleSSHKeyRemoveCmd,
				FlagSetFunc:       jsonOnlyFlags("ssh-key-remove"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeSSHKeyName,
			},
			{
				Name:              "rename",
				Description:       "Rename an SSH key",
				Usage:             "ssh-key rename <old-name> <new-name>",
				Handler:           ss.handleSSHKeyRenameCmd,
				FlagSetFunc:       jsonOnlyFlags("ssh-key-rename"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeSSHKeyName,
			},
		},
	}
}

func (ss *SSHServer) handleSSHKeyHelp(ctx context.Context, cc *exemenu.CommandContext) error {
	cmd := ss.commands.FindCommand([]string{"ssh-key"})
	if cmd != nil {
		cmd.Help(cc)
	}
	return nil
}

func (ss *SSHServer) handleSSHKeyListCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	dbKeys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUser, cc.User.ID)
	if err != nil {
		return err
	}

	type sshKeyRow struct {
		PublicKey   string     `json:"public_key"`
		Fingerprint string     `json:"fingerprint"`
		Name        string     `json:"name"`
		AddedAt     *time.Time `json:"added_at,omitempty"`
		LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
		Current     bool       `json:"current"`
	}

	ccPubKey := strings.TrimSpace(cc.PublicKey)
	sshKeys := []sshKeyRow{}
	for _, dbKey := range dbKeys {
		pubKey := strings.TrimSpace(dbKey.PublicKey)
		if pubKey == "" {
			continue
		}
		isCurrent := pubKey == ccPubKey
		sshKeys = append(sshKeys, sshKeyRow{
			PublicKey:   pubKey,
			Fingerprint: "SHA256:" + dbKey.Fingerprint,
			Name:        dbKey.Comment,
			AddedAt:     dbKey.AddedAt,
			LastUsedAt:  dbKey.LastUsedAt,
			Current:     isCurrent,
		})
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"ssh_keys": sshKeys,
		})
		return nil
	}

	if len(sshKeys) == 0 {
		cc.Writeln("No SSH keys found.")
		cc.Writeln("")
		cc.Writeln("To add a key, generate one locally:")
		cc.Writeln("  ssh-keygen -t ed25519 -C \"mnemonic-for-this-key\" -f ~/.ssh/id_exe")
		cc.Writeln("")
		cc.Writeln("Then add the public key from your local shell:")
		cc.Writeln("  cat ~/.ssh/id_exe.pub | ssh exe.dev ssh-key add")
		return nil
	}

	cc.Writeln("\033[1mSSH Keys:\033[0m")
	for _, key := range sshKeys {
		cc.Write("  %s", key.PublicKey)
		if key.Name != "" {
			cc.Write(" \033[2m(%s)\033[0m", key.Name)
		}
		if key.Current {
			cc.Write(" \033[1;32m← current\033[0m")
		}
		cc.Writeln("")
		// Show timestamps on next line if available
		if key.AddedAt != nil || key.LastUsedAt != nil {
			cc.Write("    ")
			if key.AddedAt != nil {
				cc.Write("\033[2madded %s\033[0m", humanize.Time(*key.AddedAt))
			}
			if key.LastUsedAt != nil {
				if key.AddedAt != nil {
					cc.Write(" · ")
				}
				cc.Write("\033[2mlast used %s\033[0m", humanize.Time(*key.LastUsedAt))
			}
			cc.Writeln("")
		}
	}
	return nil
}

func (ss *SSHServer) handleSSHKeyAddCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// Read public key from args
	args := strings.Join(cc.Args, " ")
	args = strings.TrimSpace(args)

	// Read public key from stdin
	var stdin string
	if !cc.IsInteractive() && cc.SSHSession != nil {
		data, err := io.ReadAll(io.LimitReader(cc.SSHSession, 16*1024))
		if err != nil {
			return cc.Errorf("failed to read from stdin: %v", err)
		}
		stdin = strings.TrimSpace(string(data))
	}

	var publicKey string
	switch {
	case args != "" && stdin != "":
		return cc.Errorf("please provide the SSH public key either as an argument or via stdin, not both")
	case args != "":
		publicKey = args
	case stdin != "":
		publicKey = stdin
	default:
		return cc.Errorf("please provide the SSH public key to add")
	}

	// Detect if user accidentally provided a private key
	if strings.Contains(publicKey, "PRIVATE KEY") {
		return cc.Errorf("this is a private key, please use the public key instead (typically found in a .pub file)")
	}

	// Validate and canonicalize the public key, extract comment
	parsedKey, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return cc.Errorf("invalid SSH public key: %v", err)
	}
	canonicalKey := string(ssh.MarshalAuthorizedKey(parsedKey))

	// Sanitize the comment (empty string if none provided)
	comment = sshkey.SanitizeComment(comment)

	// Try to insert the key - this will fail silently if the key already exists.
	// Always increment the counter; use it for the name if user didn't provide one.
	var rowsAffected int64
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		keyNumber, err := queries.GetAndIncrementNextSSHKeyNumber(ctx, cc.User.ID)
		if err != nil {
			return err
		}
		comment = cmp.Or(comment, sshkey.GeneratedComment(keyNumber)) // also updates JSON output
		result, err := queries.InsertSSHKeyIfNotExists(ctx, exedb.InsertSSHKeyIfNotExistsParams{
			UserID:      cc.User.ID,
			PublicKey:   canonicalKey,
			Comment:     comment,
			Fingerprint: sshkey.FingerprintForKey(parsedKey),
		})
		if err != nil {
			return err
		}
		rowsAffected, _ = result.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}

	// If no rows were affected, the key already exists
	if rowsAffected == 0 {
		// Check if it belongs to this user or another user
		existingUserID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDBySSHKey, canonicalKey)
		if err != nil {
			return cc.Errorf("this SSH key is already in use")
		}
		if existingUserID == cc.User.ID {
			return cc.Errorf("this SSH key is already associated with your account")
		}
		return cc.Errorf("this SSH key is already associated with another account")
	}

	if cc.WantJSON() {
		result := map[string]any{
			"public_key": strings.TrimSpace(canonicalKey),
			"status":     "added",
		}
		if comment != "" {
			result["name"] = comment
		}
		cc.WriteJSON(result)
		return nil
	}
	cc.Writeln("\033[1;32mAdded SSH key.\033[0m")
	return nil
}

func (ss *SSHServer) handleSSHKeyRemoveCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("please specify a key to remove (name, fingerprint, or full public key)")
	}
	identifier := strings.Join(cc.Args, " ")
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return cc.Errorf("please specify a key to remove (name, fingerprint, or full public key)")
	}

	// Resolve the identifier to matching keys
	matchingKeys, err := ss.resolveSSHKey(ctx, cc.User.ID, identifier)
	if err != nil {
		return err
	}

	if len(matchingKeys) == 0 {
		return cc.Errorf("no matching SSH key found for %q", identifier)
	}

	if len(matchingKeys) > 1 {
		// Ambiguous - list the matches with their fingerprints
		var sb strings.Builder
		sb.WriteString("multiple keys match, please specify by fingerprint:\n")
		for _, key := range matchingKeys {
			sb.WriteString("  SHA256:")
			sb.WriteString(key.Fingerprint)
			if key.Comment != "" {
				sb.WriteString(" (")
				sb.WriteString(key.Comment)
				sb.WriteString(")")
			}
			sb.WriteString("\n")
		}
		return cc.Errorf("%s", strings.TrimSpace(sb.String()))
	}

	// Exactly one match - delete it
	keyToDelete := matchingKeys[0]
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteSSHKeyByID(ctx, exedb.DeleteSSHKeyByIDParams{
			ID:     keyToDelete.ID,
			UserID: cc.User.ID,
		})
	})
	if err != nil {
		return err
	}

	proxyChangeDeletedSSHKey(int(keyToDelete.ID), keyToDelete.UserID, keyToDelete.PublicKey, keyToDelete.Fingerprint)

	if cc.WantJSON() {
		result := map[string]any{
			"public_key":  strings.TrimSpace(keyToDelete.PublicKey),
			"fingerprint": "SHA256:" + keyToDelete.Fingerprint,
			"status":      "deleted",
		}
		if keyToDelete.Comment != "" {
			result["name"] = keyToDelete.Comment
		}
		cc.WriteJSON(result)
		return nil
	}
	cc.Writeln("\033[1;32mDeleted SSH key.\033[0m")
	return nil
}

func (ss *SSHServer) handleSSHKeyRenameCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: ssh-key rename <old-name> <new-name>")
	}

	oldName := strings.TrimSpace(cc.Args[0])
	newName := strings.TrimSpace(cc.Args[1])

	if oldName == "" {
		return cc.Errorf("old name cannot be empty")
	}
	if newName == "" {
		return cc.Errorf("new name cannot be empty")
	}

	// Resolve old name to a key (also accepts SHA256:-prefixed fingerprint as undocumented escape hatch)
	matchingKeys, err := ss.resolveSSHKeyByNameOrFingerprint(ctx, cc.User.ID, oldName)
	if err != nil {
		return err
	}

	if len(matchingKeys) == 0 {
		return cc.Errorf("no matching SSH key found for %q", oldName)
	}

	if len(matchingKeys) > 1 {
		// Ambiguous - list the matches with their fingerprints
		var sb strings.Builder
		sb.WriteString("multiple keys match, please specify by fingerprint:\n")
		for _, key := range matchingKeys {
			sb.WriteString("  SHA256:")
			sb.WriteString(key.Fingerprint)
			if key.Comment != "" {
				sb.WriteString(" (")
				sb.WriteString(key.Comment)
				sb.WriteString(")")
			}
			sb.WriteString("\n")
		}
		return cc.Errorf("%s", strings.TrimSpace(sb.String()))
	}

	keyToRename := matchingKeys[0]

	// Sanitize the new name (removes special characters like ; | $ etc.)
	newName = sshkey.SanitizeComment(newName)
	if newName == "" {
		return cc.Errorf("new name is empty (special characters like ; | $ are removed)")
	}

	// Check if new-name already exists for this user
	existingKeys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByComment, exedb.GetSSHKeysForUserByCommentParams{
		UserID:  cc.User.ID,
		Comment: newName,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Ignore if it's the same key we're renaming
	for _, key := range existingKeys {
		if key.ID != keyToRename.ID {
			return cc.Errorf("a key named %q already exists", newName)
		}
	}

	// Perform the rename
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateSSHKeyComment(ctx, exedb.UpdateSSHKeyCommentParams{
			Comment: newName,
			ID:      keyToRename.ID,
			UserID:  cc.User.ID,
		})
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"public_key":  strings.TrimSpace(keyToRename.PublicKey),
			"fingerprint": "SHA256:" + keyToRename.Fingerprint,
			"old_name":    keyToRename.Comment,
			"new_name":    newName,
			"status":      "renamed",
		})
		return nil
	}
	cc.Writeln("\033[1;32mRenamed key from %q to %q.\033[0m", keyToRename.Comment, newName)
	return nil
}

// completeSSHKeyName provides tab completion for SSH key names.
// Only completes the first argument (position 2 = first arg after subcommand).
func (ss *SSHServer) completeSSHKeyName(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if compCtx.Position != 2 {
		return nil
	}
	if ss == nil || ss.server == nil || cc == nil || cc.User == nil || cc.SSHSession == nil {
		return nil
	}

	keys, err := withRxRes1(ss.server, cc.SSHSession.Context(), (*exedb.Queries).GetSSHKeysForUser, cc.User.ID)
	if err != nil {
		return nil
	}

	var completions []string
	prefix := compCtx.CurrentWord
	seen := make(map[string]bool)

	for _, key := range keys {
		if key.Comment != "" && !seen[key.Comment] && strings.HasPrefix(key.Comment, prefix) {
			completions = append(completions, key.Comment)
			seen[key.Comment] = true
		}
	}
	return completions
}
