package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "gatekeeper-test"
	testAudience = "gatekeeper-clients"
)

func signRS256(t *testing.T, key interface{}, kid string, claims jwt.RegisteredClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}

func validClaims() jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Subject:   "user-123",
		Issuer:    testIssuer,
		Audience:  jwt.ClaimStrings{testAudience},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
	}
}

func newVerifier(t *testing.T, keys ...jwk) *JWTVerifier {
	t.Helper()
	srv := newJWKSServer(t, keys...)
	jwks := NewJWKSClient(srv.srv.URL, time.Minute)
	return NewJWTVerifier(jwks, testIssuer, testAudience)
}

func TestJWTVerifier_ValidToken(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	token := signRS256(t, priv, "kid-1", validClaims())
	claims, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "user-123")
	}
}

func TestJWTVerifier_ExpiredToken(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	claims := validClaims()
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	token := signRS256(t, priv, "kid-1", claims)

	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error for an expired token")
	}
}

func TestJWTVerifier_NotYetValidToken(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	claims := validClaims()
	claims.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
	token := signRS256(t, priv, "kid-1", claims)

	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error for a not-yet-valid (nbf) token")
	}
}

func TestJWTVerifier_WrongIssuer(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	claims := validClaims()
	claims.Issuer = "someone-else"
	token := signRS256(t, priv, "kid-1", claims)

	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error for the wrong issuer")
	}
}

func TestJWTVerifier_WrongAudience(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	claims := validClaims()
	claims.Audience = jwt.ClaimStrings{"someone-else"}
	token := signRS256(t, priv, "kid-1", claims)

	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error for the wrong audience")
	}
}

func TestJWTVerifier_UnknownKid(t *testing.T) {
	priv, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	token := signRS256(t, priv, "kid-does-not-exist", validClaims())
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error for an unknown kid")
	}
}

func TestJWTVerifier_WrongSigningKey(t *testing.T) {
	_, pub := rsaJWK(t, "kid-1")
	otherPriv, _ := rsaJWK(t, "kid-1") // a different key pair, same kid
	v := newVerifier(t, pub)

	token := signRS256(t, otherPriv, "kid-1", validClaims())
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify: expected an error - token signed with a key that doesn't match the published one")
	}
}

// TestJWTVerifier_RejectsAlgNone is the actual point of WithValidMethods:
// a token that declares "alg":"none" (and is therefore trivially
// "signed" by anyone) must be rejected before signature verification
// even matters.
func TestJWTVerifier_RejectsAlgNone(t *testing.T) {
	_, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	token := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
	token.Header["kid"] = "kid-1"
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	if _, err := v.Verify(context.Background(), signed); err == nil {
		t.Fatal("Verify: expected alg:none to be rejected")
	}
}

func TestJWTVerifier_MalformedToken(t *testing.T) {
	_, pub := rsaJWK(t, "kid-1")
	v := newVerifier(t, pub)

	if _, err := v.Verify(context.Background(), "not-a-jwt-at-all"); err == nil {
		t.Fatal("Verify: expected an error for a malformed token")
	}
}
