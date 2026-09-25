package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func hashOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func writeKeysFile(t *testing.T, entries ...APIKeyEntry) string {
	t.Helper()
	f := apiKeysFile{Keys: entries}
	// Build the YAML by hand to avoid importing yaml.v3 twice in the test
	// just to marshal what LoadAPIKeys will unmarshal anyway.
	path := filepath.Join(t.TempDir(), "keys.yaml")
	var buf []byte
	buf = append(buf, "keys:\n"...)
	for _, e := range f.Keys {
		buf = append(buf, []byte("  - name: "+e.Name+"\n    hash: "+e.Hash+"\n")...)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadAPIKeys_VerifyValidKey(t *testing.T) {
	path := writeKeysFile(t, APIKeyEntry{Name: "team-a", Hash: hashOf("secret-key-1")})

	store, err := LoadAPIKeys(path)
	if err != nil {
		t.Fatalf("LoadAPIKeys: unexpected error: %v", err)
	}

	name, ok := store.Verify("secret-key-1")
	if !ok {
		t.Fatal("Verify: expected the valid key to match")
	}
	if name != "team-a" {
		t.Errorf("name = %q, want %q", name, "team-a")
	}
}

func TestAPIKeyStore_RejectsUnknownKey(t *testing.T) {
	path := writeKeysFile(t, APIKeyEntry{Name: "team-a", Hash: hashOf("secret-key-1")})
	store, err := LoadAPIKeys(path)
	if err != nil {
		t.Fatalf("LoadAPIKeys: unexpected error: %v", err)
	}

	if _, ok := store.Verify("wrong-key"); ok {
		t.Error("Verify: expected an unknown key to be rejected")
	}
}

func TestAPIKeyStore_RejectsEmptyKey(t *testing.T) {
	path := writeKeysFile(t, APIKeyEntry{Name: "team-a", Hash: hashOf("secret-key-1")})
	store, err := LoadAPIKeys(path)
	if err != nil {
		t.Fatalf("LoadAPIKeys: unexpected error: %v", err)
	}

	if _, ok := store.Verify(""); ok {
		t.Error("Verify: expected an empty key to be rejected")
	}
}

func TestAPIKeyStore_MultipleKeysEachMatchTheirOwn(t *testing.T) {
	path := writeKeysFile(t,
		APIKeyEntry{Name: "team-a", Hash: hashOf("key-a")},
		APIKeyEntry{Name: "team-b", Hash: hashOf("key-b")},
	)
	store, err := LoadAPIKeys(path)
	if err != nil {
		t.Fatalf("LoadAPIKeys: unexpected error: %v", err)
	}

	if name, ok := store.Verify("key-a"); !ok || name != "team-a" {
		t.Errorf("Verify(key-a) = %q, %v, want team-a, true", name, ok)
	}
	if name, ok := store.Verify("key-b"); !ok || name != "team-b" {
		t.Errorf("Verify(key-b) = %q, %v, want team-b, true", name, ok)
	}
}

func TestLoadAPIKeys_FileNotFound(t *testing.T) {
	if _, err := LoadAPIKeys(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("LoadAPIKeys: expected an error for a missing file")
	}
}

func TestLoadAPIKeys_InvalidHashHex(t *testing.T) {
	path := writeKeysFile(t, APIKeyEntry{Name: "bad", Hash: "not-hex!!"})
	if _, err := LoadAPIKeys(path); err == nil {
		t.Fatal("LoadAPIKeys: expected an error for a non-hex hash")
	}
}

func TestAPIKeyStore_EmptyStoreRejectsEverything(t *testing.T) {
	path := writeKeysFile(t)
	store, err := LoadAPIKeys(path)
	if err != nil {
		t.Fatalf("LoadAPIKeys: unexpected error: %v", err)
	}
	if _, ok := store.Verify("anything"); ok {
		t.Error("Verify: an empty store should reject every key")
	}
}
