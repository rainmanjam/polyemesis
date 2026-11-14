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
