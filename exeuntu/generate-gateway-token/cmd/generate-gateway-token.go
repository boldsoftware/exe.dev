package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// bearerTokenClaim holds a claim that a request originated from a client who has access to
// a parituclar box's ssh server identitys private key.  The http header is encoded as a
// {claim}.{signature} string, and would appear in an http header like so:
//
// `Authorization: Bearer <base64(json(claim))>.<base64(signature(json(claim)))>`
//
// The json format of the claim is defined by type bearerTokenClaim's json encoding
// struct tags.
type bearerTokenClaim struct {
	BoxName    string    `json:"box_name"`
	CreatedAt  time.Time `json:"created_at"`
	TTLSeconds int       `json:"ttl_seconds"`
}

func (b *bearerTokenClaim) Sign(signer ssh.Signer) (*ssh.Signature, error) {
	claimBytes, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	sig, err := signer.Sign(rand.Reader, claimBytes)
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	return sig, nil
}

// Encode returns the base64-encoded token suitable for an "Authorization: Bearer {value}"
// http header.  Returns an error if marshaling or signing fail.
func (b *bearerTokenClaim) Encode(signer ssh.Signer) (string, error) {
	claimBytes, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("Encode marshaling claim: %w", err)
	}

	claimStr := base64.StdEncoding.EncodeToString(claimBytes)

	sig, err := b.Sign(signer)
	if err != nil {
		return "", fmt.Errorf("Encode signing claim: %w", err)
	}

	sigBytes := ssh.Marshal(sig)

	sigStr := base64.StdEncoding.EncodeToString(sigBytes)

	return claimStr + "." + sigStr, nil
}

var (
	hostnameFlag   = flag.String("host", "", "name of host")
	keyFile        = flag.String("key", "/exe.dev/etc/ssh/ssh_host_ed25519_key", "path to ssh host identity key")
	ttlSecondsFlag = flag.Int("ttl_seconds", 60*60*25, "time to live, in seconds")
)

func main() {
	flag.Parse()
	hostname := *hostnameFlag
	var err error
	if hostname == "" {
		hostname, err = os.Hostname()
		if err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			os.Exit(1)
		}
	}

	btok := &bearerTokenClaim{
		BoxName:    hostname,
		CreatedAt:  time.Now(),
		TTLSeconds: *ttlSecondsFlag,
	}

	pemBytes, err := os.ReadFile(*keyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}

	key, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
	tokStr, err := btok.Encode(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
	fmt.Printf("%s\n", tokStr)
}
