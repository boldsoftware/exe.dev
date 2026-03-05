package sshkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	jsonv2 "github.com/go-json-experiment/json"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// createTestToken creates a token for testing.
func createTestToken(t *testing.T, signer ssh.Signer, payload map[string]any, namespace string) string {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)

	// Create SSH signature
	sig, err := sshsig.Sign(bytes.NewReader(payloadBytes), signer, sshsig.HashSHA512, namespace)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	sigBlobB64 := base64.RawURLEncoding.EncodeToString(sig.Marshal())

	return "exe0." + payloadB64 + "." + sigBlobB64
}

func TestParseToken(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	t.Run("valid_token", func(t *testing.T) {
		// Use only allowed keys: exp, nbf, ctx
		token := createTestToken(t, signer, map[string]any{"ctx": map[string]any{"foo": "bar"}}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}

		if parsed.Fingerprint != FingerprintForKey(signer.PublicKey()) {
			t.Errorf("wrong fingerprint")
		}
		ctx, ok := parsed.PayloadJSON["ctx"].(map[string]any)
		if !ok || ctx["foo"] != "bar" {
			t.Errorf("wrong payload: %v", parsed.PayloadJSON)
		}
	})

	t.Run("missing_prefix", func(t *testing.T) {
		// Token without exe0. prefix should fail
		_, err := ParseToken("payload.sigblob")
		if err == nil {
			t.Error("expected error for token without exe0. prefix")
		}
		if !strings.Contains(err.Error(), "missing exe0. prefix") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("wrong_prefix", func(t *testing.T) {
		_, err := ParseToken("exe2.payload.sigblob")
		if err == nil {
			t.Error("expected error for wrong prefix")
		}
		if !strings.Contains(err.Error(), "missing exe0. prefix") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid_format_no_dots", func(t *testing.T) {
		_, err := ParseToken("nodots")
		if err == nil {
			t.Error("expected error for token without dots")
		}
	})

	t.Run("invalid_format_missing_sigblob", func(t *testing.T) {
		_, err := ParseToken("exe0.payloadonly")
		if err == nil {
			t.Error("expected error for token missing sigblob")
		}
		if !strings.Contains(err.Error(), "expected exe0.<payload>.<sigblob>") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid_base64_payload", func(t *testing.T) {
		_, err := ParseToken("exe0.!!!invalid!!!.sigblob")
		if err == nil {
			t.Error("expected error for invalid base64")
		}
		if !strings.Contains(err.Error(), "base64url") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid_json_payload", func(t *testing.T) {
		notJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
		_, err := ParseToken("exe0." + notJSON + ".sigblob")
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "invalid token") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid_sigblob_base64", func(t *testing.T) {
		payload := base64.RawURLEncoding.EncodeToString([]byte("{}"))
		_, err := ParseToken("exe0." + payload + ".!!!invalid!!!")
		if err == nil {
			t.Error("expected error for invalid sigblob base64")
		}
	})

	t.Run("invalid_sigblob_not_sshsig", func(t *testing.T) {
		payload := base64.RawURLEncoding.EncodeToString([]byte("{}"))
		fakeSig := base64.RawURLEncoding.EncodeToString([]byte("not an sshsig blob"))
		_, err := ParseToken("exe0." + payload + "." + fakeSig)
		if err == nil {
			t.Error("expected error for invalid sigblob content")
		}
		if !strings.Contains(err.Error(), "malformed signature") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestParsedTokenValidateClaims(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	t.Run("no_claims", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.ValidateClaims(); err != nil {
			t.Errorf("ValidateClaims failed for empty claims: %v", err)
		}
	})

	t.Run("valid_exp", func(t *testing.T) {
		futureExp := float64(time.Now().Add(time.Hour).Unix())
		token := createTestToken(t, signer, map[string]any{"exp": futureExp}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.ValidateClaims(); err != nil {
			t.Errorf("ValidateClaims failed for valid exp: %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		pastExp := float64(time.Now().Add(-time.Hour).Unix())
		token := createTestToken(t, signer, map[string]any{"exp": pastExp}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		err := parsed.ValidateClaims()
		if err == nil {
			t.Error("expected error for expired token")
		}
		if !strings.Contains(err.Error(), "expired") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid_nbf", func(t *testing.T) {
		pastNbf := float64(time.Now().Add(-time.Hour).Unix())
		token := createTestToken(t, signer, map[string]any{"nbf": pastNbf}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.ValidateClaims(); err != nil {
			t.Errorf("ValidateClaims failed for valid nbf: %v", err)
		}
	})

	t.Run("not_yet_valid", func(t *testing.T) {
		futureNbf := float64(time.Now().Add(time.Hour).Unix())
		token := createTestToken(t, signer, map[string]any{"nbf": futureNbf}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		err := parsed.ValidateClaims()
		if err == nil {
			t.Error("expected error for not-yet-valid token")
		}
		if !strings.Contains(err.Error(), "not yet valid") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("validate_at_specific_time", func(t *testing.T) {
		// Token valid from unix 1700000000 (Nov 2023) to unix 1800000000 (Jan 2027)
		// These are within the allowed range [MinUnixTimestamp, MaxUnixTimestamp]
		token := createTestToken(t, signer, map[string]any{
			"nbf": float64(1700000000),
			"exp": float64(1800000000),
		}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}

		// Before validity window
		err = parsed.ValidateClaimsAt(time.Unix(1600000000, 0))
		if err == nil || !strings.Contains(err.Error(), "not yet valid") {
			t.Errorf("expected 'not yet valid' error at time 1600000000, got: %v", err)
		}

		// During validity window
		err = parsed.ValidateClaimsAt(time.Unix(1750000000, 0))
		if err != nil {
			t.Errorf("expected valid at time 1750000000, got: %v", err)
		}

		// After validity window
		err = parsed.ValidateClaimsAt(time.Unix(1900000000, 0))
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Errorf("expected 'expired' error at time 1900000000, got: %v", err)
		}
	})
}

func TestStrictIntUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool // just check if error occurred, error messages vary
	}{
		// Valid cases
		{"plain_int", `123`, 123, false},
		{"zero", `0`, 0, false},
		{"negative", `-456`, -456, false},
		{"large_int", `2000000000`, 2000000000, false},

		// Invalid cases - decimals
		{"decimal", `123.0`, 0, true},
		{"decimal_nonzero", `123.5`, 0, true},

		// Invalid cases - exponents
		{"exponent_lower", `1e10`, 0, true},
		{"exponent_upper", `1E10`, 0, true},
		{"exponent_negative", `1e-10`, 0, true},
		{"exponent_positive", `2e+9`, 0, true},

		// Invalid cases - leading zeros (note: JSON parser rejects these before we see them)
		{"leading_zero", `0123`, 0, true},
		{"negative_leading_zero", `-0123`, 0, true},

		// Invalid cases - overflow
		{"overflow_positive", `99999999999999999999`, 0, true},
		{"overflow_negative", `-99999999999999999999`, 0, true},
		{"max_int64_plus_one", `9223372036854775808`, 0, true},

		// Invalid cases - non-numbers
		{"string", `"123"`, 0, true},
		{"bool", `true`, 0, true},
		{"array", `[1]`, 0, true},
		{"object", `{}`, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonStr := `{"val":` + tt.input + `}`
			var wrapper struct {
				Val StrictInt `json:"val"`
			}

			// Use json/v2 for unmarshaling since StrictInt implements UnmarshalJSONV2
			err := jsonv2.Unmarshal([]byte(jsonStr), &wrapper)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if int64(wrapper.Val) != tt.want {
				t.Errorf("got %d, want %d", wrapper.Val, tt.want)
			}
		})
	}

	// Test null separately - with omitempty, null leaves the value unchanged
	t.Run("null_with_omitempty", func(t *testing.T) {
		var wrapper struct {
			Val *StrictInt `json:"val,omitempty"`
		}
		err := jsonv2.Unmarshal([]byte(`{"val":null}`), &wrapper)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if wrapper.Val != nil {
			t.Errorf("expected nil, got %v", wrapper.Val)
		}
	})
}

func TestParseTokenStrictValidation(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	// Helper to create a token from raw JSON payload
	createTokenFromRawJSON := func(t *testing.T, rawJSON string) string {
		t.Helper()
		payload := []byte(rawJSON)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

		sig, err := sshsig.Sign(bytes.NewReader(payload), signer, sshsig.HashSHA512, "v0@exe.dev")
		if err != nil {
			t.Fatalf("failed to sign: %v", err)
		}

		sigBlobB64 := base64.RawURLEncoding.EncodeToString(sig.Marshal())

		return "exe0." + payloadB64 + "." + sigBlobB64
	}

	t.Run("leading_space_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, " {}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for leading space in payload")
		}
		if !strings.Contains(err.Error(), "whitespace") {
			t.Errorf("expected whitespace error, got: %v", err)
		}
	})

	t.Run("trailing_space_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "{} ")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for trailing space in payload")
		}
		if !strings.Contains(err.Error(), "whitespace") {
			t.Errorf("expected whitespace error, got: %v", err)
		}
	})

	t.Run("leading_tab_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "\t{}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for leading tab in payload")
		}
		if !strings.Contains(err.Error(), "whitespace") {
			t.Errorf("expected whitespace error, got: %v", err)
		}
	})

	t.Run("newline_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "{\n\"exp\":2000000000}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for newline in payload")
		}
		if !strings.Contains(err.Error(), "newline") {
			t.Errorf("expected newline error, got: %v", err)
		}
	})

	t.Run("carriage_return_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "{\r\"exp\":2000000000}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for carriage return in payload")
		}
		if !strings.Contains(err.Error(), "newline") {
			t.Errorf("expected newline error, got: %v", err)
		}
	})

	t.Run("duplicate_key_top_level", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":2000000000,"exp":2100000000}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for duplicate key")
		}
		if !strings.Contains(err.Error(), "invalid JSON") {
			t.Errorf("expected invalid JSON error, got: %v", err)
		}
	})

	t.Run("duplicate_key_in_ctx", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"ctx":{"a":1,"a":2}}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for duplicate key in ctx")
		}
		if !strings.Contains(err.Error(), "invalid JSON") {
			t.Errorf("expected invalid JSON error, got: %v", err)
		}
	})

	t.Run("unknown_key", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"foo":"bar"}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for unknown key")
		}
		if !strings.Contains(err.Error(), `unknown field "foo"`) {
			t.Errorf("expected unknown field error, got: %v", err)
		}
	})

	t.Run("exp_decimal", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":2000000000.0}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for decimal exp")
		}
		// json/v2 will report the error in its own format
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("expected invalid syntax error, got: %v", err)
		}
	})

	t.Run("exp_exponent", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":2e9}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for exponent exp")
		}
		// json/v2 will report the error in its own format
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("expected invalid syntax error, got: %v", err)
		}
	})

	t.Run("exp_out_of_range_low", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":100}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for out of range exp")
		}
		if !strings.Contains(err.Error(), "out of valid range") {
			t.Errorf("expected range error, got: %v", err)
		}
	})

	t.Run("exp_out_of_range_high", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":9999999999}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for out of range exp")
		}
		if !strings.Contains(err.Error(), "out of valid range") {
			t.Errorf("expected range error, got: %v", err)
		}
	})

	t.Run("nbf_out_of_range", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"nbf":100}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for out of range nbf")
		}
		if !strings.Contains(err.Error(), "out of valid range") {
			t.Errorf("expected range error, got: %v", err)
		}
	})

	t.Run("exp_at_min_boundary", func(t *testing.T) {
		// Exactly MinUnixTimestamp (946684800) should be accepted.
		token := createTokenFromRawJSON(t, `{"exp":946684800}`)
		_, err := ParseToken(token)
		if err != nil {
			t.Errorf("expected exp at MinUnixTimestamp to be valid, got: %v", err)
		}
	})

	t.Run("exp_below_min_boundary", func(t *testing.T) {
		// One below MinUnixTimestamp should be rejected.
		token := createTokenFromRawJSON(t, `{"exp":946684799}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for exp one below MinUnixTimestamp")
		}
	})

	t.Run("exp_at_max_boundary", func(t *testing.T) {
		// Exactly MaxUnixTimestamp (4102444800) should be accepted.
		token := createTokenFromRawJSON(t, `{"exp":4102444800}`)
		_, err := ParseToken(token)
		if err != nil {
			t.Errorf("expected exp at MaxUnixTimestamp to be valid, got: %v", err)
		}
	})

	t.Run("exp_above_max_boundary", func(t *testing.T) {
		// One above MaxUnixTimestamp should be rejected.
		token := createTokenFromRawJSON(t, `{"exp":4102444801}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for exp one above MaxUnixTimestamp")
		}
	})

	t.Run("exp_negative", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":-1}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for negative exp")
		}
	})

	t.Run("valid_empty", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{}`)
		_, err := ParseToken(token)
		if err != nil {
			t.Errorf("expected valid token, got: %v", err)
		}
	})

	t.Run("valid_with_ctx", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"ctx":{"nested":1}}`)
		parsed, err := ParseToken(token)
		if err != nil {
			t.Errorf("expected valid token, got: %v", err)
		}
		if parsed.CtxRaw == nil {
			t.Error("expected CtxRaw to be set")
		}
	})

	t.Run("valid_with_exp_nbf", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"exp":2000000000,"nbf":1000000000}`)
		_, err := ParseToken(token)
		if err != nil {
			t.Errorf("expected valid token, got: %v", err)
		}
	})

	t.Run("token_too_large", func(t *testing.T) {
		// Create a token larger than MaxTokenSize (8KB)
		largeCtx := strings.Repeat("x", MaxTokenSize+1)
		token := "exe0." + base64.RawURLEncoding.EncodeToString([]byte(`{"ctx":"`+largeCtx+`"}`)) + ".sig"
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for oversized token")
		}
		if !strings.Contains(err.Error(), "exceeds maximum size") {
			t.Errorf("expected size error, got: %v", err)
		}
	})

	t.Run("valid_cmds", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"cmds":["ls","whoami","ssh-key list"]}`)
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("expected valid token, got: %v", err)
		}
		if len(parsed.Cmds) != 3 {
			t.Fatalf("expected 3 cmds, got %d", len(parsed.Cmds))
		}
		if parsed.Cmds[0] != "ls" || parsed.Cmds[1] != "whoami" || parsed.Cmds[2] != "ssh-key list" {
			t.Errorf("unexpected cmds: %v", parsed.Cmds)
		}
		// Check PayloadJSON also has cmds
		cmdsAny, ok := parsed.PayloadJSON["cmds"].([]any)
		if !ok || len(cmdsAny) != 3 {
			t.Errorf("expected cmds in PayloadJSON, got: %v", parsed.PayloadJSON["cmds"])
		}
	})

	t.Run("empty_cmds_array", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"cmds":[]}`)
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("expected valid token, got: %v", err)
		}
		if parsed.Cmds == nil {
			t.Fatal("expected non-nil empty cmds slice")
		}
		if len(parsed.Cmds) != 0 {
			t.Errorf("expected 0 cmds, got %d", len(parsed.Cmds))
		}
	})

	t.Run("omitted_cmds", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{}`)
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("expected valid token, got: %v", err)
		}
		if parsed.Cmds != nil {
			t.Errorf("expected nil cmds when omitted, got: %v", parsed.Cmds)
		}
		if _, ok := parsed.PayloadJSON["cmds"]; ok {
			t.Errorf("expected no cmds in PayloadJSON when omitted")
		}
	})

	t.Run("cmds_empty_string_element", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"cmds":["ls",""]}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for empty string in cmds")
		}
		if !strings.Contains(err.Error(), "non-empty") {
			t.Errorf("expected non-empty error, got: %v", err)
		}
	})

	t.Run("cmds_non_string_element", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"cmds":["ls",123]}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for non-string element in cmds")
		}
	})

	t.Run("cmds_non_array", func(t *testing.T) {
		token := createTokenFromRawJSON(t, `{"cmds":"ls"}`)
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for non-array cmds")
		}
	})
}

