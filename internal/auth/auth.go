// Package auth guards the /v1 API with bearer tokens. The manager UI, /metrics
// and the llama-server WebUI proxy stay open (local/trusted).
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nilsherzig/local-inference-manager/internal/store"
)

type ctxKey struct{}

// Middleware rejects /v1 requests without a valid bearer token. On success it
// stores the token ID in the request context.
func Middleware(tokens store.TokenStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearer(r)
			if raw == "" {
				writeError(w, http.StatusUnauthorized, "missing bearer token; create one at "+tokensURL(r))
				return
			}
			tok, err := tokens.Lookup(raw)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "token lookup failed")
				return
			}
			if tok == nil {
				writeError(w, http.StatusUnauthorized, "invalid token; create one at "+tokensURL(r))
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, tok.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TokenID returns the authenticated token ID from the context, if any.
func TokenID(ctx context.Context) *string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return &v
	}
	return nil
}

// tokensURL builds the absolute URL of the token page from the incoming
// request, so the error tells the caller exactly where to create a token.
func tokensURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host + "/tokens"
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": msg},
	})
}
