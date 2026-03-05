package execore

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sshkey"
)

// exe0ToExe1Flags returns the FlagSet for the exe0-to-exe1 command.
func exe0ToExe1Flags() *flag.FlagSet {
	fs := flag.NewFlagSet("exe0-to-exe1", flag.ContinueOnError)
	fs.String("vm", "", "scope token to a specific VM")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// handleExe0ToExe1Command trades an exe0 token for a shorter exe1 token.
func (ss *SSHServer) handleExe0ToExe1Command(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: exe0-to-exe1 [--vm=VMNAME] <exe0-token>")
	}
	exe0Token := cc.Args[0]

	vmFlag := cc.FlagSet.Lookup("vm").Value.String()

	// Determine namespace
	namespace := "v0@" + ss.server.env.WebHost
	if vmFlag != "" {
		vmName := ss.normalizeBoxName(vmFlag)
		// Validate VM exists and is accessible by the current user
		_, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
		if err != nil {
			return cc.Errorf("VM %q not found or access denied", vmName)
		}
		namespace = "v0@" + vmName + "." + ss.server.env.BoxHost
	}

	// Parse and validate the exe0 token format
	parsed, err := sshkey.ParseToken(exe0Token)
	if err != nil {
		return cc.Errorf("invalid token: %v", err)
	}

	// Look up the SSH key by fingerprint and verify ownership.
	keyOwnerID, publicKey, err := ss.server.getSSHKeyByFingerprint(ctx, parsed.Fingerprint)
	if err != nil {
		return cc.Errorf("invalid token")
	}
	if keyOwnerID != cc.User.ID {
		return cc.Errorf("invalid token")
	}

	// Parse the public key
	matchedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return cc.Errorf("invalid token")
	}

	// Verify the signature against the namespace
	if err := parsed.Verify(matchedKey, namespace); err != nil {
		return cc.Errorf("invalid token")
	}

	// Check trade-time claims (exp only; nbf is intentionally skipped).
	if err := parsed.ValidateClaimsForTrade(); err != nil {
		return cc.Errorf("token has expired")
	}

	// Return existing exe1 token if one already exists for this exe0.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetExe1TokenByExe0, exedb.GetExe1TokenByExe0Params{
		Exe0:      exe0Token,
		ExpiresAt: time.Now().Truncate(time.Second),
	})
	if err == nil {
		return writeExe1Token(cc, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to look up existing exe1 token: %v", err)
	}

	// Compute expires_at from the exe0's exp claim, or use far-future default.
	expiresAt := time.Date(3333, 3, 3, 0, 0, 0, 0, time.UTC)
	if exp, ok := parsed.PayloadJSON["exp"].(float64); ok {
		expiresAt = time.Unix(int64(exp), 0)
	}

	// Generate exe1 token
	exe1Token := sshkey.Exe1TokenPrefix + crand.Text()

	// Insert into DB
	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertExe1Token, exedb.InsertExe1TokenParams{
		Exe1:      exe1Token,
		Exe0:      exe0Token,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return fmt.Errorf("failed to create exe1 token: %v", err)
	}

	return writeExe1Token(cc, exe1Token)
}

func writeExe1Token(cc *exemenu.CommandContext, token string) error {
	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{
			"token": token,
		})
		return nil
	}
	cc.Writeln(token)
	return nil
}
