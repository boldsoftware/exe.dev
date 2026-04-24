package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// whoisTimeout bounds how long auth will wait for a Tailscale whois
// lookup before failing closed.
const whoisTimeout = 3 * time.Second

// User is the authenticated Tailscale user for a request.
type User struct {
	LoginName   string
	DisplayName string
}

type userCtxKey struct{}

// UserFromContext returns the authenticated Tailscale user attached by
// RequireHumanTailscaleUser, if any.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(User)
	return u, ok
}

func contextWithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// RequireHumanTailscaleUser returns middleware that admits only
// requests whose Tailscale peer is a human user. Tagged nodes (e.g.
// agents, CI runners), loopback connections, and anything the local
// tailscaled cannot identify are rejected with 403. The authenticated
// User is attached to the request context for downstream handlers.
func RequireHumanTailscaleUser(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), whoisTimeout)
			defer cancel()

			who, err := TailscaleWhoIs(ctx, r.RemoteAddr)
			if err != nil {
				log.Warn("auth: tailscale whois failed",
					"remote_addr", r.RemoteAddr,
					"path", r.URL.Path,
					"error", err,
				)
				http.Error(w, "forbidden: tailscale identity required", http.StatusForbidden)
				return
			}
			if !who.IsHuman() {
				log.Warn("auth: non-human tailscale peer denied",
					"remote_addr", r.RemoteAddr,
					"path", r.URL.Path,
					"tags", who.Tags,
					"computed_name", who.ComputedName,
				)
				http.Error(w, "forbidden: human tailscale user required", http.StatusForbidden)
				return
			}

			u := User{LoginName: who.LoginName, DisplayName: who.DisplayName}
			next.ServeHTTP(w, r.WithContext(contextWithUser(r.Context(), u)))
		})
	}
}
