package secrets

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This package protects every OAuth token and client secret in an install, and
// until now it had no tests at all. The failure modes are asymmetric: a Seal
// that is subtly wrong leaks live streaming credentials to anyone holding a
// database backup, and an Open that is subtly wrong makes every connected
// account unrecoverable with no way back. Both deserve to be pinned.

func testBox(t *testing.T) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{0x2b}, keySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := testBox(t)
	for _, want := range []string{
		"a",
		"ya29.a0AfB_byC-realistic-looking-oauth-access-token",
		strings.Repeat("long", 4096),
		"unicode: 日本語 \U0001f510",
		"\x00embedded\x00nulls\x00",
	} {
		sealed, err := b.Seal(want)
		if err != nil {
			t.Fatalf("Seal(%.20q): %v", want, err)
		}
		got, err := b.Open(sealed)
		if err != nil {
			t.Fatalf("Open(%.20q): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip changed the value: got %.40q, want %.40q", got, want)
		}
	}
}

func TestSealNeverEmitsThePlaintext(t *testing.T) {
	b := testBox(t)
	// The whole point is that a leaked database is not a leaked credential.
	const token = "ya29.SUPER-SECRET-REFRESH-TOKEN"
	sealed, err := b.Seal(token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, []byte(token)) {
		t.Fatal("the ciphertext contains the plaintext verbatim")
	}
	// Not even a recognisable fragment.
	if bytes.Contains(sealed, []byte("SUPER-SECRET")) {
		t.Fatal("the ciphertext contains a plaintext fragment")
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	b := testBox(t)
	const token = "the same token twice"
	// A deterministic seal would let anyone with the database tell that two
	// accounts share a credential, and would leak when one stops changing.
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		sealed, err := b.Seal(token)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		k := hex.EncodeToString(sealed)
		if seen[k] {
			t.Fatal("two seals of the same plaintext produced identical bytes: the nonce is not random")
		}
		seen[k] = true
	}
}

func TestSealUsesAFreshNonceEveryTime(t *testing.T) {
	b := testBox(t)
	// Reusing a nonce with the same key is the one catastrophic mistake in
	// XSalsa20-Poly1305: it leaks the XOR of two plaintexts and forfeits
	// authentication. Check the nonce prefix specifically, not just the whole
	// ciphertext, so this fails for the right reason.
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		sealed, err := b.Seal("x")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		nonce := hex.EncodeToString(sealed[:nonceSize])
		if seen[nonce] {
			t.Fatalf("nonce reused after %d seals", i)
		}
		seen[nonce] = true
	}
}

