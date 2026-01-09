// Package pow implements a stateless proof-of-work challenge system.
//
// The server generates HMAC-signed challenge tokens that include difficulty
// and expiry. Clients must find a nonce such that SHA256(token || nonce)
// has a specified number of leading zero bits.
package pow

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidToken    = errors.New("invalid challenge token")
	ErrTokenExpired    = errors.New("challenge token expired")
	ErrInvalidProof    = errors.New("proof of work invalid")
	ErrMalformedToken  = errors.New("malformed token format")
)

// Challenge represents the payload inside a signed token.
type Challenge struct {
	Seed       string `json:"seed"`       // Random seed (base64)
	Difficulty int    `json:"difficulty"` // Required leading zero bits
	Exp        int64  `json:"exp"`        // Unix timestamp expiry
}

// Challenger generates and verifies proof-of-work challenges.
type Challenger struct {
	secret     []byte
	difficulty int
	ttl        time.Duration
}

// NewChallenger creates a new Challenger with the given HMAC secret and difficulty.
// Difficulty is the number of leading zero bits required (e.g., 16 = ~65k hashes on average).
func NewChallenger(secret []byte, difficulty int, ttl time.Duration) *Challenger {
	return &Challenger{
		secret:     secret,
		difficulty: difficulty,
		ttl:        ttl,
	}
}

// NewToken generates a new signed challenge token.
func (c *Challenger) NewToken() (string, error) {
	// Generate random seed
	seedBytes := make([]byte, 16)
	if _, err := rand.Read(seedBytes); err != nil {
		return "", err
	}

	challenge := Challenge{
		Seed:       base64.RawURLEncoding.EncodeToString(seedBytes),
		Difficulty: c.difficulty,
		Exp:        time.Now().Add(c.ttl).Unix(),
	}

	payload, err := json.Marshal(challenge)
	if err != nil {
		return "", err
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := c.sign(payloadB64)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return payloadB64 + "." + sigB64, nil
}

// Verify checks that the token is valid and the nonce produces required leading zeros.
func (c *Challenger) Verify(token string, nonce uint64) error {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ErrMalformedToken
	}
	payloadB64, sigB64 := parts[0], parts[1]

	// Verify HMAC signature
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return ErrMalformedToken
	}
	expectedSig := c.sign(payloadB64)
	if !hmac.Equal(sig, expectedSig) {
		return ErrInvalidToken
	}

	// Decode and parse challenge
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return ErrMalformedToken
	}
	var challenge Challenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		return ErrMalformedToken
	}

	// Check expiry
	if time.Now().Unix() > challenge.Exp {
		return ErrTokenExpired
	}

	// Verify proof of work
	if !verifyPOW(token, nonce, challenge.Difficulty) {
		return ErrInvalidProof
	}

	return nil
}

// GetDifficulty returns the configured difficulty.
func (c *Challenger) GetDifficulty() int {
	return c.difficulty
}

func (c *Challenger) sign(data string) []byte {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// verifyPOW checks if SHA256(token || nonce) has the required leading zero bits.
func verifyPOW(token string, nonce uint64, difficulty int) bool {
	hash := computeHash(token, nonce)
	return hasLeadingZeros(hash, difficulty)
}

// computeHash returns SHA256(token || nonce) where nonce is little-endian 8 bytes.
func computeHash(token string, nonce uint64) []byte {
	h := sha256.New()
	h.Write([]byte(token))
	nonceBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(nonceBytes, nonce)
	h.Write(nonceBytes)
	return h.Sum(nil)
}

// hasLeadingZeros checks if hash has at least n leading zero bits.
func hasLeadingZeros(hash []byte, n int) bool {
	fullBytes := n / 8
	remainingBits := n % 8

	// Check full zero bytes
	for i := 0; i < fullBytes; i++ {
		if hash[i] != 0 {
			return false
		}
	}

	// Check remaining bits in the next byte
	if remainingBits > 0 && fullBytes < len(hash) {
		mask := byte(0xFF << (8 - remainingBits))
		if hash[fullBytes]&mask != 0 {
			return false
		}
	}

	return true
}

// Solve finds a nonce for the given token (for testing purposes).
// This is intentionally slow - it's meant to run on the client.
func Solve(token string, difficulty int) uint64 {
	for nonce := uint64(0); ; nonce++ {
		if verifyPOW(token, nonce, difficulty) {
			return nonce
		}
	}
}
