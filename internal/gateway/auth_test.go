package gateway

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"gatekeeper/internal/config"
)

func writeTestKeysFile(t *testing.T, rawKey, name string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(rawKey))
	path := filepath.Join(t.TempDir(), "keys.yaml")
	content := "keys:\n  - name: " + name + "\n    hash: " + hex.EncodeToString(sum[:]) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func configWithAuth(upstreamURL, authMode string) *config.Config {
	cfg := baseConfig(upstreamURL)
	route := cfg.Routes[0]
	route.Auth = authMode
	cfg.Routes[0] = route
	return cfg
}

func TestGateway_AuthAPIKey_ValidKeyAllowed(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithAuth(srv.URL, config.AuthAPIKey)
	cfg.Auth.APIKeysFile = writeTestKeysFile(t, "correct-key", "client-a")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.Header.Set("X-API-Key", "correct-key")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestGateway_AuthAPIKey_MissingKeyRejected(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithAuth(srv.URL, config.AuthAPIKey)
	cfg.Auth.APIKeysFile = writeTestKeysFile(t, "correct-key", "client-a")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGateway_AuthAPIKey_WrongKeyRejected(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithAuth(srv.URL, config.AuthAPIKey)
	cfg.Auth.APIKeysFile = writeTestKeysFile(t, "correct-key", "client-a")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// jwksTestServer serves a single RSA public key as a JWKS document, for
// gateway-level JWT auth tests. Signature/claims edge cases themselves are
// already covered by internal/auth's own tests - this just needs to prove
// the wiring (route -> authenticate -> JWTVerifier -> X-User-ID) works.
func jwksTestServer(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	type jwkOut struct {
		Kty, Kid, N, E string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eBytes := big64(pub.E)
		resp := map[string]any{
			"keys": []jwkOut{{
				Kty: "RSA",
				Kid: kid,
				N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(eBytes),
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func big64(e int) []byte {
	b := []byte{byte(e >> 16), byte(e >> 8), byte(e)}
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return b[i:]
}

func configWithJWT(upstreamURL, jwksURL, issuer, audience string) *config.Config {
	cfg := configWithAuth(upstreamURL, config.AuthJWT)
	cfg.Auth.JWT = config.JWTConfig{
		JWKSURL:  jwksURL,
		Issuer:   issuer,
		Audience: audience,
		CacheTTL: config.Duration(time.Minute),
	}
	return cfg
}

func signedTestJWT(t *testing.T, priv *rsa.PrivateKey, kid, issuer, audience, subject string, expired bool) string {
	t.Helper()
	now := time.Now()
	exp := now.Add(time.Hour)
	if expired {
		exp = now.Add(-time.Hour)
	}
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings{audience},
		ExpiresAt: jwt.NewNumericDate(exp),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}

func TestGateway_AuthJWT_ValidTokenAllowedAndSetsUserID(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	jwks := jwksTestServer(t, "kid-1", &priv.PublicKey)

	srv := backend(t, "demo-backend")
	cfg := configWithJWT(srv.URL, jwks.URL, "test-issuer", "test-audience")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	token := signedTestJWT(t, priv, "kid-1", "test-issuer", "test-audience", "user-42", false)
	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Received-User-ID"); got != "user-42" {
		t.Errorf("upstream saw X-User-ID = %q, want %q", got, "user-42")
	}
}

func TestGateway_AuthJWT_MissingTokenRejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	jwks := jwksTestServer(t, "kid-1", &priv.PublicKey)

	srv := backend(t, "demo-backend")
	cfg := configWithJWT(srv.URL, jwks.URL, "test-issuer", "test-audience")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGateway_AuthJWT_ExpiredTokenRejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	jwks := jwksTestServer(t, "kid-1", &priv.PublicKey)

	srv := backend(t, "demo-backend")
	cfg := configWithJWT(srv.URL, jwks.URL, "test-issuer", "test-audience")

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	token := signedTestJWT(t, priv, "kid-1", "test-issuer", "test-audience", "user-42", true)
	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGateway_AuthNone_NoCredentialsNeeded(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithAuth(srv.URL, config.AuthNone)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
