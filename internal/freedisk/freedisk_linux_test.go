//go:build linux

package freedisk

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The field Space reads the block unit from, pinned on the kernel's own struct.
// Reading Bsize here is the defect that made a 1.8 TB volume read as 466 TB.
func TestLinuxCountsInFrsizeNotBsize(t *testing.T) {
	st := unix.Statfs_t{Blocks: 1000, Bavail: 400, Bsize: 1 << 20, Frsize: 4096}
	free, total := statfsNumbersOf(&st).bytes()
	if free != 400*4096 || total != 1000*4096 {
		t.Fatalf("free %d total %d, want %d %d (Frsize is the unit)", free, total, 400*4096, 1000*4096)
	}

	st.Frsize = 0 // an old kernel leaves it unset: Bsize is then the unit
	free, total = statfsNumbersOf(&st).bytes()
	if free != 400*(1<<20) || total != 1000*(1<<20) {
		t.Fatalf("zero Frsize: free %d total %d", free, total)
	}
}

// Space itself, through the seam, on a mount whose Bsize is not its block unit
// (Docker Desktop's shared folders are consistent with one). A Space that
// multiplied by Bsize directly would read 256 times too large here; ext4 on a CI
// runner cannot show it, because there Bsize == Frsize.
func TestSpaceCountsInFrsizeNotBsize(t *testing.T) {
	orig := statfs
	defer func() { statfs = orig }()
	statfs = func(_ string, st *unix.Statfs_t) error {
		*st = unix.Statfs_t{Blocks: 1000, Bavail: 400, Bsize: 1 << 20, Frsize: 4096}
		return nil
	}
	free, total, err := Space("/anywhere")
	if err != nil {
		t.Fatal(err)
	}
	if free != 400*4096 || total != 1000*4096 {
		t.Fatalf("Space = free %d total %d, want %d %d: it must count in Frsize", free, total, 400*4096, 1000*4096)
	}
}

func TestSpaceReportsTheKernelsError(t *testing.T) {
	orig := statfs
	defer func() { statfs = orig }()
	statfs = func(string, *unix.Statfs_t) error { return unix.ENOENT }
	if free, total, err := Space("/x"); err == nil || free != 0 || total != 0 {
		t.Fatalf("got (%d, %d, %v)", free, total, err)
	}
}
