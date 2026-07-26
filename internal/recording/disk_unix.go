//go:build !windows

package recording

import "golang.org/x/sys/unix"

// diskFree reports free and total bytes on the filesystem holding path.
func diskFree(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	// Bavail, not Bfree: Bfree includes blocks reserved for root, which a
	// recording process cannot actually use.
	return st.Bavail * bs, st.Blocks * bs, nil
}
