// Package auth implements gatekeeper's two auth modes: API keys compared
// as SHA-256 hashes in constant time, and JWTs verified against a JWKS
// endpoint with a fixed algorithm allow-list.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry is one line of the api_keys_file: a caller's name and the
// SHA-256 hash (hex) of their key. The raw key itself is never stored,
// per the TZ ("хранятся хэши ключей, не сами ключи").
type APIKeyEntry struct {
	Name string `yaml:"name"`
	Hash string `yaml:"hash"`
}

type apiKeysFile struct {
	Keys []APIKeyEntry `yaml:"keys"`
}

type storedKey struct {
	name string
	hash []byte
}

// APIKeyStore holds the loaded set of valid API key hashes.
type APIKeyStore struct {
	keys []storedKey
}

// LoadAPIKeys reads and parses an api_keys_file.
func LoadAPIKeys(path string) (*APIKeyStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("api keys file %q: %w", path, err)
	}

	var parsed apiKeysFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("api keys file %q: %w", path, err)
	}

	keys := make([]storedKey, 0, len(parsed.Keys))
	for i, e := range parsed.Keys {
		h, err := hex.DecodeString(e.Hash)
		if err != nil {
			return nil, fmt.Errorf("api keys file %q: keys[%d].hash: %w", path, i, err)
		}
		keys = append(keys, storedKey{name: e.Name, hash: h})
	}
	return &APIKeyStore{keys: keys}, nil
}

// Verify hashes rawKey and compares it against every stored hash using
// subtle.ConstantTimeCompare, always scanning the whole list rather than
// returning on the first match - so the time Verify takes depends only on
// how many keys are registered, never on which one (if any) matched or
// where it sits in the list.
func (s *APIKeyStore) Verify(rawKey string) (name string, ok bool) {
	sum := sha256.Sum256([]byte(rawKey))

	var matched int
	for _, k := range s.keys {
		// ConstantTimeCompare itself requires equal-length inputs (it
		// returns 0 immediately otherwise, which is fine: both sides are
		// fixed-size SHA-256 sums here in the only path that matters, and
		// a malformed stored hash of the wrong length just never matches).
		eq := subtle.ConstantTimeCompare(sum[:], k.hash)
		if eq == 1 {
			matched = 1
			name = k.name
		}
	}
	return name, matched == 1
}
