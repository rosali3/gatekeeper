package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader is the header carrying the request id, both on the
// response to the client and forwarded on to the upstream.
const RequestIDHeader = "X-Request-ID"

// RequestID ensures every request has an X-Request-ID: it keeps the
// client's value if present, otherwise generates one. Setting it directly
// on r.Header (rather than only in context) means it's automatically
// forwarded upstream by the reverse proxy, which copies request headers.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" {
				id = generateRequestID()
				r.Header.Set(RequestIDHeader, id)
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r)
		})
	}
}

func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on a supported OS practically never fails; if it
		// somehow does, an all-zero id is still a valid (if unlucky)
		// identifier rather than a reason to error the request.
		return hex.EncodeToString(b[:])
	}
	return hex.EncodeToString(b[:])
}
