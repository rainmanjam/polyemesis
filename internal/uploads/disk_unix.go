//go:build !windows

package uploads

import "golang.org/x/sys/unix"

// freeBytes reports free bytes on the filesystem holding path.
//
// A near-copy of internal/recording's diskFree, deliberately not shared. These
// packages hold no other dependency on each other, and a `uploads -> recording`
// import to reach twelve lines of syscall would be the wrong shape -- the same
// judgement internal/clips and internal/media already made about their own
// path checks.
func freeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail, not Bfree: Bfree counts blocks reserved for root, which the
	// service user cannot actually write into.
	return st.Bavail * uint64(st.Bsize), nil
}
