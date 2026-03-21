// Package apns implements an Apple Push Notification service client.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

const (
	BundleID           = "dev.exe.exe-dev"
	tokenRefreshPeriod = 50 * time.Minute
	productionURL      = "https://api.push.apple.com"
	sandboxURL         = "https://api.sandbox.push.apple.com"
)

// ErrTokenInvalid is returned when APNs indicates the device token
// is no longer valid (HTTP 410 Gone or BadDeviceToken).
var ErrTokenInvalid = errors.New("APNs device token is no longer valid")

// Client sends push notifications via Apple Push Notification service.
type Client struct {
	keyID   string
	teamID  string
	privKey *ecdsa.PrivateKey
	baseURL string
	client  *http.Client

	tokenMu    sync.Mutex
	jwtToken   string
	tokenEpoch time.Time
}

// NewClient creates a new APNs client.
// keyPEM is the contents of the .p8 file (including BEGIN/END headers).
func NewClient(keyID, teamID, keyPEM string, sandbox bool) (*Client, error) {
	if keyID == "" {
		return nil, errors.New("APNs key ID is required")
	}
	if teamID == "" {
		return nil, errors.New("APNs team ID is required")
	}
	if keyPEM == "" {
		return nil, errors.New("APNs private key is required")
	}

	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("failed to decode APNs private key PEM")
	}

	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse APNs private key: %w", err)
	}

	ecKey, ok := parsedKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("APNs private key is not an ECDSA key (got %T)", parsedKey)
	}

	baseURL := productionURL
	if sandbox {
		baseURL = sandboxURL
	}

	return &Client{
		keyID:   keyID,
		teamID:  teamID,
		privKey: ecKey,
		baseURL: baseURL,
		client:  &http.Client{Transport: &http2.Transport{}},
	}, nil
}

type payload struct {
	APS  aps               `json:"aps"`
	Data map[string]string `json:"data,omitempty"`
}

type aps struct {
	Alert alert  `json:"alert"`
	Sound string `json:"sound"`
}

type alert struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

type errorResponse struct {
	Reason string `json:"reason"`
}

// Send sends a push notification to the given device token.
// Returns [ErrTokenInvalid] if APNs reports the token is expired or invalid.
func (c *Client) Send(ctx context.Context, deviceToken, title, body string, data map[string]string) error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("APNs JWT: %w", err)
	}

	p := payload{
		APS: aps{
			Alert: alert{Title: title, Body: body},
			Sound: "default",
		},
		Data: data,
	}

	payloadBytes, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("APNs payload marshal: %w", err)
	}

	url := fmt.Sprintf("%s/3/device/%s", c.baseURL, deviceToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("APNs request: %w", err)
	}

	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("apns-topic", BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("APNs send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var apnsErr errorResponse
	json.Unmarshal(respBody, &apnsErr)

	if resp.StatusCode == http.StatusGone {
		return ErrTokenInvalid
	}
	if resp.StatusCode == http.StatusBadRequest && apnsErr.Reason == "BadDeviceToken" {
		return ErrTokenInvalid
	}

	return fmt.Errorf("APNs error: status=%d reason=%s", resp.StatusCode, apnsErr.Reason)
}

func (c *Client) getToken() (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	now := time.Now()
	if c.jwtToken != "" && now.Sub(c.tokenEpoch) < tokenRefreshPeriod {
		return c.jwtToken, nil
	}

	token, err := c.createToken(now)
	if err != nil {
		return "", err
	}
	c.jwtToken = token
	c.tokenEpoch = now
	return token, nil
}

func (c *Client) createToken(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID})
	claims, _ := json.Marshal(map[string]any{"iss": c.teamID, "iat": now.Unix()})

	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)

	h := sha256.New()
	h.Write([]byte(unsigned))
	hash := h.Sum(nil)

	r, s, err := ecdsa.Sign(crand.Reader, c.privKey, hash)
	if err != nil {
		return "", fmt.Errorf("ECDSA sign: %w", err)
	}

	keyBytes := (c.privKey.Curve.Params().BitSize + 7) / 8
	sigBytes := make([]byte, 2*keyBytes)
	rb, sb := r.Bytes(), s.Bytes()
	copy(sigBytes[keyBytes-len(rb):keyBytes], rb)
	copy(sigBytes[2*keyBytes-len(sb):], sb)

	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sigBytes), nil
}

// VerifyES256 verifies an ES256 JWT signature. Exported for testing.
func VerifyES256(token string, pub *ecdsa.PublicKey) bool {
	parts := splitJWT(token)
	if parts == nil {
		return false
	}

	h := sha256.New()
	h.Write([]byte(parts[0] + "." + parts[1]))
	hash := h.Sum(nil)

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}

	keyBytes := len(sigBytes) / 2
	r := new(big.Int).SetBytes(sigBytes[:keyBytes])
	s := new(big.Int).SetBytes(sigBytes[keyBytes:])

	return ecdsa.Verify(pub, hash, r, s)
}

func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := range token {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	if len(parts) != 3 {
		return nil
	}
	return parts
}
