package gateway

import (
	"net/http"
	"strings"

	"gatekeeper/internal/auth"
	"gatekeeper/internal/config"
	"gatekeeper/internal/middleware"
	"gatekeeper/internal/reqctx"
	"gatekeeper/internal/router"
)

// authenticate enforces the matched route's auth mode. apiKeyStore/
// jwtVerifier are nil unless some route actually needs them (Build only
// constructs what's used); that's safe here because a route can only
// resolve to AuthAPIKey/AuthJWT if Build already required the matching
// store/verifier to exist for this exact config.
func authenticate(apiKeyStore *auth.APIKeyStore, jwtVerifier *auth.JWTVerifier) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, _ := r.Context().Value(routeContextKey{}).(*router.Route)

			switch route.Auth {
			case config.AuthAPIKey:
				key := r.Header.Get("X-API-Key")
				name, ok := apiKeyStore.Verify(key)
				if !ok {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
					fields.UserID = name
				}

			case config.AuthJWT:
				token := bearerToken(r)
				if token == "" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				claims, err := jwtVerifier.Verify(r.Context(), token)
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				r.Header.Set("X-User-ID", claims.Subject)
				if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
					fields.UserID = claims.Subject
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimPrefix(h, prefix)
}
