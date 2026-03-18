package server

import (
	"crypto/subtle"
	"net/http"
)

// TokenAuth returns middleware that validates the Authorization: Bearer <token> header.
func TokenAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("Authorization")
			expected := "Bearer " + token

			if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
