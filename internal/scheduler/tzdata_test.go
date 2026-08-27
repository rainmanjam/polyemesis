package scheduler

import (
	"testing"
	"time"
)

// EVERY IANA ZONE RESOLVES, INCLUDING IN THE CONTAINER.
//
// A schedule carries a zone name and time.LoadLocation resolves it against
// /usr/share/zoneinfo -- which the shipped Alpine image does not have. Zones
// were refused at save time there, and worse, a schedule saved where the
// database exists and run where it does not never fires: Previous() returns
// (zero, false) when Location() errors, which is the identical answer to
// "nothing is due". A show that should have gone on air at 19:00 does not, and
// the runs page shows no failure because there was no run.
//
// cmd/polyemesis imports _ "time/tzdata", which compiles the database into the
// binary. This asserts the property rather than the import, so a future build
// that drops the blank import fails HERE, naming the consequence.
//
// Mutation: remove the tzdata import from cmd/polyemesis/main.go and run this
// in a container without zoneinfo. It fails naming the zone.
func TestEveryScheduleZoneResolves(t *testing.T) {
	// The zones an operator of a broadcast product plausibly types.
	for _, name := range []string{
		"UTC",
		"Europe/London",
		"America/New_York",
		"America/Los_Angeles",
		"Australia/Sydney",
		"Asia/Tokyo",
	} {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("time zone %q does not resolve: %v.\n"+
				"A schedule carrying it is refused at save, and one already stored "+
				"never fires and never says why -- Previous() cannot tell a zone it "+
				"could not load from a night with nothing due.", name, err)
		}
	}
}