func TestParseTokenInvalidJSON(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	createTokenFromRawJSON := func(t *testing.T, rawJSON string) string {
		t.Helper()
		payload := []byte(rawJSON)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sig, err := sshsig.Sign(bytes.NewReader(payload), signer, sshsig.HashSHA512, "v0@exe.dev")
		if err != nil {
			t.Fatalf("failed to sign: %v", err)
		}
		sigBlobB64 := base64.RawURLEncoding.EncodeToString(sig.Marshal())
		return "exe0." + payloadB64 + "." + sigBlobB64
	}

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"unknown_field", `{"admin":true}`, `invalid token: unknown field "admin"`},
		{"exp_wrong_type", `{"exp":"2000000000"}`, "invalid token: invalid JSON"},
		{"exp_decimal", `{"exp":2000000000.5}`, "invalid token: invalid JSON"},
		{"cmds_wrong_type", `{"cmds":[1]}`, "invalid token: invalid JSON"},
		{"cmds_object_element", `{"cmds":[{"cmd":"rm"}]}`, "invalid token: invalid JSON"},
		{"duplicate_ctx_key", `{"ctx":{"a":1,"a":2}}`, "invalid token: invalid JSON"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := createTokenFromRawJSON(t, tc.payload)
			_, err := ParseToken(token)
			if err == nil {
				t.Fatal("expected error")
			}
			if err.Error() != tc.want {
				t.Errorf("got %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateCtxNoDuplicates(t *testing.T) {
	t.Run("valid_ctx", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`{"a":1,"b":2}`))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("nested_valid", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`{"a":{"b":1,"c":2}}`))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate_in_nested", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`{"a":{"b":1,"b":2}}`))
		if err == nil {
			t.Error("expected error for duplicate key in nested object")
		}
	})

	t.Run("duplicate_top_level", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`{"a":1,"a":2}`))
		if err == nil {
			t.Error("expected error for duplicate top-level key")
		}
	})

	t.Run("array_is_valid", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`[1,2,3]`))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("string_is_valid", func(t *testing.T) {
		err := validateCtxNoDuplicates([]byte(`"hello"`))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestParsedTokenValidateClaimsBoundary(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	t.Run("exp_exactly_now_is_valid", func(t *testing.T) {
		// exp < now means expired (strict less-than), so exp == now should be valid.
		now := time.Now()
		token := createTestToken(t, signer, map[string]any{"exp": float64(now.Unix())}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}
		if err := parsed.ValidateClaimsAt(now); err != nil {
			t.Errorf("exp exactly at now should be valid, got: %v", err)
		}
	})

	t.Run("nbf_exactly_now_is_valid", func(t *testing.T) {
		// nbf > now means not yet valid (strict greater-than), so nbf == now should be valid.
		now := time.Now()
		token := createTestToken(t, signer, map[string]any{"nbf": float64(now.Unix())}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}
		if err := parsed.ValidateClaimsAt(now); err != nil {
			t.Errorf("nbf exactly at now should be valid, got: %v", err)
		}
	})

	t.Run("nbf_after_exp_impossible_window", func(t *testing.T) {
		// nbf=2000000000, exp=1500000000 — nbf is after exp, creating an impossible window.
		token := createTestToken(t, signer, map[string]any{
			"nbf": float64(2000000000),
			"exp": float64(1500000000),
		}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}

		// Time between exp and nbf — should fail as expired (exp check runs first).
		err = parsed.ValidateClaimsAt(time.Unix(1700000000, 0))
		if err == nil {
			t.Error("expected error for impossible window (between exp and nbf)")
		}
		// Time before exp — should fail as not yet valid.
		err = parsed.ValidateClaimsAt(time.Unix(1400000000, 0))
		if err == nil {
			t.Error("expected error for time before nbf")
		}
		// Time after nbf — should fail as expired.
		err = parsed.ValidateClaimsAt(time.Unix(2100000000, 0))
		if err == nil {
			t.Error("expected error for time after exp but also after nbf")
		}
	})

	t.Run("exp_equals_nbf_zero_width_window", func(t *testing.T) {
		// exp == nbf creates a zero-width validity window.
		// The only valid instant is when now == exp == nbf.
		ts := int64(2000000000)
		token := createTestToken(t, signer, map[string]any{
			"exp": float64(ts),
			"nbf": float64(ts),
		}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}

		// Exactly at the timestamp — should be valid (exp is not < now, nbf is not > now).
		if err := parsed.ValidateClaimsAt(time.Unix(ts, 0)); err != nil {
			t.Errorf("zero-width window at exact time should be valid, got: %v", err)
		}
		// One second before — nbf > now, so not yet valid.
		err = parsed.ValidateClaimsAt(time.Unix(ts-1, 0))
		if err == nil {
			t.Error("expected not-yet-valid error one second before zero-width window")
		}
		// One second after — exp < now, so expired.
		err = parsed.ValidateClaimsAt(time.Unix(ts+1, 0))
		if err == nil {
			t.Error("expected expired error one second after zero-width window")
		}
	})
}

func TestParseTokenStrictJSONEdgeCases(t *testing.T) {
	// Generate a test key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	// Helper to create a token from raw JSON payload
	createTokenFromRawJSON := func(t *testing.T, rawJSON string) string {
		t.Helper()
		payload := []byte(rawJSON)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

		sig, err := sshsig.Sign(bytes.NewReader(payload), signer, sshsig.HashSHA512, "v0@exe.dev")
		if err != nil {
			t.Fatalf("failed to sign: %v", err)
		}

		sigBlobB64 := base64.RawURLEncoding.EncodeToString(sig.Marshal())

		return "exe0." + payloadB64 + "." + sigBlobB64
	}

	t.Run("empty_string_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for empty string payload")
		}
	})

	t.Run("json_array_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "[]")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for JSON array payload")
		}
	})

	t.Run("null_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "null")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for null payload")
		}
		if err != nil && !strings.Contains(err.Error(), "payload must be a JSON object") {
			t.Errorf("expected 'payload must be a JSON object' error, got: %v", err)
		}
	})

	t.Run("null_byte_in_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "{\"exp\":4000000000\x00}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for null byte in payload")
		}
		if err != nil && !strings.Contains(err.Error(), "null bytes") {
			t.Errorf("expected 'null bytes' error, got: %v", err)
		}
	})

	t.Run("null_byte_before_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "\x00{}")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for null byte before payload")
		}
	})

	t.Run("null_byte_after_payload", func(t *testing.T) {
		token := createTokenFromRawJSON(t, "{}\x00")
		_, err := ParseToken(token)
		if err == nil {
			t.Error("expected error for null byte after payload")
		}
	})

	t.Run("exp_null", func(t *testing.T) {
		// null is treated as absent (omitempty behavior).
		token := createTokenFromRawJSON(t, `{"exp":null}`)
		_, err := ParseToken(token)
		if err != nil {
			t.Errorf("exp:null should be accepted (treated as absent), got: %v", err)
		}
	})

	t.Run("cmds_null", func(t *testing.T) {
		// null is treated as absent (omitempty behavior).
		token := createTokenFromRawJSON(t, `{"cmds":null}`)
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("cmds:null should be accepted (treated as absent), got: %v", err)
		}
		if parsed.Cmds != nil {
			t.Errorf("expected nil cmds for cmds:null, got: %v", parsed.Cmds)
		}
	})
}

