package freedisk

import "testing"

// statfs(2): f_blocks, f_bfree and f_bavail count blocks of f_frsize bytes.
// f_bsize is only the preferred I/O size, and on some filesystems it is far
// larger than the unit the counts are in. Docker Desktop's file-sharing mounts
// (virtiofs) report f_bsize 1 MiB with f_frsize 4096: multiplying by f_bsize
// read a host volume with 1.8 TB free as 466 TB free of 930 TB.
func TestBytesCountsInFragmentUnitsNotIOSize(t *testing.T) {
	const (
		frsize = 4096
		bsize  = 1 << 20
		blocks = 244_140_625 // 1 TB of 4 KiB blocks
		avail  = 122_070_312
	)
	free, total := bytesFromBlocks(avail, blocks, frsize, bsize)
	if total != blocks*frsize || free != avail*frsize {
		t.Fatalf("got free %d total %d, want %d %d (counted in the fragment size)", free, total, uint64(avail*frsize), uint64(blocks*frsize))
	}
}

func TestBytesFallsBackToBsizeWhereThereIsNoFragmentSize(t *testing.T) {
	// macOS and the BSDs have no f_frsize: f_bsize is the unit there, and an
	// old kernel can report a zero f_frsize.
	free, total := bytesFromBlocks(10, 100, 0, 4096)
	if free != 10*4096 || total != 100*4096 {
		t.Fatalf("got %d %d", free, total)
	}
}
