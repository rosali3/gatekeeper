package auth

import (
	"context"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// allowedJWTAlgorithms is a fixed allow-list, checked by the parser before
// keyFunc is even called - this is the actual defense against "alg: none"
// and algorithm-confusion attacks (e.g. an attacker resigning a token
// with HS256 using the RSA public key as the HMAC secret). Only
// asymmetric algorithms are listed since keys come from a JWKS (public
// keys only); there's no HS* entry to confuse them with.
var allowedJWTAlgorithms = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}

// JWTVerifier validates JWTs against a JWKS-published key set.
type JWTVerifier struct {
	jwks     *JWKSClient
	issuer   string
	audience string
}

func NewJWTVerifier(jwks *JWKSClient, issuer, audience string) *JWTVerifier {
	return &JWTVerifier{jwks: jwks, issuer: issuer, audience: audience}
}

// Verify checks the token's signature (via JWKS), algorithm, issuer,
// audience, and expiry/not-before, returning its registered claims (the
// caller reads Subject for X-User-ID).
func (v *JWTVerifier) Verify(ctx context.Context, tokenString string) (*jwt.RegisteredClaims, error) {
	claims := &jwt.RegisteredClaims{}
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("token header missing kid")
		}
		return v.jwks.getKey(ctx, kid)
	}

	_, err := jwt.ParseWithClaims(tokenString, claims, keyFunc,
		jwt.WithValidMethods(allowedJWTAlgorithms),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt: %w", err)
	}
	return claims, nil
}
