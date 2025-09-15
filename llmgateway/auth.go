package llmgateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
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
	BoxName    string    `json:"box_name"`
	CreatedAt  time.Time `json:"created_at"`
	TTLSeconds int       `json:"ttl_seconds"`
}

// NewBearerToken returns a new, unsigned BearerToken.
func NewBearerToken(boxName string, createdAt time.Time, ttlSec int) *bearerTokenClaim {
	ret := &bearerTokenClaim{
		BoxName:    boxName,
		CreatedAt:  createdAt,
		TTLSeconds: ttlSec,
	}

	return ret
}

// DecodeBearerToken decodes the value from a "Authorization: Bearer {value}"
// http header, and returns an unmarshaled bearer claim, its decoded claim bytes,
// and its decoded signature bytes, or an error if any decoding steps fail.
func DecodeBearerToken(token string) (*bearerTokenClaim, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, nil, nil, fmt.Errorf("invalid bearer token")
	}
	claimBytes, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding bearer token claim: %w", err)
	}
	slog.Debug("DecodeBearerToken", "claimBytes", string(claimBytes))

	ret := &bearerTokenClaim{}
	if err := json.Unmarshal(claimBytes, ret); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshaling bearer token: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decoding bearer token signature: %w", err)
	}
	return ret, claimBytes, sigBytes, nil
}

func (b *bearerTokenClaim) Sign(signer ssh.Signer) (*ssh.Signature, error) {
	claimBytes, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("marshaling json: %w", err)
	}
	sig, err := signer.Sign(rand.Reader, claimBytes)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return sig, nil
}

// Encode returns the base64-encoded token suitable for an "Authorization: Bearer {value}"
// http header.  Returns an error if marshaling or signing fail.
func (b *bearerTokenClaim) Encode(signer ssh.Signer) (string, error) {
	claimBytes, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("marshaling claim: %w", err)
	}

	sig, err := b.Sign(signer)
	if err != nil {
		return "", fmt.Errorf("signing claim: %w", err)
	}

	claimStr := base64.StdEncoding.EncodeToString(claimBytes)
	sigBytes := ssh.Marshal(sig)
	sigStr := base64.StdEncoding.EncodeToString(sigBytes)

	return claimStr + "." + sigStr, nil
}

func (m *llmGateway) boxKeyAuth(ctx context.Context, r *http.Request) (string, error) {
	bearerTokenString := r.Header.Get("Authorization")
	if bearerTokenString == "" {
		return "", fmt.Errorf("no authorization header provided")
	}

	bearerTokenString = strings.TrimPrefix(bearerTokenString, "Bearer ")

	tok, claimBytes, sigBytes, err := DecodeBearerToken(bearerTokenString)
	if err != nil {
		return "", fmt.Errorf("boxKeyAuth: %w", err)
	}

	// Get the SSH public key for this box from a trusted authority
	sshPublicKey, err := m.boxKeyAuthority.SSHIdentityKeyForBox(ctx, tok.BoxName)
	if err != nil {
		return "", fmt.Errorf("failed to get SSH key for box %s: %w", tok.BoxName, err)
	}

	sig := &ssh.Signature{}
	if err := ssh.Unmarshal(sigBytes, sig); err != nil {
		return "", fmt.Errorf("parsing ssh signature: %w", err)
	}

	if err := sshPublicKey.Verify(claimBytes, sig); err != nil {
		return "", fmt.Errorf("verifying signature: %w", err)
	}

	expiry := tok.CreatedAt.Add(time.Second * time.Duration(tok.TTLSeconds))

	if m.now().After(expiry) {
		return "", fmt.Errorf("token expired")
	}

	return tok.BoxName, nil
}
