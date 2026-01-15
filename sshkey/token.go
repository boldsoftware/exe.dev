package sshkey

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	jsonv2 "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/hiddeco/sshsig"

	"golang.org/x/crypto/ssh"
)

const (
	TokenPrefix      = "exe0."    // Token version prefix
	MaxTokenSize     = 8 * 1024   // 8KB token limit
	MinUnixTimestamp = 946684800  // Jan 1, 2000
	MaxUnixTimestamp = 4102444800 // Jan 1, 2100
)

// StrictInt is an integer type that rejects non-plain-integer JSON representations.
// It rejects: decimals (.0), exponents (1e10), leading zeros, hex/octal, NaN/Infinity.
type StrictInt int64

func (s *StrictInt) UnmarshalJSONV2(dec *jsontext.Decoder, opts jsonv2.Options) error {
	// Read the raw token
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}

	// Must be a number
	if tok.Kind() != '0' {
		return errors.New("expected integer, got " + tok.Kind().String())
	}

	// Get the raw string representation to validate format
	raw := tok.String()

	// Reject empty
	if raw == "" {
		return errors.New("empty number")
	}

	// Reject decimals and exponents
	for _, c := range raw {
		if c == '.' || c == 'e' || c == 'E' {
			return errors.New("integer must not contain decimal or exponent: " + raw)
		}
	}

	// Reject leading zeros (except "0" itself and negative zero "-0")
	if len(raw) > 1 {
		start := 0
		if raw[0] == '-' {
			start = 1
		}
		if start < len(raw) && raw[start] == '0' && start+1 < len(raw) {
			return errors.New("integer must not have leading zeros: " + raw)
		}
	}

	// Parse as int64 using strconv for proper overflow detection
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}

	*s = StrictInt(val)
	return nil
}

// tokenPayload is the struct used for strict JSON parsing with json/v2.
type tokenPayload struct {
	Exp  *StrictInt      `json:"exp,omitempty"`
	Nbf  *StrictInt      `json:"nbf,omitempty"`
	Ctx  json.RawMessage `json:"ctx,omitempty"`
	Cmds []string        `json:"cmds,omitempty"`
}

// validateCtxNoDuplicates parses ctx bytes with json/v2 to check for duplicate keys.
// The result is discarded; this is validation only.
func validateCtxNoDuplicates(ctx []byte) error {
	var v any
	if err := jsonv2.Unmarshal(ctx, &v); err != nil {
		return errors.New("invalid ctx JSON")
	}
	return nil
}

// ParsedToken contains the parsed components of an SSH-signed token.
// The token format is: exe0.<payload>.<sigblob>
type ParsedToken struct {
	Fingerprint string            // Key fingerprint (without SHA256: prefix), derived from sig blob's embedded public key
	Payload     []byte            // Raw JSON payload bytes
	PayloadJSON map[string]any    // Parsed JSON payload
	CtxRaw      json.RawMessage   // Raw bytes of the "ctx" field (nil if not present)
	Cmds        []string          // Allowed commands (nil means use default)
	sig         *sshsig.Signature // Parsed SSHSIG signature (used by Verify)
}

