//go:build !windows && !linux

package freedisk

import "golang.org/x/sys/unix"

// Space reports the bytes available to the calling user and the total bytes on
// the filesystem holding path.
func Space(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bavail, not Bfree: Bfree includes blocks reserved for root, which the
	// service user cannot actually use. No Frsize on these platforms: Bsize is
	// the unit.
	free, total = bytesFromBlocks(uint64(st.Bavail), uint64(st.Blocks), 0, uint64(st.Bsize))
	return free, total, nil
}
