package middleware

import "net/http"

// BodyLimit rejects request bodies larger than maxBytes. http.MaxBytesReader
// makes the body return an error once the limit is exceeded, which the
// standard library's request reading turns into a 413 for the client.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
