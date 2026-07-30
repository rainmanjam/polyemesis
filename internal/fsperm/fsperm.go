// Package fsperm restricts access to files and directories holding secrets.
//
// It exists because Go's os.FileMode is a Unix concept and Windows silently
// discards almost all of it. os.MkdirAll(dir, 0o700) and os.WriteFile(key,
// b, 0o600) both compile and both succeed on Windows, and neither restricts
// anything: the Windows syscall layer maps a FileMode to a single read-only
// attribute, so the object simply inherits its parent's ACL. Under the default
// ACL on C:\ that includes BUILTIN\Users.
//
// polyemesis writes TLS private keys -- a self-signed CA key, the server key,
// and autocert's ACME account key and issued certificates. On Linux the 0700
// directory and 0600 files are what keeps those from every other account on the
// host. Before this package the Windows build had the same two lines of code,
// the same green tests, and none of the protection.
//
// The API is deliberately about INTENT rather than about mode bits, because
// there is no mode bit on one of the platforms:
//
//	SecureDir  -- this directory holds secrets; only this account may enter it.
//	SecureFile -- this file is secret; only this account may read it.
//
// Each platform then implements that intent with whatever its own access
// control actually is.
package fsperm

// SecureDir creates path if it does not exist and restricts it to the account
// running the process.
//
// Applied to an existing directory too, not just a freshly created one: an
// operator who restored a backup, or who ran an older build, has a directory
// whose permissions were never set. Enforcing on every startup is what makes
// the guarantee true of deployments that predate it.
//
// Files created inside afterwards are covered as well. That is load-bearing
// rather than incidental -- autocert writes the ACME account key itself, with
// its own permissions, and we never see the call. Inheritance is the only way
// that key is protected.
func SecureDir(path string) error { return secureDir(path) }

// SecureFile restricts an existing file to the account running the process.
//
// Call it AFTER writing, and prefer writing to a 0600 temporary file and
// renaming, so the bytes are never briefly readable. internal/tlsx already
// does that; this closes the Windows half of it.
func SecureFile(path string) error { return secureFile(path) }

// CheckPrivate reports nil when nothing but the account running the process can
// reach path, and otherwise an error naming what else can.
//
// Exported so that callers ASSERT the property rather than the mechanism.
// A test that checks for mode 0700 is really checking "Unix", and that is how
// polyemesis shipped a Windows build whose TLS key directory was open to every
// local account while three separate tests reported it as protected.
func CheckPrivate(path string) error { return checkPrivate(path) }
