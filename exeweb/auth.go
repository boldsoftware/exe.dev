package exeweb

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/go4org/hashtriemap"
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

// CookieUsesCache caches the UTC date of the last time a cookie
// was marked as used. We write once per cookie per day to avoid
// per-request DB writes.
type CookieUsesCache struct {
	cache hashtriemap.HashTrieMap[string, time.Time]
}

// Touch is called when we use a cookie.
// It reports whether the database should be updated.
func (cuc *CookieUsesCache) Touch(cookieValue string) bool {
	year, month, day := time.Now().UTC().Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if prev, ok := cuc.cache.Load(cookieValue); !ok || prev.Before(today) {
		// It is possible for this to be run simultaneously
		// by two different goroutines causing both to see
		// a true return and update the database.
		// We ignore this minor unlikely inefficiency.
		cuc.cache.Store(cookieValue, today)
		return true
	}
	return false
}

// Delete removes a cookie from the cache.
func (cuc *CookieUsesCache) Delete(cookieValue string) {
	cuc.cache.Delete(cookieValue)
}

// Clean starts a separate goroutine removing stale entries from the cache.
func (cuc *CookieUsesCache) Clean(ctx context.Context) {
	go func(ctx context.Context) {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				old := time.Now().Add(-25 * time.Hour)
				for cookieValue, t := range cuc.cache.All() {
					if t.Before(old) {
						cuc.cache.Delete(cookieValue)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}(ctx)
}
