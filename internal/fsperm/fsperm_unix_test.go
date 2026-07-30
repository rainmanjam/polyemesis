//go:build !windows

package fsperm

import (
	"os"
	"testing"
)

func assertPrivateDir(t *testing.T, path string) {
	t.Helper()
	if err := CheckPrivate(path); err != nil {
		t.Errorf("directory is not private: %v", err)
	}
	// The exact mode as well as the property, because on Unix 0700 is the
	// documented deployment contract (config.go says so, and operators read
	// it). CheckPrivate would accept 0500, which is private but is not what
	// the server needs to write into.
	assertMode(t, path, 0o700)
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	// Private EITHER because the file denies other accounts itself, OR because
	// it sits in a directory nobody else can traverse. On Unix both are real
	// protection, and each case in this package relies on a different one:
	// SecureFile chmods the key to 0600, while autocert's account key keeps
	// whatever mode autocert gave it and is protected only by the 0700
	// directory around it. Asserting either mechanism alone fails a case that
	// is genuinely safe.
	if err := CheckPrivate(path); err == nil {
		return
	} else if dirErr := CheckPrivate(dirOf(path)); dirErr != nil {
		t.Errorf("%s is reachable by other accounts (%v) and so is the directory "+
			"holding it (%v)", path, err, dirErr)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s is mode %04o, want %04o", path, got, want)
	}
}

// loosen makes an object as public as the platform allows, so a test that then
// calls Secure* is measuring the call rather than the default.
func loosen(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	// Guard the guard, matching the Windows helper: if this did not actually
	// make the object public, the assertion that follows proves nothing.
	if err := CheckPrivate(path); err == nil {
		t.Fatalf("loosen(%s) left the object private", path)
	}
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