func TestOpenRejectsTheWrongKey(t *testing.T) {
	a := testBox(t)
	other, err := New(bytes.Repeat([]byte{0x7c}, keySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := a.Seal("a token")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a ciphertext opened under the wrong key")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	b := testBox(t)
	sealed, err := b.Seal("a token worth forging")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Every byte matters: nonce, ciphertext body and Poly1305 tag alike. A flip
	// anywhere must fail authentication rather than yield altered plaintext,
	// which is the difference between a corrupted row being noticed and a
	// forged token being used.
	for i := range sealed {
		tampered := append([]byte(nil), sealed...)
		tampered[i] ^= 0x01
		if _, err := b.Open(tampered); err == nil {
			t.Fatalf("a ciphertext with byte %d flipped opened successfully", i)
		}
	}
}

func TestOpenRejectsTruncatedCiphertext(t *testing.T) {
	b := testBox(t)
	sealed, err := b.Seal("a token")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// A truncated value must be an error, never a panic: this data comes from a
	// database column that a backup restore or a partial write can corrupt.
	for n := 1; n < len(sealed); n++ {
		if _, err := b.Open(sealed[:n]); err == nil {
			t.Fatalf("a ciphertext truncated to %d bytes opened successfully", n)
		}
	}
}

func TestEmptyRoundTripsAsEmptyRatherThanFailing(t *testing.T) {
	b := testBox(t)
	// A nil refresh token is normal — several platforms do not issue one — so
	// the empty case must be a value, not an error, or every account without
	// one fails to load.
	sealed, err := b.Seal("")
	if err != nil {
		t.Fatalf("Seal(\"\"): %v", err)
	}
	if len(sealed) != 0 {
		t.Errorf("Seal(\"\") produced %d bytes, want none", len(sealed))
	}
	got, err := b.Open(nil)
	if err != nil {
		t.Fatalf("Open(nil): %v", err)
	}
	if got != "" {
		t.Errorf("Open(nil) = %q, want empty", got)
	}
}

func TestDeriveIsDeterministicAndSeparatedByLabel(t *testing.T) {
	b := testBox(t)
	// The JWT signing key comes from here. It has to survive a restart
	// unchanged, or every session cookie is invalidated whenever the process
	// bounces.
	if !bytes.Equal(b.Derive("jwt"), b.Derive("jwt")) {
		t.Fatal("Derive is not deterministic; a restart would log everyone out")
	}
	// And the two uses of the master key must not share bytes.
	if bytes.Equal(b.Derive("jwt"), b.Derive("other")) {
		t.Fatal("two labels derived the same subkey")
	}
	if bytes.Equal(b.Derive("jwt"), b.key[:]) {
		t.Fatal("a derived subkey equals the master key")
	}
	if n := len(b.Derive("jwt")); n != 32 {
		t.Errorf("derived subkey is %d bytes, want 32", n)
	}
}

func TestDeriveDiffersBetweenKeys(t *testing.T) {
	a := testBox(t)
	other, err := New(bytes.Repeat([]byte{0x11}, keySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if bytes.Equal(a.Derive("jwt"), other.Derive("jwt")) {
		t.Fatal("two different master keys derived the same subkey")
	}
}

func TestNewRejectsAKeyOfTheWrongLength(t *testing.T) {
	for _, n := range []int{0, 1, keySize - 1, keySize + 1, 64} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New accepted a %d-byte key", n)
		}
	}
}

func TestLoadOrCreateGeneratesThenReusesTheSameKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "secret.key")

	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate (create): %v", err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate (reuse): %v", err)
	}
	// Re-reading must give the same key, or every stored token becomes
	// unreadable the next time the server starts.
	if first.key != second.key {
		t.Fatal("a second LoadOrCreate produced a different key; all stored tokens would be lost")
	}
	sealed, err := first.Seal("survives a restart")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := second.Open(sealed)
	if err != nil || got != "survives a restart" {
		t.Fatalf("Open across instances = %q, %v", got, err)
	}
}

func TestLoadOrCreateWritesAPrivateKeyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes do not apply on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The key at rest is the whole security boundary: anyone who can read this
	// file can decrypt every credential in the database.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode is %04o, want 0600", perm)
	}
}

func TestLoadOrCreateToleratesTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	key := bytes.Repeat([]byte{0xab}, keySize)
	// An operator who edits or copies this file by hand will leave a newline,
	// and refusing to start over one would be an unrecoverable install.
	if err := os.WriteFile(path, []byte("\n  "+hex.EncodeToString(key)+"  \n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !bytes.Equal(b.key[:], key) {
		t.Error("whitespace around the key changed the key that was loaded")
	}
}

func TestLoadOrCreateRefusesAMalformedKeyFile(t *testing.T) {
	cases := map[string]string{
		"not hex":   "zzzz-not-hex-at-all",
		"too short": hex.EncodeToString(make([]byte, keySize-1)),
		"too long":  hex.EncodeToString(make([]byte, keySize+1)),
		"empty":     "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret.key")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// Refusing loudly is right here. Silently generating a fresh key
			// would start the server with credentials it cannot decrypt and no
			// indication of why every platform account had stopped working.
			if _, err := LoadOrCreate(path); err == nil {
				t.Errorf("LoadOrCreate accepted a %s key file", name)
			}
		})
	}
}
