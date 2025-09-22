package exedb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"

	"golang.org/x/crypto/ssh"
)

// Route represents a routing configuration for a box
type Route struct {
	Port  int    `json:"port"`
	Share string `json:"share"`
}

// GetRoute returns the routing configuration for the box
func (b *Box) GetRoute() Route {
	if b.Routes == nil || *b.Routes == "" {
		return DefaultRoute()
	}

	var route Route
	err := json.Unmarshal([]byte(*b.Routes), &route)
	if err != nil {
		return DefaultRoute()
	}

	return route
}

// SetRoute sets the box's routing configuration
func (b *Box) SetRoute(route Route) {
	data, err := json.Marshal(route)
	if err != nil {
		panic("Failed to marshal route: " + err.Error())
	}
	routesStr := string(data)
	b.Routes = &routesStr
}

// DefaultRoute returns the default routing configuration
func DefaultRoute() Route {
	return Route{
		Port:  80,
		Share: "private",
	}
}

// DefaultRouteJSON returns the default route as JSON.
func DefaultRouteJSON() string {
	route := DefaultRoute()
	data, err := json.Marshal(route)
	if err != nil {
		log.Fatalf("Failed to marshal default route: %v", err)
	}
	return string(data)
}

// CreateHostKeyCallback creates a proper SSH host key validation callback
// that verifies the presented host key against this box's SSH server identity key
func (b *Box) CreateHostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Ensure we have an SSH server identity key
		if len(b.SSHServerIdentityKey) == 0 {
			return fmt.Errorf("no SSH server identity key available for box %s", b.Name)
		}

		// Parse the server identity private key to extract the public key
		privKey, err := ssh.ParsePrivateKey(b.SSHServerIdentityKey)
		if err != nil {
			return fmt.Errorf("failed to parse server identity key for box %s: %w", b.Name, err)
		}

		// Compare the keys by comparing their marshaled bytes
		if !bytes.Equal(key.Marshal(), privKey.PublicKey().Marshal()) {
			return fmt.Errorf("host key mismatch for %s: presented key does not match expected key for box %s", hostname, b.Name)
		}

		return nil
	}
}
