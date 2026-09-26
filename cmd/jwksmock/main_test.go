package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestHandleJWKS_ServesTheGivenKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	rec := httptest.NewRecorder()
	handleJWKS(&key.PublicKey)(rec, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))

	var body struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(body.Keys) != 1 || body.Keys[0]["kid"] != kid || body.Keys[0]["kty"] != "RSA" {
		t.Fatalf("keys = %+v, want one RSA key with kid %q", body.Keys, kid)
	}
}

func TestHandleToken_IssuesAVerifiableToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/token?sub=alice", nil)
	handleToken(key, "test-issuer", "test-audience")(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	tokenString := rec.Body.String()

	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("test-issuer"), jwt.WithAudience("test-audience"))
	if err != nil {
		t.Fatalf("issued token did not verify: %v", err)
	}
	if claims.Subject != "alice" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "alice")
	}
}

func TestHandleToken_DefaultsSubjectWhenMissing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	rec := httptest.NewRecorder()
	handleToken(key, "test-issuer", "test-audience")(rec, httptest.NewRequest(http.MethodGet, "/token", nil))

	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(rec.Body.String(), claims, func(token *jwt.Token) (interface{}, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("token did not parse: %v", err)
	}
	if claims.Subject != "demo-user" {
		t.Errorf("Subject = %q, want default %q", claims.Subject, "demo-user")
	}
}
