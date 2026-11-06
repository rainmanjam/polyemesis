//go:build !windows

package freedisk

import "golang.org/x/sys/unix"

// Space reports the bytes available to the calling user and the total bytes on
// the filesystem holding path.
func Space(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	// Bavail, not Bfree: Bfree includes blocks reserved for root, which the
	// service user cannot actually use.
	return st.Bavail * bs, st.Blocks * bs, nil
}
