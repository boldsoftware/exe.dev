package sshkey

import (
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateToken(t *testing.T) {
	namespace := "v0@exe.dev"
	permsJSON := []byte(`{"cmds":["whoami","ls"]}`)

	gt, err := GenerateToken(permsJSON, namespace)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// Verify the exe0 token is parseable
	parsed, err := ParseToken(gt.Exe0Token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}

	// Check fingerprint matches
	if parsed.Fingerprint != gt.Fingerprint {
		t.Errorf("fingerprint mismatch: got %q, want %q", parsed.Fingerprint, gt.Fingerprint)
	}

	// Verify the token's signature using the generated public key
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(gt.PublicKeyAuth))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	if err := parsed.Verify(pubKey, namespace); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Verify claims parsed correctly
	cmds, ok := parsed.PayloadJSON["cmds"]
	if !ok {
		t.Fatal("cmds not in payload")
	}
	cmdsSlice, ok := cmds.([]any)
	if !ok || len(cmdsSlice) != 2 {
		t.Fatalf("unexpected cmds: %v", cmds)
	}
}

func TestGenerateToken_EmptyPermissions(t *testing.T) {
	gt, err := GenerateToken([]byte(`{}`), "v0@exe.dev")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	parsed, err := ParseToken(gt.Exe0Token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(gt.PublicKeyAuth))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	if err := parsed.Verify(pubKey, "v0@exe.dev"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestGenerateToken_VMNamespace(t *testing.T) {
	ns := "v0@myvm.exe.xyz"
	gt, err := GenerateToken([]byte(`{}`), ns)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	parsed, err := ParseToken(gt.Exe0Token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(gt.PublicKeyAuth))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}

	// Should verify with the correct namespace
	if err := parsed.Verify(pubKey, ns); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Should fail with a different namespace
	if err := parsed.Verify(pubKey, "v0@exe.dev"); err == nil {
		t.Fatal("expected verification to fail with wrong namespace")
	}
}
