package freedisk

import "testing"

// statfs(2): f_blocks, f_bfree and f_bavail count blocks of f_frsize bytes.
// f_bsize is only the preferred I/O size, and on some mounts it is far larger
// than the unit the counts are in.
func TestBytesCountInFragmentUnitsNotIOSize(t *testing.T) {
	n := statfsNumbers{
		Blocks: 244_140_625, // 1 TB of 4 KiB blocks
		Bavail: 122_070_312,
		Bsize:  1 << 20,
		Frsize: 4096,
	}
	free, total := n.bytes()
	if want := uint64(244_140_625) * 4096; total != want {
		t.Errorf("total = %d, want %d (blocks x Frsize, not x Bsize)", total, want)
	}
	if want := uint64(122_070_312) * 4096; free != want {
		t.Errorf("free = %d, want %d (Bavail x Frsize, not x Bsize)", free, want)
	}
}

func TestBytesFallBackToBsizeWhereThereIsNoFragmentSize(t *testing.T) {
	// macOS and the BSDs have no f_frsize: f_bsize is the unit there, and an old
	// kernel can report a zero f_frsize.
	free, total := statfsNumbers{Blocks: 100, Bavail: 10, Bsize: 4096}.bytes()
	if free != 10*4096 || total != 100*4096 {
		t.Fatalf("got free %d total %d", free, total)
	}
}

func TestBytesAreUnchangedWhereBsizeEqualsFrsize(t *testing.T) {
	// Equal Bsize and Frsize (ext4, xfs, overlayfs): nothing changes.
	free, total := statfsNumbers{Blocks: 8, Bavail: 3, Bsize: 4096, Frsize: 4096}.bytes()
	if free != 3*4096 || total != 8*4096 {
		t.Fatalf("got free %d total %d", free, total)
	}
}
