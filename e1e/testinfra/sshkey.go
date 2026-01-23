package testinfra

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenSSHKey generates an SSH keypair.
// The private half goes into a file in dir to satisfy ssh,
// and the public half is returned as a string,
// for testing convenience.
func GenSSHKey(dir string) (path, publicKey string, err error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate ed25519 key: %v", err)
	}

	privKeyPath := filepath.Join(dir, "id_ed25519")
	privKeyFile, err := os.OpenFile(privKeyPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("failed to create private key file: %v", err)
	}
	privateKeyBytes, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %v", err)
	}
	if err := pem.Encode(privKeyFile, privateKeyBytes); err != nil {
		return "", "", fmt.Errorf("failed to write private key: %v", err)
	}
	if err = privKeyFile.Close(); err != nil {
		return "", "", fmt.Errorf("failed to close private key file: %v", err)
	}

	pubKey := privateKey.Public().(ed25519.PublicKey)
	sshPublicKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to create SSH public key: %v", err)
	}
	pubStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublicKey)))
	AddCanonicalization(pubStr, "SSH_PUBKEY")
	return privKeyPath, pubStr, nil
}

// GenSSHKeyWithComment generates an SSH keypair with a custom comment.
// The private half goes into a file in dir to satisfy ssh,
// and the public half (with comment appended) is returned as a string.
func GenSSHKeyWithComment(dir, comment string) (path, publicKey string, err error) {
	path, pubKey, err := GenSSHKey(dir)
	if err != nil {
		return "", "", err
	}
	// Append the comment to the public key
	return path, pubKey + " " + comment, nil
}
