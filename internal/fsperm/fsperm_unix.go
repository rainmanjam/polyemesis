//go:build !windows

package fsperm

import (
	"fmt"
	"os"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

func secureDir(path string) error {
	if err := os.MkdirAll(path, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	// Chmod as well as MkdirAll, for two reasons that both produce a
	// world-readable key directory:
	//
	//   - MkdirAll applies the process umask, so 0700 requested is 0700 &^
	//     umask granted. A umask is site policy and can be anything.
	//   - MkdirAll does nothing at all to a directory that already exists, so
	//     an upgrade or a restored backup keeps whatever mode it arrived with.
	//
	// Chmod states the mode unconditionally, which is the actual requirement.
	if err := os.Chmod(path, dirPerm); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}

func secureFile(path string) error {
	if err := os.Chmod(path, filePerm); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}

func checkPrivate(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Group and other bits, not an exact mode: 0700 and 0600 are both private,
	// and so is 0500. What matters is that nobody else is named.
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is mode %04o, so it is reachable by %s",
			path, perm, reachableBy(perm))
	}
	return nil
}

func reachableBy(perm os.FileMode) string {
	switch {
	case perm&0o007 != 0 && perm&0o070 != 0:
		return "its group and every other account"
	case perm&0o007 != 0:
		return "every other account"
	default:
		return "its group"
	}
}
