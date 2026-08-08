package relay

import (
	"testing"
)

// BenchmarkFanout measures the per-datagram cost of the fan-out path, which at
// 20 Mbit/s runs about 1,900 times a second per hub and is the hottest thing in
// the process.
//
// The figure that matters is allocs/op, and the honest version of the claim is
// narrower than the review that raised it said. fanout used to build a fresh
// []*subscriber under mu.RLock for every datagram, but Go's escape analysis
// kept that slice on the stack up to twelve subscribers: measured on this
// machine it was 0 allocs at 1, 4, 8 and 12, and 1 alloc of 128 B at 16. So the
// per-datagram garbage was real only for an unusually busy hub, not for the
// handful of consumers a typical source has.
//
// What was true at every count is the lock: an RLock and RUnlock per datagram
// on a list that changes perhaps twice an hour. Both are gone.
func BenchmarkFanout(b *testing.B) {
	for _, n := range []int{1, 4, 8, 12, 16} {
		b.Run(subCount(n), func(b *testing.B) {
			h, err := New(testLogger(), 0)
			if err != nil {
				b.Fatalf("New: %v", err)
			}
			defer h.Close()
			for i := 0; i < n; i++ {
				// Unbound ports: the send fails fast and what is being measured
				// is the walk, not the kernel.
				h.Subscribe(subCount(i), 40000+i)
			}
			pkt := tsDatagram(0x40)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.fanout(pkt)
			}
		})
	}
}

func subCount(n int) string {
	return string(rune('a'+n%26)) + "-sub"
}