func TestParsedTokenVerify(t *testing.T) {
	// Generate test keys
	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(privKey)
	pubKey := signer.PublicKey()

	_, wrongPrivKey, _ := ed25519.GenerateKey(rand.Reader)
	wrongSigner, _ := ssh.NewSignerFromKey(wrongPrivKey)
	wrongPubKey := wrongSigner.PublicKey()

	t.Run("valid_signature", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.Verify(pubKey, "v0@exe.dev"); err != nil {
			t.Errorf("Verify failed: %v", err)
		}
	})

	t.Run("wrong_key", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.Verify(wrongPubKey, "v0@exe.dev"); err == nil {
			t.Error("expected Verify to fail with wrong key")
		}
	})

	t.Run("wrong_namespace", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{}, "v0@exe.dev")
		parsed, _ := ParseToken(token)
		if err := parsed.Verify(pubKey, "v0@wrong.namespace"); err == nil {
			t.Error("expected Verify to fail with wrong namespace")
		}
	})

	t.Run("vm_specific_namespace", func(t *testing.T) {
		vmNamespace := "v0@myvm.exe.xyz"
		token := createTestToken(t, signer, map[string]any{"ctx": "data"}, vmNamespace)
		parsed, _ := ParseToken(token)

		// Should verify with correct namespace
		if err := parsed.Verify(pubKey, vmNamespace); err != nil {
			t.Errorf("Verify failed: %v", err)
		}

		// Should fail with different namespace
		if err := parsed.Verify(pubKey, "v0@othervm.exe.xyz"); err == nil {
			t.Error("expected Verify to fail with different VM namespace")
		}
	})
}

