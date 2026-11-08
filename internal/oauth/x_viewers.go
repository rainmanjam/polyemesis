package oauth

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// XLiveViewers is what the analytics sampler reads from X: whether the account has a RUNNING broadcast and how many
// are watching it.
//
// X is deliberately NOT a LiveStatter. That capability's drift test ties its presence to the viewerStats cell
// reading "works", and this read has never run against a live X account, so the cell stays unverified. The
// sampler reaches this through its own seam (internal/api/analytics.go), the way YouTube's cheap read does.
type XLiveViewers struct {
	Live bool
	// Viewers is nil when X sent no total_watching (a count that was not given is not an audience of zero).
	Viewers   *int
	Title     string
	StartedAt *time.Time
}

// LiveViewers lists the account's broadcasts and reads the viewer count of the RUNNING one. Gated.
//
// total_watching is documented as live viewers (docs.x.com/livestream-api/broadcasts/overview), a string, and the
// get-broadcast reference does not say whether it is returned unasked, so Broadcasts requests it by name.
func (x *X) LiveViewers(ctx context.Context, accessToken string) (*XLiveViewers, error) {
	bs, err := x.Broadcasts(ctx, "", accessToken)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		if b.State != XStateRunning {
			continue
		}
		out := &XLiveViewers{Live: true, Title: b.Title}
		if n, err := strconv.Atoi(strings.TrimSpace(b.TotalWatching)); err == nil && n >= 0 {
			out.Viewers = &n
		}
		if ms, err := strconv.ParseInt(strings.TrimSpace(b.StartMS), 10, 64); err == nil && ms > 0 {
			t := time.UnixMilli(ms).UTC()
			out.StartedAt = &t
		}
		return out, nil
	}
	return &XLiveViewers{}, nil
}
