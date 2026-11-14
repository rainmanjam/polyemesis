package freedisk

// bytesFromBlocks turns statfs block counts into bytes.
//
// The counts are in units of f_frsize (statfs(2)), NOT f_bsize: f_bsize is the
// preferred I/O size and can be far larger. Docker Desktop's file-sharing
// mounts report f_bsize 1 MiB over f_frsize 4096, which made a 1.8 TB free
// volume read as 466 TB free of 930 TB. Where there is no f_frsize (macOS, the
// BSDs) or it is zero (old kernels) f_bsize is the unit.
func bytesFromBlocks(avail, blocks, frsize, bsize uint64) (free, total uint64) {
	unit := frsize
	if unit == 0 {
		unit = bsize
	}
	return avail * unit, blocks * unit
}
