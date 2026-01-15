// Package sshminisig converts Armored SSH Signatures to the compact sshminisig format.
//
// The sshminisig format is:
//   - one byte prefix indicating the combination of signature algorithm and hash algorithm
//   - the signature, base64url-encoded, without padding
package sshminisig

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
)

// SigAlg is an SSH signature algorithm name.
type SigAlg string

// HashAlg is a hash algorithm name.
type HashAlg string

// Signature algorithm constants.
const (
	SigEd25519   SigAlg = "ssh-ed25519"
	SigRSA256    SigAlg = "rsa-sha2-256"
	SigRSA512    SigAlg = "rsa-sha2-512"
	SigECDSAP256 SigAlg = "ecdsa-sha2-nistp256"
	SigECDSAP384 SigAlg = "ecdsa-sha2-nistp384"
	SigECDSAP521 SigAlg = "ecdsa-sha2-nistp521"
	SigSKEd25519 SigAlg = "sk-ssh-ed25519@openssh.com"
	SigSKECDSA   SigAlg = "sk-ecdsa-sha2-nistp256@openssh.com"
	SigLegacyRSA SigAlg = "ssh-rsa"
)

// Hash algorithm constants.
const (
	HashSHA256 HashAlg = "sha256"
	HashSHA512 HashAlg = "sha512"
)

// Algs combines a signature algorithm and hash algorithm.
type Algs struct {
	Sig  SigAlg
	Hash HashAlg
}

// Prefix bytes for algorithm combinations.
const (
	PrefixEd25519      = 'e' // ssh-ed25519 + sha512
	PrefixRSA256       = 'r' // rsa-sha2-256 + sha256
	PrefixRSA512       = 's' // rsa-sha2-512 + sha512
	PrefixECDSAP256    = 'c' // ecdsa-sha2-nistp256 + sha512
	PrefixECDSAP384    = 'd' // ecdsa-sha2-nistp384 + sha512
	PrefixECDSAP521    = 'p' // ecdsa-sha2-nistp521 + sha512
	PrefixSKEd25519    = 'f' // sk-ssh-ed25519@openssh.com + sha512
	PrefixSKECDSA      = 'g' // sk-ecdsa-sha2-nistp256@openssh.com + sha256
	PrefixLegacyRSA256 = '2' // ssh-rsa + sha256
	PrefixLegacyRSA512 = '5' // ssh-rsa + sha512
	PrefixReserved     = 'z' // Reserved for forward-compatibility
)

var algsToPrefix = map[Algs]byte{
	{SigEd25519, HashSHA512}:   PrefixEd25519,
	{SigRSA256, HashSHA256}:    PrefixRSA256,
	{SigRSA512, HashSHA512}:    PrefixRSA512,
	{SigECDSAP256, HashSHA512}: PrefixECDSAP256,
	{SigECDSAP384, HashSHA512}: PrefixECDSAP384,
	{SigECDSAP521, HashSHA512}: PrefixECDSAP521,
	{SigSKEd25519, HashSHA512}: PrefixSKEd25519,
	{SigSKECDSA, HashSHA256}:   PrefixSKECDSA,
	{SigLegacyRSA, HashSHA256}: PrefixLegacyRSA256,
	{SigLegacyRSA, HashSHA512}: PrefixLegacyRSA512,
}

// PrefixToAlgs maps each prefix byte to its algorithm combination.
// Invalid prefixes map to the zero value.
var PrefixToAlgs [256]Algs

func init() {
	for algs, prefix := range algsToPrefix {
		PrefixToAlgs[prefix] = algs
	}
}

// maxArmoredSize is the maximum reasonable size for an armored SSH signature.
// RSA-8192 with a large namespace fits in ~4KB; 8KB is paranoid-safe.
const maxArmoredSize = 8 * 1024

// Encode converts an armored SSH signature to sshminisig format.
func Encode(armored []byte) (string, error) {
	if len(armored) > maxArmoredSize {
		return "", errors.New("armored signature too large")
	}
	block, _ := pem.Decode(armored)
	if block == nil || block.Type != "SSH SIGNATURE" {
		return "", errors.New("invalid armored SSH signature")
	}

	sigAlg, hashAlg, sigData, err := parseSignatureBlob(block.Bytes)
	if err != nil {
		return "", err
	}

	prefix, ok := algsToPrefix[Algs{SigAlg(sigAlg), HashAlg(hashAlg)}]
	if !ok {
		return "", fmt.Errorf("unsupported algorithm: %q with %q", sigAlg, hashAlg)
	}

	return string(prefix) + base64.RawURLEncoding.EncodeToString(sigData), nil
}

// parseSignatureBlob parses the SSH signature blob and extracts the algorithm, hash, and signature data.
func parseSignatureBlob(blob []byte) (sigAlgName, hashAlgName string, sigData []byte, err error) {
	b := blob

	// Verify magic preamble
	if len(b) < 6 || string(b[:6]) != "SSHSIG" {
		return "", "", nil, errors.New("invalid magic preamble")
	}
	b = b[6:]

	// Verify version
	if len(b) < 4 || binary.BigEndian.Uint32(b[:4]) != 1 {
		return "", "", nil, errors.New("invalid signature version")
	}
	b = b[4:]

	// Skip past public key, namespace, reserved
	for range 3 {
		_, b = readString(b)
	}
	if b == nil {
		return "", "", nil, errors.New("truncated signature blob")
	}

	// Read hash algorithm and signature blob
	var hashAlg, sigBlob []byte
	hashAlg, b = readString(b)
	sigBlob, _ = readString(b)
	if sigBlob == nil {
		return "", "", nil, errors.New("invalid signature blob")
	}

	// Parse signature blob: algorithm + data + optional trailing data (SK flags/counter)
	var sigAlg []byte
	sigAlg, sigBlob = readString(sigBlob)
	sigData, sigBlob = readString(sigBlob)
	if sigData == nil {
		return "", "", nil, errors.New("invalid signature blob")
	}

	// Append any remaining data (e.g., SK flags and counter)
	if len(sigBlob) > 0 {
		sigData = append(sigData, sigBlob...)
	}

	return string(sigAlg), string(hashAlg), sigData, nil
}

// readString reads an SSH-style string (uint32 length prefix + data).
func readString(b []byte) (data, rest []byte) {
	if len(b) < 4 {
		return nil, nil
	}
	n := binary.BigEndian.Uint32(b)
	b = b[4:]
	if len(b) < int(n) {
		return nil, nil
	}
	return b[:n], b[n:]
}

// Decode parses an sshminisig and returns the algorithm info and raw signature bytes.
func Decode(minisig string) (Algs, []byte, error) {
	if len(minisig) < 2 {
		return Algs{}, nil, errors.New("sshminisig too short")
	}

	algs := PrefixToAlgs[minisig[0]]
	if algs.Sig == "" {
		return Algs{}, nil, fmt.Errorf("unknown prefix: %c", minisig[0])
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(minisig[1:])
	if err != nil {
		return Algs{}, nil, fmt.Errorf("failed to decode signature: %w", err)
	}

	return algs, sigBytes, nil
}
