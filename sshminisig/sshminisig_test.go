package sshminisig

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	tests := []struct {
		name       string
		keygenArgs []string
		wantPrefix byte
		wantSig    SigAlg
		wantHash   HashAlg
	}{
		{
			name:       "ed25519",
			keygenArgs: []string{"-t", "ed25519"},
			wantPrefix: PrefixEd25519,
			wantSig:    SigEd25519,
			wantHash:   HashSHA512,
		},
		{
			name:       "ecdsa-p256",
			keygenArgs: []string{"-t", "ecdsa", "-b", "256"},
			wantPrefix: PrefixECDSAP256,
			wantSig:    SigECDSAP256,
			wantHash:   HashSHA512,
		},
		{
			name:       "ecdsa-p384",
			keygenArgs: []string{"-t", "ecdsa", "-b", "384"},
			wantPrefix: PrefixECDSAP384,
			wantSig:    SigECDSAP384,
			wantHash:   HashSHA512,
		},
		{
			name:       "ecdsa-p521",
			keygenArgs: []string{"-t", "ecdsa", "-b", "521"},
			wantPrefix: PrefixECDSAP521,
			wantSig:    SigECDSAP521,
			wantHash:   HashSHA512,
		},
		{
			name:       "rsa-sha2-512",
			keygenArgs: []string{"-t", "rsa", "-b", "2048"},
			wantPrefix: PrefixRSA512,
			wantSig:    SigRSA512,
			wantHash:   HashSHA512,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			armored := generateSignature(t, tc.keygenArgs)

			result, err := Encode(armored)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}

			if result[0] != tc.wantPrefix {
				t.Errorf("prefix: got %c, want %c", result[0], tc.wantPrefix)
			}

			// Verify round-trip through Decode
			algs, sigBytes, err := Decode(result)
			if err != nil {
				t.Fatalf("Decode failed: %v", err)
			}

			if algs.Sig != tc.wantSig {
				t.Errorf("sig algo: got %q, want %q", algs.Sig, tc.wantSig)
			}
			if algs.Hash != tc.wantHash {
				t.Errorf("hash algo: got %q, want %q", algs.Hash, tc.wantHash)
			}
			if len(sigBytes) == 0 {
				t.Error("signature bytes empty")
			}
		})
	}
}

func TestInvalidArmor(t *testing.T) {
	_, err := Encode([]byte("not a valid armor"))
	if err == nil {
		t.Error("expected error for invalid armor")
	}
}

func TestPrefixToAlgs(t *testing.T) {
	tests := []struct {
		prefix byte
		sig    SigAlg
		hash   HashAlg
	}{
		{PrefixEd25519, SigEd25519, HashSHA512},
		{PrefixRSA256, SigRSA256, HashSHA256},
		{PrefixRSA512, SigRSA512, HashSHA512},
		{PrefixECDSAP256, SigECDSAP256, HashSHA512},
		{PrefixECDSAP384, SigECDSAP384, HashSHA512},
		{PrefixECDSAP521, SigECDSAP521, HashSHA512},
		{PrefixSKEd25519, SigSKEd25519, HashSHA512},
		{PrefixSKECDSA, SigSKECDSA, HashSHA256},
		{PrefixLegacyRSA256, SigLegacyRSA, HashSHA256},
		{PrefixLegacyRSA512, SigLegacyRSA, HashSHA512},
	}

	for _, tc := range tests {
		algs := PrefixToAlgs[tc.prefix]
		if algs.Sig != tc.sig {
			t.Errorf("PrefixToAlgs[%c]: sig got %q, want %q", tc.prefix, algs.Sig, tc.sig)
		}
		if algs.Hash != tc.hash {
			t.Errorf("PrefixToAlgs[%c]: hash got %q, want %q", tc.prefix, algs.Hash, tc.hash)
		}
	}
}

func TestInvalidPrefix(t *testing.T) {
	algs := PrefixToAlgs['x']
	if algs.Sig != "" || algs.Hash != "" {
		t.Errorf("got %+v for unknown prefix, want zero value", algs)
	}
}

// generateSignature creates a temp key and signs a test message.
func generateSignature(t *testing.T, keygenArgs []string) []byte {
	t.Helper()

	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "testkey")

	// Generate key
	args := append([]string{"-f", keyPath, "-N", ""}, keygenArgs...)
	cmd := exec.Command("ssh-keygen", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen generate failed: %v\n%s", err, out)
	}

	// Sign a test message
	cmd = exec.Command("ssh-keygen", "-Y", "sign", "-f", keyPath, "-n", "test")
	cmd.Stdin = strings.NewReader("test message")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("ssh-keygen sign failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("ssh-keygen sign failed: %v", err)
	}

	return out
}
