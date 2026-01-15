package sshkey

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// TestVerifyKeyTypes tests the full token pipeline (ParseToken → Verify)
// with keys generated and signatures created by ssh-keygen,
// matching the real user workflow.
func TestVerifyKeyTypes(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found")
	}

	keyTypes := []struct {
		name string
		args []string
	}{
		{"rsa-4096", []string{"-t", "rsa", "-b", "4096"}},
		{"ecdsa-256", []string{"-t", "ecdsa", "-b", "256"}},
		{"ecdsa-384", []string{"-t", "ecdsa", "-b", "384"}},
		{"ed25519", []string{"-t", "ed25519"}},
	}

	for _, kt := range keyTypes {
		t.Run(kt.name, func(t *testing.T) {
			dir := t.TempDir()
			keyFile := filepath.Join(dir, "key")

			// Generate key with ssh-keygen (same as users do).
			args := append(kt.args, "-N", "", "-f", keyFile)
			out, err := exec.Command("ssh-keygen", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("ssh-keygen keygen: %v\n%s", err, out)
			}

			// Read and parse private key.
			keyBytes, err := os.ReadFile(keyFile)
			if err != nil {
				t.Fatalf("read key: %v", err)
			}
			signer, err := ssh.ParsePrivateKey(keyBytes)
			if err != nil {
				t.Fatalf("parse key: %v", err)
			}
			pubKey := signer.PublicKey()
			expectedFP := strings.TrimPrefix(ssh.FingerprintSHA256(pubKey), "SHA256:")

			message := []byte(`{"exp":4000000000}`)
			namespace := "v0@exe.dev"
			payloadB64 := base64.RawURLEncoding.EncodeToString(message)

			// Test 1: Sign with ssh-keygen -Y sign, verify through ParseToken + Verify.
			// This matches the real user workflow.
			t.Run("ssh-keygen", func(t *testing.T) {
				cmd := exec.Command("ssh-keygen", "-Y", "sign", "-f", keyFile, "-n", namespace)
				cmd.Stdin = bytes.NewReader(message)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("ssh-keygen sign: %v\n%s", err, stderr.String())
				}

				block, _ := pem.Decode(stdout.Bytes())
				if block == nil {
					t.Fatalf("failed to decode armored signature:\n%s", stdout.String())
				}
				sigBlobB64 := base64.RawURLEncoding.EncodeToString(block.Bytes)
				token := "exe0." + payloadB64 + "." + sigBlobB64

				parsed, err := ParseToken(token)
				if err != nil {
					t.Fatalf("ParseToken: %v", err)
				}
				if parsed.Fingerprint != expectedFP {
					t.Errorf("fingerprint: got %q, want %q", parsed.Fingerprint, expectedFP)
				}
				if err := parsed.Verify(pubKey, namespace); err != nil {
					t.Fatalf("Verify: %v", err)
				}
			})

			// Test 2: Sign with Go sshsig library, verify through ParseToken + Verify.
			// Tests interop in the other direction.
			t.Run("go-sshsig", func(t *testing.T) {
				goSig, err := sshsig.Sign(bytes.NewReader(message), signer, sshsig.HashSHA512, namespace)
				if err != nil {
					t.Fatalf("sshsig.Sign: %v", err)
				}
				sigBlobB64 := base64.RawURLEncoding.EncodeToString(goSig.Marshal())
				token := "exe0." + payloadB64 + "." + sigBlobB64

				parsed, err := ParseToken(token)
				if err != nil {
					t.Fatalf("ParseToken: %v", err)
				}
				if parsed.Fingerprint != expectedFP {
					t.Errorf("fingerprint: got %q, want %q", parsed.Fingerprint, expectedFP)
				}
				if err := parsed.Verify(pubKey, namespace); err != nil {
					t.Fatalf("Verify: %v", err)
				}
			})
		})
	}
}
