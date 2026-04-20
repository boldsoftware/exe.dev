package exeweb

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

func TestValidateVMTokenLockedOutUser(t *testing.T) {
	t.Parallel()

	testEnv := stage.Test()
	ctx := t.Context()

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	userID := "usr-randomstring"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")

	boxName := "testvm"
	namespace := "v0@" + boxName + "." + testEnv.BoxHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, namespace)
	token := "exe0." + payloadB64 + "." + sigBlob

	mock := &mockProxyData{
		sshKeysByFingerprint: map[string]mockSSHKey{
			fingerprint: {
				userID:      userID,
				publicKey:   pubKeyStr,
				fingerprint: fingerprint,
			},
		},
	}

	ps := &ProxyServer{
		Data:            mock,
		Lg:              tslog.Slogger(t),
		Env:             &testEnv,
		ProxyHTTPSPort:  443,
		CookieUsesCache: new(CookieUsesCache),
	}

	// Token should work before lockout.
	t.Run("before_lockout", func(t *testing.T) {
		result := ps.ValidateVMToken(ctx, token, boxName)
		if result == nil {
			t.Fatal("expected valid result before lockout, got nil")
		}
		if result.UserID != userID {
			t.Errorf("expected userID %q, got %q", userID, result.UserID)
		}
	})

	// Lock out the user.
	mock.lockedOut = map[string]bool{userID: true}

	// Token should be rejected after lockout.
	t.Run("after_lockout", func(t *testing.T) {
		result := ps.ValidateVMToken(ctx, token, boxName)
		if result != nil {
			t.Fatalf("expected nil result after lockout, got userID=%q", result.UserID)
		}
	})
}

// createSigBlob creates a base64url-encoded SSHSIG blob from a signer and message.
func createSigBlob(t *testing.T, signer ssh.Signer, message []byte, namespace string) string {
	t.Helper()

	sig, err := sshsig.Sign(bytes.NewReader(message), signer, sshsig.HashSHA512, namespace)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(sig.Marshal())
}
