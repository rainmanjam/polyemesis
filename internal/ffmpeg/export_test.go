package ffmpeg

import "testing"

// PinLiveCommandsGOOS makes LiveCommandsSupported and every builder that
// consults it answer for goos until the test ends. Tests only: production code
// cannot reach it.
func PinLiveCommandsGOOS(t testing.TB, goos string) {
	t.Helper()
	prev := liveCommandsGOOS
	liveCommandsGOOS = goos
	t.Cleanup(func() { liveCommandsGOOS = prev })
}
