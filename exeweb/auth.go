package exeweb

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// MagicSecret represents a temporary authentication secret
// for proxy magic URLs.
type MagicSecret struct {
	UserID      string
	BoxName     string // Direct box name instead of team
	RedirectURL string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// MagicSecrets holds a collection of MagicSecret values.
type MagicSecrets struct {
	mu           sync.Mutex
	magicSecrets map[string]*MagicSecret
}

// NewMagicSecrets returns a new MagicSecrets value.
func NewMagicSecrets() *MagicSecrets {
	return &MagicSecrets{
		magicSecrets: make(map[string]*MagicSecret),
	}
}

// Create creates a temporary magic secret for proxy authentication.
func (ms *MagicSecrets) Create(userID, boxName, redirectURL string) (string, error) {
	// Generate a random secret.
	secret := rand.Text()

	// Clean up expired secrets while we're here.
	ms.cleanupExpiredSecrets()

	// Store in memory with 2-minute expiration.
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.magicSecrets[secret] = &MagicSecret{
		UserID:      userID,
		BoxName:     boxName,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(2 * time.Minute),
		CreatedAt:   time.Now(),
	}

	return secret, nil
}

// Validate validates and consumes a magic secret.
func (ms *MagicSecrets) Validate(secret string) (*MagicSecret, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	magicSecret, exists := ms.magicSecrets[secret]
	if !exists {
		return nil, errors.New("invalid secret")
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		// Clean up expired secret.
		delete(ms.magicSecrets, secret)
		return nil, errors.New("secret expired")
	}

	// Secret is valid, consume it (single use)
	delete(ms.magicSecrets, secret)

	return magicSecret, nil
}

// Peek looks up a magic secret without deleting it.
// It returns and secrets and reports whether it exists.
func (ms *MagicSecrets) Peek(secret string) (*MagicSecret, bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	magicSecret, exists := ms.magicSecrets[secret]
	return magicSecret, exists
}

// Delete deletes a magic secret without validating it.
func (ms *MagicSecrets) Delete(secret string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	delete(ms.magicSecrets, secret)
}

// cleanupExpiredMagicSecrets removes expired magic secrets from memory
func (ms *MagicSecrets) cleanupExpiredSecrets() {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	now := time.Now()
	for secret, magicSecret := range ms.magicSecrets {
		if now.After(magicSecret.ExpiresAt) {
			delete(ms.magicSecrets, secret)
		}
	}
}