// ParseToken parses an SSH-signed token string into its components.
// Does NOT verify the signature - just parses and validates format.
// Returns an error if the format is invalid or payload is not valid JSON.
//
// Validation is strict:
//   - Token size is limited to MaxTokenSize
//   - Payload must not contain newlines
//   - Unknown top-level keys are rejected (only exp, nbf, ctx, cmds allowed)
//   - Duplicate keys are rejected (at top level and within ctx)
//   - exp/nbf must be plain integers (no decimals, no exponents)
//   - exp/nbf must be in range [MinUnixTimestamp, MaxUnixTimestamp]
//   - cmds must be an array of non-empty strings (if present)
func ParseToken(token string) (*ParsedToken, error) {
	// Size check on raw token
	if len(token) > MaxTokenSize {
		return nil, errors.New("invalid token: exceeds maximum size")
	}

	// Require and strip the version prefix
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, errors.New("invalid token format: missing exe0. prefix")
	}
	token = token[len(TokenPrefix):]

	// Split token: payload.sigblob
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid token format: expected exe0.<payload>.<sigblob>")
	}
	payloadB64, sigBlobB64 := parts[0], parts[1]

	// Decode payload (base64url, no padding)
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, errors.New("invalid token: payload is not valid base64url")
	}

	// Reject payload with leading/trailing whitespace
	if len(payload) != len(bytes.TrimSpace(payload)) {
		return nil, errors.New("invalid token: payload must not have leading or trailing whitespace")
	}

	// Reject newlines in payload
	if bytes.ContainsAny(payload, "\n\r") {
		return nil, errors.New("invalid token: payload must not contain newlines")
	}

	// Reject null bytes in payload
	if bytes.ContainsRune(payload, 0) {
		return nil, errors.New("invalid token: payload must not contain null bytes")
	}

	// Payload must be a JSON object.
	if len(payload) == 0 || payload[0] != '{' {
		return nil, errors.New("invalid token: payload must be a JSON object")
	}

	// Single json/v2 parse into tokenPayload with strict options:
	// - RejectUnknownMembers rejects unknown top-level keys
	// - json/v2 rejects duplicate keys by default
	// - StrictInt validates exp/nbf format
	var tp tokenPayload
	if err := jsonv2.Unmarshal(payload, &tp, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, errors.New("invalid token: invalid JSON")
	}

	// Range-check exp if present
	if tp.Exp != nil {
		v := int64(*tp.Exp)
		if v < MinUnixTimestamp || v > MaxUnixTimestamp {
			return nil, errors.New("invalid token: exp out of valid range")
		}
	}

	// Range-check nbf if present
	if tp.Nbf != nil {
		v := int64(*tp.Nbf)
		if v < MinUnixTimestamp || v > MaxUnixTimestamp {
			return nil, errors.New("invalid token: nbf out of valid range")
		}
	}

	// If ctx present, validate it for duplicate keys
	if len(tp.Ctx) > 0 {
		if err := validateCtxNoDuplicates(tp.Ctx); err != nil {
			return nil, errors.New("invalid token: invalid JSON")
		}
	}

	// Validate cmds elements if present
	for _, cmd := range tp.Cmds {
		if cmd == "" {
			return nil, errors.New("invalid token: cmds elements must be non-empty strings")
		}
	}

	// Build PayloadJSON as map[string]any, matching standard encoding/json
	// unmarshalling conventions (numbers as float64) so callers can type-assert
	// consistently regardless of whether they parsed with json/v2 or encoding/json.
	payloadJSON := make(map[string]any)
	if tp.Exp != nil {
		payloadJSON["exp"] = float64(*tp.Exp)
	}
	if tp.Nbf != nil {
		payloadJSON["nbf"] = float64(*tp.Nbf)
	}
	if len(tp.Ctx) > 0 {
		var ctxVal any
		_ = json.Unmarshal(tp.Ctx, &ctxVal) // already validated
		payloadJSON["ctx"] = ctxVal
	}
	if tp.Cmds != nil {
		cmdsAny := make([]any, len(tp.Cmds))
		for i, c := range tp.Cmds {
			cmdsAny[i] = c
		}
		payloadJSON["cmds"] = cmdsAny
	}

	// Decode sig blob (base64url, no padding)
	sigBlob, err := base64.RawURLEncoding.DecodeString(sigBlobB64)
	if err != nil {
		return nil, errors.New("invalid token: signature is not valid base64url")
	}

	// Parse the SSHSIG blob to extract the public key and derive fingerprint.
	sig, err := sshsig.ParseSignature(sigBlob)
	if err != nil {
		return nil, errors.New("invalid token: malformed signature")
	}

	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sig.PublicKey), "SHA256:")

	return &ParsedToken{
		Fingerprint: fingerprint,
		Payload:     payload,
		PayloadJSON: payloadJSON,
		CtxRaw:      tp.Ctx,
		Cmds:        tp.Cmds,
		sig:         sig,
	}, nil
}

// ValidateClaims checks the exp and nbf claims in the token payload.
// Returns an error if the token is expired or not yet valid.
func (t *ParsedToken) ValidateClaims() error {
	return t.ValidateClaimsAt(time.Now())
}

// ValidateClaimsAt checks the exp and nbf claims against a specific time.
// This is useful for testing.
func (t *ParsedToken) ValidateClaimsAt(now time.Time) error {
	unixNow := now.Unix()
	if exp, ok := t.PayloadJSON["exp"].(float64); ok {
		if int64(exp) < unixNow {
			return errors.New("invalid token: token has expired")
		}
	}
	if nbf, ok := t.PayloadJSON["nbf"].(float64); ok {
		if int64(nbf) > unixNow {
			return errors.New("invalid token: token is not yet valid")
		}
	}
	return nil
}

// Verify verifies the signature against the given public key and namespace.
func (t *ParsedToken) Verify(pubKey ssh.PublicKey, namespace string) error {
	return sshsig.Verify(bytes.NewReader(t.Payload), t.sig, pubKey, t.sig.HashAlgorithm, namespace)
}