func TestValidateClaimsForTrade(t *testing.T) {
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	t.Run("expired_fails", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{"exp": float64(1500000000)}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}
		// Time after exp — should fail.
		err = parsed.ValidateClaimsForTradeAt(time.Unix(1600000000, 0))
		if err == nil {
			t.Error("expected error for expired token in trade")
		}
		if !strings.Contains(err.Error(), "expired") {
			t.Errorf("expected expired error, got: %v", err)
		}
	})

	t.Run("future_nbf_passes", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{"nbf": float64(2000000000)}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}
		// Time before nbf — should pass for trade (nbf not checked).
		err = parsed.ValidateClaimsForTradeAt(time.Unix(1600000000, 0))
		if err != nil {
			t.Errorf("expected future nbf to pass for trade, got: %v", err)
		}
	})

	t.Run("future_exp_future_nbf_passes_trade_fails_use", func(t *testing.T) {
		token := createTestToken(t, signer, map[string]any{
			"exp": float64(2000000000),
			"nbf": float64(1900000000),
		}, "v0@exe.dev")
		parsed, err := ParseToken(token)
		if err != nil {
			t.Fatalf("ParseToken failed: %v", err)
		}
		now := time.Unix(1800000000, 0) // before nbf, before exp

		// Trade should pass (only checks exp).
		if err := parsed.ValidateClaimsForTradeAt(now); err != nil {
			t.Errorf("expected trade to pass, got: %v", err)
		}

		// Use should fail (checks nbf too).
		err = parsed.ValidateClaimsAt(now)
		if err == nil {
			t.Error("expected use-time validation to fail for not-yet-valid token")
		}
		if !strings.Contains(err.Error(), "not yet valid") {
			t.Errorf("expected not-yet-valid error, got: %v", err)
		}
	})
}

