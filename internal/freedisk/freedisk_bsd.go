//go:build !windows && !linux

// Built and tested for darwin, the only non-Linux Unix release target. The
// other BSDs name these fields differently and did not build before either.

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
	// service user cannot actually use. No Frsize here: Bsize is the unit.
	free, total = statfsNumbers{
		Blocks: uint64(st.Blocks),
		Bavail: uint64(st.Bavail),
		Bsize:  uint64(st.Bsize),
	}.bytes()
	return free, total, nil
}
