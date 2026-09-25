package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

// parseJWK converts one JWK entry into a Go public key. JWKS parsing is
// hand-rolled rather than pulled from a library, per the TZ - it's plain
// JSON plus base64url big-integer decoding, not worth a dependency.
func parseJWK(k jwk) (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("decoding n: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("decoding e: %w", err)
		}
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}, nil

	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("decoding x: %w", err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("decoding y: %w", err)
		}
		return &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

// JWKSClient fetches and caches a JSON Web Key Set. Keys are cached for
// cacheTTL; a kid not found in the cache triggers an immediate refresh
// (the signer may have rotated keys), not just a wait for the next TTL
// expiry.
type JWKSClient struct {
	url        string
	cacheTTL   time.Duration
	httpClient *http.Client

	// mu guards keys/lastFetch/generation, read on every token
	// verification, so it's an RWMutex; refreshMu serializes the actual
	// HTTP fetch so concurrent requests for the same unknown/expired kid
	// don't all hit the JWKS endpoint at once - two locks because the two
	// operations they protect (a map read, an HTTP round trip) have very
	// different costs and concurrency needs.
	//
	// generation increments on every completed fetch. A caller that
	// decides it needs a refresh captures the generation it observed
	// beforehand; once it gets refreshMu, if the generation has already
	// moved on, someone else's fetch (started after its own decision
	// point) already happened and there's nothing left to do. This is
	// what actually collapses a burst of concurrent callers into one
	// fetch - a plain "skip if refreshed within the last N ms" debounce
	// doesn't work here, since a genuine key rotation looked up moments
	// after a prior, unrelated refresh would then be wrongly skipped too.
	mu         sync.RWMutex
	keys       map[string]crypto.PublicKey
	lastFetch  time.Time
	generation int64

	refreshMu sync.Mutex
}

func NewJWKSClient(url string, cacheTTL time.Duration) *JWKSClient {
	return &JWKSClient{
		url:        url,
		cacheTTL:   cacheTTL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		keys:       make(map[string]crypto.PublicKey),
	}
}

func (c *JWKSClient) getKey(ctx context.Context, kid string) (crypto.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	fresh := time.Since(c.lastFetch) < c.cacheTTL
	observedGen := c.generation
	c.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}

	if err := c.refresh(ctx, observedGen); err != nil {
		return nil, fmt.Errorf("jwks: refresh: %w", err)
	}

	c.mu.RLock()
	key, ok = c.keys[kid]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("jwks: unknown kid %q", kid)
	}
	return key, nil
}

// refresh fetches fresh keys, unless a fetch already completed after
// observedGen was captured (see the generation field's doc comment).
func (c *JWKSClient) refresh(ctx context.Context, observedGen int64) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	currentGen := c.generation
	c.mu.RUnlock()
	if currentGen != observedGen {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch: unexpected status %d", resp.StatusCode)
	}

	var parsed jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	newKeys := make(map[string]crypto.PublicKey, len(parsed.Keys))
	for _, k := range parsed.Keys {
		pub, err := parseJWK(k)
		if err != nil {
			continue // skip keys we don't support (e.g. "kty":"oct") rather than failing the whole fetch
		}
		newKeys[k.Kid] = pub
	}

	c.mu.Lock()
	c.keys = newKeys
	c.lastFetch = time.Now()
	c.generation++
	c.mu.Unlock()
	return nil
}
