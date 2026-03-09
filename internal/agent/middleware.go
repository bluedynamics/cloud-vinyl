package agent

import (
	"net/http"
	"strings"
)

// BearerAuthMiddleware returns a middleware that validates Bearer tokens.
// Paths in skipPaths bypass authentication entirely.
func BearerAuthMiddleware(token string, next http.Handler, skipPaths ...string) http.Handler {
	skipSet := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		skipSet[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipSet[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, `{"error":"missing or invalid Authorization header"}`, http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(auth, "Bearer ")
		if provided != token {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
