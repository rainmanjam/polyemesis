// Package logtz renders log timestamps in the install's display time zone.
//
// THE PROBLEM IT SOLVES IS ARITHMETIC UNDER PRESSURE. The server logged in
// UTC and the console rendered in whatever zone the browser was in, so an
// operator in Los Angeles comparing a log line with the screen that produced
// it was converting 05:51Z to 22:51 in their head -- at the moment something
// was going wrong, which is the worst possible moment to ask anyone to do
// arithmetic. Two people on the same production, in different places, could
// not read each other's screenshots at all.
//
// Settings.Display.TimeZone names one zone for the whole install and this is
// the half of it that reaches the log.
//
// Switchable at runtime because the logger exists before the database does:
// main builds it to report what happens during boot, and the setting cannot be
// read until the store is open. Same shape as the runtime log level, and for
// the same reason.
package logtz

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// UTC until told otherwise, which is what every line looked like before this
// existed. A boot that fails before the settings load therefore reads exactly
// as it always has, rather than in some zone nobody chose.
var loc atomic.Pointer[time.Location]

// Set points every subsequent log line at zone. A nil zone means UTC.
func Set(zone *time.Location) {
	if zone == nil {
		zone = time.UTC
	}
	loc.Store(zone)
}

// Location is the zone lines are currently written in.
func Location() *time.Location {
	if l := loc.Load(); l != nil {
		return l
	}
	return time.UTC
}

// ReplaceAttr is slog.HandlerOptions.ReplaceAttr. It rewrites the built-in
// time attribute and touches nothing else.
//
// The zone is read PER LINE rather than captured when the handler is built,
// which is what lets a save take effect on the next line instead of the next
// restart -- and is why settings/reload.go classes this field on_demand.
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	// Only the top-level `time`. A nested attribute that happens to be called
	// "time" belongs to whoever logged it, and rewriting it would be this
	// package editing somebody else's data.
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.TimeValue(a.Value.Time().In(Location()))
	}
	return a
}
