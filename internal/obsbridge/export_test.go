package obsbridge

import "time"

// ShortenForTest makes one grace-period second, and the reconnect backoff,
// last unit instead, so a test of a ten-second grace takes milliseconds.
func ShortenForTest(b *Bridge, unit time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.graceUnit = unit
	b.retryMin = unit
	b.retryMax = 5 * unit
}
