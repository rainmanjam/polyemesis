package freedisk

// statfsNumbers are the four figures of statfs(2) that Space needs, widened to
// uint64 (their types differ by platform and by field).
type statfsNumbers struct {
	// Blocks and Bavail count blocks of Frsize bytes (Bsize where there is no
	// Frsize). Bavail is what the calling user may use, not Bfree, which
	// includes blocks reserved for root.
	Blocks, Bavail uint64
	// Bsize is the preferred I/O size and can be far larger than the unit the
	// counts are in. Frsize is that unit; 0 where the platform has none (macOS)
	// or an old kernel leaves it unset.
	Bsize, Frsize uint64
}

// bytes is the whole of the unit decision. Counts are in Frsize units
// (statfs(2)), NOT Bsize: a mount whose I/O size is larger than its block unit
// reported its free and total space inflated by the ratio -- consistent with
// the 466 TB free of 930 TB a host volume with about 1.8 TB free read as inside
// a container on Docker Desktop (1 MiB over 4 KiB is 256x). Where there is no
// Frsize, Bsize is the unit.
func (n statfsNumbers) bytes() (free, total uint64) {
	unit := n.Frsize
	if unit == 0 {
		unit = n.Bsize
	}
	return n.Bavail * unit, n.Blocks * unit
}
