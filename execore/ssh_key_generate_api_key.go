package execore

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sshkey"
)

func generateAPIKeyFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("generate-api-key", flag.ContinueOnError)
	fs.String("label", "", "label for this token's SSH key")
	fs.String("vm", "", "scope key to a VM (authenticates to its HTTPS endpoints instead of exe.dev commands)")
	fs.String("cmds", "", "comma-separated list of allowed commands (empty = defaults)")
	fs.String("exp", "", "expiry duration (e.g. 30d, 1y) or 'never'")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// parseDuration parses a human-friendly duration like "30d", "1y", "90d".
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "never" {
		return 0, nil
	}

	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	var num int
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil || num <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	switch unit {
	case 'h':
		return time.Duration(num) * time.Hour, nil
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour, nil
	case 'm':
		return time.Duration(num) * 30 * 24 * time.Hour, nil
	case 'y':
		return time.Duration(num) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid duration unit %q (use h, d, w, m, or y)", string(unit))
	}
}

// handleSSHKeyGenerateAPIKeyCmd generates an API token server-side.
// It creates an ed25519 key pair, signs the permissions, stores the
// public key in ssh_keys (private key is discarded), maps exe0→exe1
// in exe1_tokens, and returns the short exe1 token.
// Revocation is just "ssh-key remove <label>".
func (ss *SSHServer) handleSSHKeyGenerateAPIKeyCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	label := cc.FlagSet.Lookup("label").Value.String()
	vmFlag := cc.FlagSet.Lookup("vm").Value.String()
	cmdsFlag := cc.FlagSet.Lookup("cmds").Value.String()
	expFlag := cc.FlagSet.Lookup("exp").Value.String()

	// Determine namespace.
	namespace := "v0@" + ss.server.env.WebHost
	if vmFlag != "" {
		vmName := ss.normalizeBoxName(vmFlag)
		if _, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName); err != nil {
			return cc.Errorf("VM %q not found or access denied", vmName)
		}
		namespace = "v0@" + vmName + "." + ss.server.env.BoxHost
	}

	// Build permissions JSON.
	perms := make(map[string]any)

	if cmdsFlag != "" {
		var cmds []string
		for _, p := range strings.Split(cmdsFlag, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cmds = append(cmds, p)
			}
		}
		if len(cmds) > 0 {
			perms["cmds"] = cmds
		}
	}

	var expiresAt *time.Time
	if expFlag != "" && strings.ToLower(expFlag) != "never" {
		d, err := parseDuration(expFlag)
		if err != nil {
			return cc.Errorf("%v", err)
		}
		if d > 0 {
			t := time.Now().Add(d)
			expiresAt = &t
			perms["exp"] = t.Unix()
		}
	}

	permsJSON, err := json.Marshal(perms)
	if err != nil {
		return fmt.Errorf("marshaling permissions: %w", err)
	}

	// Generate the token (key pair + exe0).
	gt, err := sshkey.GenerateToken(permsJSON, namespace)
	if err != nil {
		return fmt.Errorf("generating token: %w", err)
	}

	// Default label.
	if label == "" {
		label = "api-key"
		if vmFlag != "" {
			label = "api-key-" + ss.normalizeBoxName(vmFlag)
		}
	}
	label = sshkey.SanitizeComment(label)

	// Generate short exe1 token.
	exe1Token := sshkey.Exe1TokenPrefix + crand.Text()
	exe1ExpiresAt := time.Date(3333, 3, 3, 0, 0, 0, 0, time.UTC)
	if expiresAt != nil {
		exe1ExpiresAt = *expiresAt
	}

	// Check for duplicate label before starting the transaction.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByComment, exedb.GetSSHKeysForUserByCommentParams{
		UserID:  cc.User.ID,
		Comment: label,
	})
	if err != nil {
		return fmt.Errorf("checking for duplicate label: %w", err)
	}
	if len(existing) > 0 {
		return cc.Errorf("a key named %q already exists; use a different --label", label)
	}

	// Store public key + exe1 mapping in one transaction.
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		// Insert the SSH public key.
		if err := queries.InsertSSHKey(ctx, exedb.InsertSSHKeyParams{
			UserID:      cc.User.ID,
			PublicKey:   gt.PublicKeyAuth,
			Comment:     label,
			Fingerprint: gt.Fingerprint,
		}); err != nil {
			return fmt.Errorf("inserting SSH key: %w", err)
		}
		// Insert exe1→exe0 mapping.
		if err := queries.InsertExe1Token(ctx, exedb.InsertExe1TokenParams{
			Exe1:      exe1Token,
			Exe0:      gt.Exe0Token,
			ExpiresAt: exe1ExpiresAt,
		}); err != nil {
			return fmt.Errorf("inserting exe1 token: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("creating token: %w", err)
	}

	// Output.
	if cc.WantJSON() {
		result := map[string]any{
			"label":       label,
			"token":       exe1Token,
			"namespace":   namespace,
			"fingerprint": "SHA256:" + gt.Fingerprint,
		}
		if expiresAt != nil {
			result["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		cc.WriteJSON(result)
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32mToken created.\033[0m")
	cc.Writeln("")
	cc.Writeln("\033[1mLabel:\033[0m       %s", label)
	if expiresAt != nil {
		cc.Writeln("\033[1mExpires:\033[0m     %s", expiresAt.Format("Jan 2, 2006"))
	} else {
		cc.Writeln("\033[1mExpires:\033[0m     never")
	}
	if vmFlag != "" {
		cc.Writeln("\033[1mVM:\033[0m          %s", ss.normalizeBoxName(vmFlag))
	}
	if cmdsFlag != "" {
		cc.Writeln("\033[1mCommands:\033[0m    %s", cmdsFlag)
	} else {
		cc.Writeln("\033[1mCommands:\033[0m    (defaults)")
	}
	cc.Writeln("")
	cc.Writeln("\033[1mToken:\033[0m")
	cc.Writeln("  %s", exe1Token)
	cc.Writeln("")
	cc.Writeln("\033[2mThis token will not be shown again. Store it securely.\033[0m")
	cc.Writeln("\033[2mRevoke with: ssh-key remove %s\033[0m", label)
	cc.Writeln("")
	cc.Writeln("\033[1mUsage:\033[0m")
	if vmFlag != "" {
		cc.Writeln("  curl -H \"Authorization: Bearer %s\" https://%s.%s/", exe1Token, ss.normalizeBoxName(vmFlag), ss.server.env.BoxHost)
	} else {
		cc.Writeln("  curl -X POST https://%s/exec -H \"Authorization: Bearer %s\" -d 'whoami'", ss.server.env.WebHost, exe1Token)
	}

	return nil
}
