package sshkey

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

// Fingerprint parses an SSH public key string and returns its fingerprint.
// The fingerprint is the SHA256 hash, base64 encoded, without the "SHA256:" prefix.
func Fingerprint(pubKeyStr string) (string, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKeyStr))
	if err != nil {
		return "", err
	}
	return FingerprintForKey(pubKey), nil
}

// FingerprintForKey computes the fingerprint from an already-parsed SSH public key.
// The fingerprint is the SHA256 hash, base64 encoded, without the "SHA256:" prefix.
func FingerprintForKey(pubKey ssh.PublicKey) string {
	return strings.TrimPrefix(ssh.FingerprintSHA256(pubKey), "SHA256:")
}
