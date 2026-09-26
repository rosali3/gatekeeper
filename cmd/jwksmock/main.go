// Command jwksmock is a minimal JWKS + token-issuing server for the
// docker-compose demo's JWT auth route - not something gatekeeper links
// against, just a standalone stand-in for a real identity provider.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const kid = "demo-key-1"

func main() {
	addr := getEnv("ADDR", ":8000")
	issuer := getEnv("ISSUER", "gatekeeper-demo")
	audience := getEnv("AUDIENCE", "gatekeeper")

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generating RSA key: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", handleJWKS(&key.PublicKey))
	mux.HandleFunc("/token", handleToken(key, issuer, audience))

	log.Printf("jwksmock listening on %s (issuer=%s audience=%s)", addr, issuer, audience)
	if err := http.ListenAndServe(addr, mux); err != nil { //nolint:gosec // demo-only server, no need for timeouts
		log.Fatal(err)
	}
}

func handleJWKS(pub *rsa.PublicKey) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(bigEndianExponent(pub.E)),
			}},
		})
	}
}

// handleToken mints a token for the demo scripts: GET /token?sub=alice
// signs a short-lived JWT with the same key served over JWKS above.
func handleToken(key *rsa.PrivateKey, issuer, audience string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Query().Get("sub")
		if sub == "" {
			sub = "demo-user"
		}
		now := time.Now()
		claims := jwt.RegisteredClaims{
			Subject:   sub,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = kid
		signed, err := token.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, signed)
	}
}

func bigEndianExponent(e int) []byte {
	b := []byte{byte(e >> 16), byte(e >> 8), byte(e)}
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return b[i:]
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
