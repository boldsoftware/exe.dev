package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"
)

func TestClient_CreateToken(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	c := &Client{
		keyID:   "ABC123DEFG",
		teamID:  "TEAM456789",
		privKey: privKey,
	}

	now := time.Now()
	token, err := c.createToken(now)
	if err != nil {
		t.Fatalf("createToken failed: %v", err)
	}

	parts := splitJWT(token)
	if parts == nil {
		t.Fatal("token does not have 3 parts")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("failed to decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("failed to unmarshal header: %v", err)
	}
	if header["alg"] != "ES256" {
		t.Errorf("expected alg=ES256, got %s", header["alg"])
	}
	if header["kid"] != "ABC123DEFG" {
		t.Errorf("expected kid=ABC123DEFG, got %s", header["kid"])
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if claims["iss"] != "TEAM456789" {
		t.Errorf("expected iss=TEAM456789, got %v", claims["iss"])
	}
	if iat, ok := claims["iat"].(float64); !ok || int64(iat) != now.Unix() {
		t.Errorf("expected iat=%d, got %v", now.Unix(), claims["iat"])
	}

	if !VerifyES256(token, &privKey.PublicKey) {
		t.Error("JWT signature verification failed")
	}
}

func TestClient_TokenCaching(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	c := &Client{
		keyID:   "KEY123",
		teamID:  "TEAM123",
		privKey: privKey,
	}

	token1, err := c.getToken()
	if err != nil {
		t.Fatal(err)
	}

	token2, err := c.getToken()
	if err != nil {
		t.Fatal(err)
	}
	if token1 != token2 {
		t.Error("expected cached token, got a different one")
	}

	c.tokenMu.Lock()
	c.tokenEpoch = time.Now().Add(-51 * time.Minute)
	c.tokenMu.Unlock()

	token3, err := c.getToken()
	if err != nil {
		t.Fatal(err)
	}
	if token3 == token1 {
		t.Error("expected new token after expiry, got same one")
	}
}

func TestNewClient(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes}))

	c, err := NewClient("KEYID", "TEAMID", keyPEM, false)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.baseURL != productionURL {
		t.Errorf("expected production URL, got %s", c.baseURL)
	}

	c, err = NewClient("KEYID", "TEAMID", keyPEM, true)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if c.baseURL != sandboxURL {
		t.Errorf("expected sandbox URL, got %s", c.baseURL)
	}

	if _, err = NewClient("", "TEAMID", keyPEM, false); err == nil {
		t.Error("expected error for missing key ID")
	}
	if _, err = NewClient("KEYID", "", keyPEM, false); err == nil {
		t.Error("expected error for missing team ID")
	}
	if _, err = NewClient("KEYID", "TEAMID", "", false); err == nil {
		t.Error("expected error for missing key")
	}
	if _, err = NewClient("KEYID", "TEAMID", "not a pem key", false); err == nil {
		t.Error("expected error for invalid PEM")
	}
}