func TestValidExe1Token(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"exe1.ABCDEFGHIJKLMNOPQRSTUVWXYZ", true},                 // 26 uppercase letters, all valid base32
		{"exe1.22222222222222222222222222", true},                 // all 2s
		{"exe1.77777777777777777777777777", true},                 // all 7s
		{"exe1.ABCDEFGHIJ234567ABCDEFGHIJ", true},                 // mixed valid base32
		{"exe1.ABCDEFGHIJKLMNOPQRST", true},                       // exactly 20 chars, minimum valid
		{"exe1.ABCDEFGHIJKLMNOPQRS", false},                       // 19 chars, too short
		{"exe1.ABCDEFGHIJKLMNOPQRST89", false},                    // invalid chars 8, 9
		{"exe1.abcdefghijklmnopqrstuvwxyz", false},                // lowercase not valid
		{"exe1.", false},                                          // empty body
		{"exe1.short", false},                                     // too short
		{"exe0.ABCDEFGHIJKLMNOPQRSTUVWXYZ", false},                // wrong prefix
		{"exe1.ABCDEFGHIJKLMNOP01234567AB", false},                // contains 0 and 1
		{"doesnotexist", false},                                   // no prefix
		{"", false},                                               // empty
		{"exe1.ABCDEFGHIJKLMNOPQRST VWXYZ", false},                // contains space
		{"exe1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", true},   // 40 chars, at max
		{"exe1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false}, // 41 chars, over max
		{"exe1." + strings.Repeat("A", 1000), false},              // way too long
	}
	for _, tt := range tests {
		if got := ValidExe1Token(tt.token); got != tt.want {
			t.Errorf("ValidExe1Token(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}
