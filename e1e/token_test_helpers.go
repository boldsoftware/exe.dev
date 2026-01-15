package e1e

import (
	"bytes"
	"encoding/base64"
	"os"
	"testing"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// This file provides shared token generation utilities for tests that use
// SSH signature-based authentication (exec API and proxy token auth).

// loadTestSigner reads a private key file and returns an ssh.Signer.
func loadTestSigner(t *testing.T, keyFile string) ssh.Signer {
	t.Helper()

	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("failed to read key file: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("failed to parse private key: %v", err)
	}

	return signer
}

// generateToken creates a signed token with the given JSON payload and namespace.
// The token format is: exe0.base64(payload).base64(sigblob)
func generateToken(t *testing.T, signer ssh.Signer, jsonPayload, namespace string) string {
	t.Helper()

	payload := []byte(jsonPayload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, signer, payload, namespace)

	return "exe0." + payloadB64 + "." + sigBlob
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
