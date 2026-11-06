// Package backoff is the one doubling-and-capped retry delay. MQTT reconnects,
// webhook deliveries, offload retries and analytics polling each had their own
// copy; what differed between them (jitter, a server's Retry-After, "no wait
// after the first failure") stays with the caller, because each has a reason.
package backoff

import "time"

// Exponential is base doubled retries times, capped at max. retries 0 (or
// less) is base.
func Exponential(retries int, base, max time.Duration) time.Duration {
	d := base
	for range retries {
		if d >= max {
			break
		}
		d *= 2
	}
	return min(d, max)
}
