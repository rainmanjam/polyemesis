package captions

import (
	"github.com/rainmanjam/polyemesis/internal/mpegts"
	"testing"
)

// FuzzInjectorWrite feeds the injector arbitrary transport-stream bytes, which is
// what an encoder can send. The injector must never panic and must always finish
// its Flush. The seeds are a valid stream and packets with adaptation fields that
// are too long, so the fuzzer starts from the shapes that have broken it before.
func FuzzInjectorWrite(f *testing.F) {
	f.Add(streamOf([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}))
	bad := make([]byte, mpegts.PacketSize)
	bad[0] = mpegts.SyncByte
	bad[1] = 0x40 | byte(testVideoPID>>8)
	bad[2] = byte(testVideoPID & 0xFF)
	bad[3] = 0x30
	bad[4] = 200
	f.Add(append(psFixture(0x1B), bad...))
	f.Add(append(psFixture(0x1B), append([]byte{mpegts.SyncByte, 0x01, 0x00, 0x30, 183}, make([]byte, 183)...)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		inj := NewInjector(NewSchedule(0))
		_ = inj.Write(data)
		_ = inj.Flush()
	})
}
