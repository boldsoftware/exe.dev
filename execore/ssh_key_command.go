package execore

import (
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
					"ssh-key add 'ssh-ed25519 AAAA... user@host'",
					"",
					"To generate a new key locally:",
					"  ssh-keygen -t ed25519 -f ~/.ssh/id_exe",
					"",
					"Then add the public key from your local shell:",
					"  cat ~/.ssh/id_exe.pub | ssh exe.dev ssh-key add",
					"",
					"Or from the exe.dev shell:",
					"  ssh-key add 'ssh-ed25519 AAAA... user@host'",
				},
			},
			{
				Name:              "remove",
				Description:       "Remove an SSH key from your account",
				Usage:             "ssh-key remove <public-key>",
				Handler:           ss.handleSSHKeyRemoveCmd,
				FlagSetFunc:       jsonOnlyFlags("ssh-key-remove"),
				HasPositionalArgs: true,
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
		Comment     *string    `json:"comment,omitempty"`
		AddedAt     *time.Time `json:"added_at,omitempty"`
		LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
		Current     bool       `json:"current"`
	}

	ccPubKey := strings.TrimSpace(cc.PublicKey)
	var sshKeys []sshKeyRow
	for _, dbKey := range dbKeys {
		pubKey := strings.TrimSpace(dbKey.PublicKey)
		if pubKey == "" {
			continue
		}
		isCurrent := pubKey == ccPubKey
		sshKeys = append(sshKeys, sshKeyRow{
			PublicKey:   pubKey,
			Fingerprint: "SHA256:" + dbKey.Fingerprint,
			Comment:     dbKey.Comment,
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
		cc.Writeln("  ssh-keygen -t ed25519 -f ~/.ssh/id_exe")
		cc.Writeln("")
		cc.Writeln("Then add the public key from your local shell:")
		cc.Writeln("  cat ~/.ssh/id_exe.pub | ssh exe.dev ssh-key add")
		return nil
	}

	cc.Writeln("\033[1mSSH Keys:\033[0m")
	for _, key := range sshKeys {
		cc.Write("  %s", key.PublicKey)
		if key.Comment != nil && *key.Comment != "" {
			cc.Write(" \033[2m(%s)\033[0m", *key.Comment)
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
	if !cc.IsInteractive() {
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

	// Prepare comment pointer (nil if empty)
	var commentPtr *string
	if comment != "" {
		commentPtr = &comment
	}

	// Try to insert the key - this will fail silently if the key already exists
	var rowsAffected int64
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		result, err := queries.InsertSSHKeyIfNotExists(ctx, exedb.InsertSSHKeyIfNotExistsParams{
			UserID:      cc.User.ID,
			PublicKey:   canonicalKey,
			Comment:     commentPtr,
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
			result["comment"] = comment
		}
		cc.WriteJSON(result)
		return nil
	}
	cc.Writeln("\033[1;32mAdded SSH key.\033[0m")
	return nil
}

func (ss *SSHServer) handleSSHKeyRemoveCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("please provide the SSH public key to remove")
	}
	publicKey := strings.Join(cc.Args, " ")
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" {
		return cc.Errorf("SSH public key cannot be empty")
	}

	// Canonicalize the public key
	parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return cc.Errorf("invalid SSH public key: %v", err)
	}
	canonicalKey := string(ssh.MarshalAuthorizedKey(parsedKey))

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_, err := queries.DeleteSSHKeyForUser(ctx, exedb.DeleteSSHKeyForUserParams{
			UserID:    cc.User.ID,
			PublicKey: canonicalKey,
		})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("SSH key not found")
	}
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"public_key": strings.TrimSpace(canonicalKey),
			"status":     "deleted",
		})
		return nil
	}
	cc.Writeln("\033[1;32mDeleted SSH key.\033[0m")
	return nil
}
