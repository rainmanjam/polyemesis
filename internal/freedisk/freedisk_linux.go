//go:build linux

package freedisk

import "golang.org/x/sys/unix"

// Space reports the bytes available to the calling user and the total bytes on
// the filesystem holding path.
func Space(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	free, total = statfsNumbersOf(&st).bytes()
	return free, total, nil
}

// statfsNumbersOf names which kernel field is which. Bavail, not Bfree: Bfree
// includes blocks reserved for root, which the service user cannot actually use.
func statfsNumbersOf(st *unix.Statfs_t) statfsNumbers {
	return statfsNumbers{
		Blocks: uint64(st.Blocks),
		Bavail: uint64(st.Bavail),
		Bsize:  uint64(st.Bsize),
		Frsize: uint64(st.Frsize),
	}
}
