package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestCertificateSignersFromData(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate host key: %v", err)
	}

	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("failed to build host signer: %v", err)
	}

	hostKeyID := string(hostSigner.PublicKey().Marshal())
	hostSigners := map[string]ssh.Signer{
		hostKeyID: hostSigner,
	}

	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ca key: %v", err)
	}

	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatalf("failed to build ca signer: %v", err)
	}

	cert := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		CertType:        ssh.HostCert,
		KeyId:           "test",
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
		ValidPrincipals: []string{"example.com"},
	}

	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("failed to sign host certificate: %v", err)
	}

	certData := ssh.MarshalAuthorizedKey(cert)

	signers, err := certificateSignersFromData(certData, "test-source", hostSigners)
	if err != nil {
		t.Fatalf("unexpected error creating certificate signers: %v", err)
	}

	if len(signers) != 1 {
		t.Fatalf("expected 1 certificate signer, got %d", len(signers))
	}

	entry := signers[0]

	if entry.hostKeyID != hostKeyID {
		t.Fatalf("unexpected host key id, expected %q, got %q", hostKeyID, entry.hostKeyID)
	}

	pub := entry.signer.PublicKey()
	if _, ok := pub.(*ssh.Certificate); !ok {
		t.Fatalf("expected signer public key to be a certificate, got %T", pub)
	}

	if !bytes.Equal(pub.Marshal(), cert.Marshal()) {
		t.Fatalf("certificate payload mismatch")
	}
}

func TestCertificateSignersFromDataMissingHostKey(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate host key: %v", err)
	}

	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("failed to build host signer: %v", err)
	}

	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ca key: %v", err)
	}

	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatalf("failed to build ca signer: %v", err)
	}

	cert := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		CertType:        ssh.HostCert,
		KeyId:           "test",
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
		ValidPrincipals: []string{"example.com"},
	}

	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("failed to sign host certificate: %v", err)
	}

	certData := ssh.MarshalAuthorizedKey(cert)

	if _, err := certificateSignersFromData(certData, "test-source", map[string]ssh.Signer{}); err == nil {
		t.Fatalf("expected error when no matching host key is available")
	}
}
