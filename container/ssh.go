package container

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// ContainerSSHKeys holds the SSH key material for a container
type ContainerSSHKeys struct {
	ServerIdentityKey string // SSH server private key (PEM format)
	AuthorizedKeys    string // User certificate for authorized_keys (client auth)
	ClientPrivateKey  string // Private key for connecting to container (PEM format)
	SSHPort           int    // SSH port for this container
}

// GenerateContainerSSHKeys generates all SSH key material needed for a container
// This is adapted from the generateSSHKeys function in bold/skaband/dockerhost
func GenerateContainerSSHKeys() (*ContainerSSHKeys, error) {
	// Generate server identity key (Ed25519)
	_, serverPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	// Generate user/client key for connecting to container
	clientPub, clientPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate client key: %w", err)
	}

	// Convert to SSH format
	clientSSHPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		return nil, fmt.Errorf("failed to create client SSH public key: %w", err)
	}

	// Marshal keys to PEM/SSH format
	serverPrivKeyPEM, err := ssh.MarshalPrivateKey(serverPriv, "exe container server key")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal server private key: %w", err)
	}

	clientPrivKeyPEM, err := ssh.MarshalPrivateKey(clientPriv, "exe container client key")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal client private key: %w", err)
	}

	return &ContainerSSHKeys{
		ServerIdentityKey: string(pem.EncodeToMemory(serverPrivKeyPEM)),
		AuthorizedKeys:    string(ssh.MarshalAuthorizedKey(clientSSHPub)), // Public key for authorized_keys file
		ClientPrivateKey:  string(pem.EncodeToMemory(clientPrivKeyPEM)),
		// SSH port is always 22 inside containers
		// (though nerdctl exposes this to a port on the container host)
		SSHPort: 22,
	}, nil
}

// ParsePrivateKey parses a PEM-encoded private key string
func ParsePrivateKey(pemData string) (any, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	switch block.Type {
	case "OPENSSH PRIVATE KEY":
		return ssh.ParseRawPrivateKey([]byte(pemData))
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported private key type: %s", block.Type)
	}
}

// CreateSSHSigner creates an SSH signer from a PEM-encoded private key
func CreateSSHSigner(pemData string) (ssh.Signer, error) {
	privKey, err := ParsePrivateKey(pemData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return ssh.NewSignerFromKey(privKey)
}
