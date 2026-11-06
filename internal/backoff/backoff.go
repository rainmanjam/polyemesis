// Package backoff is the one doubling-and-capped retry delay. MQTT reconnects,
// webhook deliveries, offload retries, analytics polling, alert delivery, the
// job queue and chat reconnects each had their own copy, and SleepCtx is the
// one cancellable wait they all share.
//
// The offload multipart part retry (offload/multipart.go) deliberately stays on
// its own doubling: it has no cap, a partBackoff the tests set to microseconds,
// and a try count that bounds it instead. What differed between the others
// (jitter, a server's Retry-After, "no wait after the first failure") stays
// with the caller, because each has a reason.
package backoff

import (
	"context"
	"time"
)

// Exponential is base doubled retries times, capped at max. retries 0 (or
// less) is base.
func Exponential(retries int, base, ceiling time.Duration) time.Duration {
	d := base
	for range retries {
		if d >= ceiling {
			break
		}
		d *= 2
	}
	return min(d, ceiling)
}

// SleepCtx waits d, or until ctx is done, and reports whether the full wait
// elapsed. A d of zero or less is no wait at all, answered by ctx alone: a
// timer that is already due and a done context would otherwise race in the
// select and return either.
func SleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
