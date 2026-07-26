// Package secrets encrypts OAuth tokens and client secrets at rest with NaCl
// secretbox (XSalsa20-Poly1305).
//
// The key lives in a 0600 file in the data directory, generated on first run.
// It is deliberately NOT derived from the admin password: the server must be
// able to refresh OAuth tokens while nobody is logged in, so it needs the key
// at rest anyway. Deriving from the password would be security theatre that
// also breaks unattended restarts. What this does buy you is that a leaked
// database file — a backup, a snapshot, an errant scp — is not a leaked set of
// live streaming credentials.
package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/nacl/secretbox"
)

const (
	keySize   = 32
	nonceSize = 24
)

// ErrDecrypt is returned when a ciphertext fails authentication — a wrong key
// or a tampered database.
var ErrDecrypt = errors.New("secretbox: decryption failed (wrong key or corrupted data)")

// Box seals and opens secrets.
type Box struct {
	key [keySize]byte
}

// LoadOrCreate reads the key file, generating one if it does not exist.
func LoadOrCreate(path string) (*Box, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		raw, err := hex.DecodeString(string(trimSpace(b)))
		if err != nil {
			return nil, fmt.Errorf("secret key %s is not valid hex: %w", path, err)
		}
		if len(raw) != keySize {
			return nil, fmt.Errorf("secret key %s is %d bytes, want %d", path, len(raw), keySize)
		}
		box := &Box{}
		copy(box.key[:], raw)
		return box, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read secret key %s: %w", path, err)
	}

	box := &Box{}
	if _, err := io.ReadFull(rand.Reader, box.key[:]); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(box.key[:])), 0o600); err != nil {
		return nil, fmt.Errorf("write secret key %s: %w", path, err)
	}
	return box, nil
}

// New builds a Box from raw key material. Used by tests.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), keySize)
	}
	b := &Box{}
	copy(b.key[:], key)
	return b, nil
}

// Seal encrypts plaintext. The nonce is random per call and prefixed to the
// ciphertext, so encrypting the same token twice yields different bytes.
func (b *Box) Seal(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	var nonce [nonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, err
	}
	return secretbox.Seal(nonce[:], []byte(plaintext), &nonce, &b.key), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if len(ciphertext) < nonceSize+secretbox.Overhead {
		return "", ErrDecrypt
	}
	var nonce [nonceSize]byte
	copy(nonce[:], ciphertext[:nonceSize])
	out, ok := secretbox.Open(nil, ciphertext[nonceSize:], &nonce, &b.key)
	if !ok {
		return "", ErrDecrypt
	}
	return string(out), nil
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
