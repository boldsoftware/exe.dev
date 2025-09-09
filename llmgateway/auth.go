package llmgateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type boxKeyBearerToken struct {
	StartTime time.Time     `json:"start_time"`
	Duration  time.Duration `json:"duration"`
	BoxName   string        `json:"box_name"`
	Signature []byte        `json:"signature"`
}

func NewBearerToken(boxName string, startTime time.Time, duration time.Duration, signer ssh.Signer) (*boxKeyBearerToken, error) {
	ret := &boxKeyBearerToken{
		BoxName:   boxName,
		StartTime: startTime,
		Duration:  duration,
	}

	sig, err := signer.Sign(rand.Reader, ret.claim())
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	ret.Signature = sig.Blob
	return ret, nil
}

// This is the value you should validate b.Signature against.
func (b *boxKeyBearerToken) claim() []byte {
	return fmt.Appendf(nil, "%v,%v,%v", b.StartTime.Format(time.RFC822Z), b.Duration.Milliseconds(), b.BoxName)
}

// Encode returns the base64-encoded token suitable for Authorization header
func (b *boxKeyBearerToken) Encode() (string, error) {
	jsonBytes, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("marshaling token: %w", err)
	}
	return base64.StdEncoding.EncodeToString(jsonBytes), nil
}

func (m *llmGateway) boxKeyAuth(ctx context.Context, r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("no authorization header provided")
	}

	authHeader = strings.TrimPrefix(authHeader, "Bearer ")
	
	// Decode base64 token
	tokenBytes, err := base64.StdEncoding.DecodeString(authHeader)
	if err != nil {
		return "", fmt.Errorf("decoding base64 bearer token: %w", err)
	}
	
	tok := &boxKeyBearerToken{}
	if err := json.Unmarshal(tokenBytes, tok); err != nil {
		return "", fmt.Errorf("unmarshaling bearer token: %w", err)
	}

	// Get the SSH public key for this box
	sshPublicKeyStr, err := m.boxKeyAuthority.SSHIdentityKeyForBox(ctx, tok.BoxName)
	if err != nil {
		return "", fmt.Errorf("failed to get SSH key for box %s: %w", tok.BoxName, err)
	}

	// Parse the SSH public key
	sshPublicKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(sshPublicKeyStr))
	if err != nil {
		return "", fmt.Errorf("failed to parse SSH public key for box %s: %w", tok.BoxName, err)
	}

	// Use the actual signature format from SSH key type (ssh-ed25519 -> ssh-ed25519)
	expectedFormat := sshPublicKey.Type()
	sig := &ssh.Signature{
		Format: expectedFormat,
		Blob:   tok.Signature,
	}
	if err := sshPublicKey.Verify(tok.claim(), sig); err != nil {
		return "", fmt.Errorf("verifying signature: %w", err)
	}

	if m.now().After(tok.StartTime.Add(tok.Duration)) {
		return "", fmt.Errorf("token expired")
	}

	return tok.BoxName, nil
}
