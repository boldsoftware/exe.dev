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

// bearerTokenClaim holds a claim that a request originated from a client who has access to
// a parituclar box's ssh server identitys private key.  The http header is encoded as a
// {claim}.{signature} string, and would appear in an http header like so:
//
// `Authorization: Bearer <base64(json(claim))>.<base64(signature(json(claim)))>`
//
// The json format of the claim is defined by type bearerTokenClaim's json encoding
// struct tags.
type bearerTokenClaim struct {
	BoxName   string        `json:"box_name"`
	StartTime time.Time     `json:"start_time"`
	Duration  time.Duration `json:"duration"`
}

// NewBearerToken returns a new, unsigned BearerToken.
func NewBearerToken(boxName string, startTime time.Time, duration time.Duration) *bearerTokenClaim {
	ret := &bearerTokenClaim{
		BoxName:   boxName,
		StartTime: startTime,
		Duration:  duration,
	}

	return ret
}

// DecodeBearerToken decodes the value from a "Authorization: Bearer {value}"
// http header, and returns a decoded bearer claim, its decoded signature or
// an error if any decoding steps fail.
func DecodeBearerToken(token string) (*bearerTokenClaim, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("invalid bearer token")
	}
	claimBytes, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding bearer token claim: %w", err)
	}
	ret := &bearerTokenClaim{}
	if err := json.Unmarshal(claimBytes, ret); err != nil {
		return nil, nil, fmt.Errorf("unmarshaling bearer token: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding bearer token signature: %w", err)
	}

	return ret, sigBytes, nil
}

func (b *bearerTokenClaim) Sign(signer ssh.Signer) ([]byte, error) {
	claimBytes, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	sig, err := signer.Sign(rand.Reader, claimBytes)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return sig.Blob, nil
}

// Encode returns the base64-encoded token suitable for an "Authorization: Bearer {value}"
// http header.  Returns an error if marshaling or signing fail.
func (b *bearerTokenClaim) Encode(signer ssh.Signer) (string, error) {
	jsonBytes, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("Encode marshaling claim: %w", err)
	}

	sig, err := b.Sign(signer)
	if err != nil {
		return "", fmt.Errorf("Encode signing claim: %w", err)
	}

	claimStr := base64.StdEncoding.EncodeToString(jsonBytes)
	sigStr := base64.StdEncoding.EncodeToString(sig)

	return claimStr + "." + sigStr, nil
}

func (m *llmGateway) boxKeyAuth(ctx context.Context, r *http.Request) (string, error) {
	bearerTokenString := r.Header.Get("Authorization")
	if bearerTokenString == "" {
		return "", fmt.Errorf("no authorization header provided")
	}

	bearerTokenString = strings.TrimPrefix(bearerTokenString, "Bearer ")

	tok, sigBytes, err := DecodeBearerToken(bearerTokenString)
	if err != nil {
		return "", fmt.Errorf("boxKeyAuth: %w", err)
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
		Blob:   sigBytes,
	}
	claimBytes, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("unmarshling claim bytes: %w", err)
	}
	if err := sshPublicKey.Verify(claimBytes, sig); err != nil {
		return "", fmt.Errorf("verifying signature: %w", err)
	}

	if m.now().After(tok.StartTime.Add(tok.Duration)) {
		return "", fmt.Errorf("token expired")
	}

	return tok.BoxName, nil
}
