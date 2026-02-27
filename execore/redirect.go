package execore

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"exe.dev/exedb"
)

const redirectTTL = 10 * time.Minute

// createRedirect stores a short redirect key that maps to target.
// It returns the key (suitable for appending to /r/).
// Callers are responsible for passing only trusted URLs as target;
// the redirect is issued verbatim with no validation.
func (s *Server) createRedirect(ctx context.Context, target string) (string, error) {
	key := crand.Text()
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.CleanupExpiredRedirects(ctx, time.Now()); err != nil {
			s.slog().WarnContext(ctx, "cleanup expired redirects", "error", err)
		}
		return queries.InsertRedirect(ctx, exedb.InsertRedirectParams{
			Key:       key,
			Target:    target,
			ExpiresAt: time.Now().Add(redirectTTL),
		})
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// redirectURL returns the full short URL for a redirect key.
func (s *Server) redirectURL(key string) string {
	return s.webBaseURLNoRequest() + "/r/" + key
}

// handleRedirect looks up a /r/<key> redirect and sends a 302.
func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request, key string) {
	var target string
	err := s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		target, err = queries.GetRedirect(ctx, exedb.GetRedirectParams{
			Key:       key,
			ExpiresAt: time.Now(),
		})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "redirect lookup failed", "error", err, "key", key)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// isRedirectRequest checks if the path is /r/<key> and returns the key.
func isRedirectRequest(path string) (string, bool) {
	key, ok := strings.CutPrefix(path, "/r/")
	if !ok || key == "" {
		return "", false
	}
	// Reject keys with slashes (no sub-paths).
	if strings.Contains(key, "/") {
		return "", false
	}
	return key, true
}
