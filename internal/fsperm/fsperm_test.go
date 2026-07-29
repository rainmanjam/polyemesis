package fsperm

import (
	"os"
	"path/filepath"
	"testing"
)

// The behaviour asserted here is identical on every platform; only the way you
// OBSERVE it differs, so assertPrivateDir/assertPrivateFile/loosen live in the
// per-platform test files. That split is deliberate: it keeps the guarantee
// stated once, so neither platform can quietly promise less than the other.

func TestSecureDirCreatesAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := SecureDir(dir); err != nil {
		t.Fatalf("SecureDir: %v", err)
	}
	assertPrivateDir(t, dir)
}

// The upgrade path, and the reason SecureDir does not stop at MkdirAll.
//
// An operator running a build that predates this, or restoring a backup, has a
// key directory that was never restricted. MkdirAll does nothing whatsoever to
// a directory that already exists, so a create-only implementation leaves every
// existing deployment exactly as exposed as it was.
func TestSecureDirTightensADirectoryThatAlreadyExists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	loosen(t, dir)

	if err := SecureDir(dir); err != nil {
		t.Fatalf("SecureDir: %v", err)
	}
	assertPrivateDir(t, dir)
}

func TestSecureFileRestrictsAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(path, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loosen(t, path)

	if err := SecureFile(path); err != nil {
		t.Fatalf("SecureFile: %v", err)
	}
	assertPrivateFile(t, path)
}

// The autocert case, and the one a Unix-shaped mental model gets wrong.
//
// autocert writes the ACME account key and every issued certificate itself,
// through a code path polyemesis never calls, so it can never be handed to
// SecureFile. On Unix the 0700 directory is enough -- nobody can traverse in.
// On Windows a directory ACL restricts the DIRECTORY and says nothing about
// objects created inside it unless the ACEs are marked inheritable.
//
// So this test is the only thing standing between "the ACME account key is
// protected" and "the ACME account key looked protected".
func TestAFileCreatedInsideASecuredDirectoryIsAlsoPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	if err := SecureDir(dir); err != nil {
		t.Fatalf("SecureDir: %v", err)
	}
	// Written AFTER the directory was secured, exactly as autocert does, and
	// with a deliberately permissive mode so that any protection observed comes
	// from the directory rather than from this call.
	inner := filepath.Join(dir, "acme_account+key")
	if err := os.WriteFile(inner, []byte("account key"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, inner)
}

func TestSecureDirReportsAPathItCannotCreate(t *testing.T) {
	// A file where a directory should be: MkdirAll cannot proceed, and the
	// error has to surface rather than leave an unprotected path behind.
	base := t.TempDir()
	blocker := filepath.Join(base, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SecureDir(filepath.Join(blocker, "tls")); err == nil {
		t.Error("SecureDir reported success for a path it could not create")
	}
}
