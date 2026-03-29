package sshkey

import (
	"bytes"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// GeneratedToken holds all artifacts from server-side token generation.
// The private key is scoped to GenerateToken and discarded after signing.
type GeneratedToken struct {
	PublicKeyAuth string // SSH authorized_key format (for ssh_keys table)
	Fingerprint   string // SHA256 fingerprint (no prefix)
	Exe0Token     string // The complete exe0 token
}

// GenerateToken creates an ed25519 key pair, signs the given permissions JSON,
// and returns a complete exe0 token. The namespace should be "v0@exe.dev" for
// API tokens or "v0@vmname.exe.xyz" for VM-scoped tokens.
func GenerateToken(permissionsJSON []byte, namespace string) (*GeneratedToken, error) {
	// Generate ed25519 key pair
	pubKey, privKey, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key pair: %w", err)
	}

	// Create SSH public key for storage in ssh_keys table
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("creating SSH public key: %w", err)
	}
	authorizedKey := string(ssh.MarshalAuthorizedKey(sshPubKey))

	// Create SSH signer
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("creating signer: %w", err)
	}

	// Sign the permissions
	sig, err := sshsig.Sign(bytes.NewReader(permissionsJSON), signer, sshsig.HashSHA512, namespace)
	if err != nil {
		return nil, fmt.Errorf("signing permissions: %w", err)
	}

	// Assemble exe0 token
	payloadB64 := base64.RawURLEncoding.EncodeToString(permissionsJSON)
	sigBlobB64 := base64.RawURLEncoding.EncodeToString(sig.Marshal())
	exe0Token := TokenPrefix + payloadB64 + "." + sigBlobB64

	fingerprint := FingerprintForKey(sshPubKey)

	return &GeneratedToken{
		PublicKeyAuth: authorizedKey,
		Fingerprint:   fingerprint,
		Exe0Token:     exe0Token,
	}, nil
}
