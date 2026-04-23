package execore

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sshkey"
)

// handleAddHTTPProxyWithPeer creates an http-proxy integration using --peer auth.
// It extracts the target VM name from the target URL, generates an API key scoped
// to that VM, stores the associated SSH key linked to the integration, and
// configures the proxy to forward with that credential.
func (ss *SSHServer) handleAddHTTPProxyWithPeer(ctx context.Context, cc *exemenu.CommandContext, name, target, attachments string) error {
	// Extract the VM name from the target URL hostname.
	u, err := url.Parse(target)
	if err != nil {
		return cc.Errorf("invalid target URL: %v", err)
	}
	peerVM := domz.Label(domz.StripPort(u.Host), ss.server.env.BoxHost)
	if peerVM == "" {
		return cc.Errorf("target URL must be a VM on %s (e.g. https://myvm.%s)", ss.server.env.BoxHost, ss.server.env.BoxHost)
	}

	// Verify the user has access to the target VM.
	if _, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, peerVM); err != nil {
		return cc.Errorf("VM %q not found or access denied", peerVM)
	}

	// Generate an API key scoped to the target VM.
	namespace := "v0@" + peerVM + "." + ss.server.env.BoxHost

	permsJSON, err := json.Marshal(map[string]any{})
	if err != nil {
		return fmt.Errorf("marshaling permissions: %w", err)
	}

	gt, err := sshkey.GenerateToken(permsJSON, namespace)
	if err != nil {
		return fmt.Errorf("generating token: %w", err)
	}

	exe1Token := sshkey.Exe1TokenPrefix + crand.Text()
	exe1ExpiresAt := time.Date(3333, 3, 3, 0, 0, 0, 0, time.UTC)

	label := sshkey.SanitizeComment("peer-" + name)

	// Check for duplicate label.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUserByComment, exedb.GetSSHKeysForUserByCommentParams{
		UserID:  cc.User.ID,
		Comment: label,
	})
	if err != nil {
		return fmt.Errorf("checking for duplicate label: %w", err)
	}
	if len(existing) > 0 {
		return cc.Errorf("a key named %q already exists", label)
	}

	header := "Authorization:Bearer " + exe1Token
	cfg := httpProxyConfig{Target: target, Header: header, PeerVM: peerVM}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	if attachments == "" {
		attachments = "[]"
	}

	// Compute hint: first 4 characters of the exe1 token body.
	apiKeyHint := exe1Token
	if body, ok := strings.CutPrefix(exe1Token, sshkey.Exe1TokenPrefix); ok {
		if len(body) > 4 {
			apiKeyHint = body[:4]
		} else {
			apiKeyHint = body
		}
	}

	integrationID, err := generateID("int")
	if err != nil {
		return err
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.InsertIntegration(ctx, exedb.InsertIntegrationParams{
			IntegrationID: integrationID,
			OwnerUserID:   cc.User.ID,
			Type:          "http-proxy",
			Config:        string(cfgJSON),
			Name:          name,
			Attachments:   attachments,
			Comment:       commentFromFlags(cc),
		}); err != nil {
			return fmt.Errorf("inserting integration: %w", err)
		}
		if err := queries.InsertSSHKeyWithIntegration(ctx, exedb.InsertSSHKeyWithIntegrationParams{
			UserID:        cc.User.ID,
			PublicKey:     gt.PublicKeyAuth,
			Comment:       label,
			Fingerprint:   gt.Fingerprint,
			IntegrationID: &integrationID,
			ApiKeyHint:    &apiKeyHint,
		}); err != nil {
			return fmt.Errorf("inserting SSH key: %w", err)
		}
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
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	cc.Writeln("Added integration %s (peer auth \u2192 %s)", name, peerVM)
	ss.printIntegrationUsage(cc, "http-proxy", name, attachments, nil, nil)
	return nil
}
