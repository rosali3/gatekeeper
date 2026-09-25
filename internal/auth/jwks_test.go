package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rsaJWK(t *testing.T, kid string) (*rsa.PrivateKey, jwk) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return key, jwk{
		Kty: "RSA",
		Kid: kid,
		N:   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big64(key.PublicKey.E)),
	}
}

func ecJWK(t *testing.T, kid string) (*ecdsa.PrivateKey, jwk) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return key, jwk{
		Kty: "EC",
		Kid: kid,
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(key.PublicKey.X.Bytes()),
		Y:   base64.RawURLEncoding.EncodeToString(key.PublicKey.Y.Bytes()),
	}
}

// big64 encodes a small int (RSA's public exponent) as minimal big-endian
// bytes, the way a real JWK's "e" field is encoded.
func big64(e int) []byte {
	b := []byte{byte(e >> 16), byte(e >> 8), byte(e)}
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return b[i:]
}

// jwksServer serves a mutable JWKS document and counts requests, so tests
// can assert on caching/refresh behavior and simulate key rotation.
type jwksServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	keys  []jwk
	calls atomic.Int32
}

func newJWKSServer(t *testing.T, keys ...jwk) *jwksServer {
	t.Helper()
	s := &jwksServer{keys: keys}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.mu.Lock()
		resp := jwksResponse{Keys: s.keys}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jwksServer) setKeys(keys ...jwk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func TestJWKSClient_GetKey_RSA(t *testing.T) {
	_, k := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Minute)

	key, err := client.getKey(context.Background(), "kid-1")
	if err != nil {
		t.Fatalf("getKey: unexpected error: %v", err)
	}
	if _, ok := key.(*rsa.PublicKey); !ok {
		t.Errorf("getKey returned %T, want *rsa.PublicKey", key)
	}
}

func TestJWKSClient_GetKey_EC(t *testing.T) {
	_, k := ecJWK(t, "kid-ec")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Minute)

	key, err := client.getKey(context.Background(), "kid-ec")
	if err != nil {
		t.Fatalf("getKey: unexpected error: %v", err)
	}
	if _, ok := key.(*ecdsa.PublicKey); !ok {
		t.Errorf("getKey returned %T, want *ecdsa.PublicKey", key)
	}
}

func TestJWKSClient_CachesWithinTTL(t *testing.T) {
	_, k := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Minute)

	for i := 0; i < 5; i++ {
		if _, err := client.getKey(context.Background(), "kid-1"); err != nil {
			t.Fatalf("getKey #%d: unexpected error: %v", i, err)
		}
	}
	if got := srv.calls.Load(); got != 1 {
		t.Errorf("JWKS endpoint called %d times, want 1 (cached)", got)
	}
}

func TestJWKSClient_UnknownKidTriggersImmediateRefresh(t *testing.T) {
	_, k1 := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k1)
	client := NewJWKSClient(srv.srv.URL, time.Hour) // long TTL - refresh must be from the unknown kid, not expiry

	if _, err := client.getKey(context.Background(), "kid-1"); err != nil {
		t.Fatalf("getKey(kid-1): unexpected error: %v", err)
	}
	if got := srv.calls.Load(); got != 1 {
		t.Fatalf("calls after first getKey = %d, want 1", got)
	}

	// Simulate the signer rotating in a new key.
	_, k2 := rsaJWK(t, "kid-2")
	srv.setKeys(k1, k2)

	if _, err := client.getKey(context.Background(), "kid-2"); err != nil {
		t.Fatalf("getKey(kid-2): unexpected error: %v", err)
	}
	if got := srv.calls.Load(); got != 2 {
		t.Errorf("calls after unknown-kid lookup = %d, want 2 (should have refreshed)", got)
	}
}

func TestJWKSClient_ExpiredCacheTriggersRefresh(t *testing.T) {
	_, k := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Millisecond)

	if _, err := client.getKey(context.Background(), "kid-1"); err != nil {
		t.Fatalf("getKey #1: unexpected error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := client.getKey(context.Background(), "kid-1"); err != nil {
		t.Fatalf("getKey #2: unexpected error: %v", err)
	}
	if got := srv.calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (second call should refresh after TTL expiry)", got)
	}
}

func TestJWKSClient_StillUnknownAfterRefreshIsAnError(t *testing.T) {
	_, k := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Hour)

	if _, err := client.getKey(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("getKey: expected an error for a kid that's unknown even after refresh")
	}
}

func TestJWKSClient_FetchFailureIsAnError(t *testing.T) {
	client := NewJWKSClient("http://127.0.0.1:1", time.Minute)
	if _, err := client.getKey(context.Background(), "kid-1"); err == nil {
		t.Fatal("getKey: expected an error when the JWKS endpoint is unreachable")
	}
}

func TestJWKSClient_UnsupportedKeyTypeIsSkippedNotFatal(t *testing.T) {
	_, good := rsaJWK(t, "kid-good")
	bad := jwk{Kty: "oct", Kid: "kid-bad"}
	srv := newJWKSServer(t, good, bad)
	client := NewJWKSClient(srv.srv.URL, time.Minute)

	if _, err := client.getKey(context.Background(), "kid-good"); err != nil {
		t.Fatalf("getKey(kid-good): unexpected error: %v", err)
	}
	if _, err := client.getKey(context.Background(), "kid-bad"); err == nil {
		t.Error("getKey(kid-bad): expected an error - unsupported key types should be skipped, not usable")
	}
}

func TestJWKSClient_ConcurrentRefreshOnlyFetchesOnce(t *testing.T) {
	_, k := rsaJWK(t, "kid-1")
	srv := newJWKSServer(t, k)
	client := NewJWKSClient(srv.srv.URL, time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.getKey(context.Background(), "kid-1"); err != nil {
				t.Errorf("getKey: unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := srv.calls.Load(); got != 1 {
		t.Errorf("JWKS endpoint called %d times concurrently, want 1 (refreshMu should serialize fetches)", got)
	}
}
