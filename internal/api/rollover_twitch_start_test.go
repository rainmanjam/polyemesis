package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The Twitch countdown counts from the stream's own start (Helix GET /helix/streams started_at), not from the
// first moment polyemesis happened to see the destination on air. Restarting polyemesis mid-stream must not
// reset the 48-hour count. When the start cannot be read, the old behaviour stands: the first sighting is used.

// aTwitchOnAirRig is a Twitch destination that is delivering and has no session recorded yet, so the sweep reads
// its start.
func aTwitchOnAirRig(t *testing.T) *rolloverRig {
	t.Helper()
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformTwitch, "")
	r.enable(1, "")
	return r
}

// The platform's own start is the session start when it is available.
func TestTheTwitchCountdownStartsFromTheStreamsOwnStartedAt(t *testing.T) {
	r := aTwitchOnAirRig(t)
	platformStart := r.now.Add(-20 * time.Hour)
	r.c.host.twitchStart = func(context.Context, string) (time.Time, error) { return platformStart, nil }

	r.c.sweepOnce(context.Background())

	if got := r.store.settings[1].SessionStartedAt; !got.Equal(platformStart) {
		t.Fatalf("session start = %v, want the platform's started_at %v, not the first sighting %v",
			got, platformStart, r.now)
	}
}

// When the read fails, the first sighting is used, as it was before the read existed.
func TestTheTwitchCountdownFallsBackToTheFirstSightingWhenTheStartCannotBeRead(t *testing.T) {
	r := aTwitchOnAirRig(t)
	r.c.host.twitchStart = func(context.Context, string) (time.Time, error) {
		return time.Time{}, errors.New("helix is unavailable")
	}

	r.c.sweepOnce(context.Background())

	if got := r.store.settings[1].SessionStartedAt; !got.Equal(r.now) {
		t.Fatalf("session start = %v, want the first sighting %v when the read fails", got, r.now)
	}
}
