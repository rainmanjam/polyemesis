package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Status copies the destination pointers out under the read lock and then reads
// their fields after dropping it, which is only sound while a published
// destination never changes again. Refreshing the row of a destination that
// survived the stop phase therefore has to publish a replacement, not write
// through the pointer the dashboard is already holding.
func TestRefreshingARunningDestinationPublishesAReplacementRatherThanMutatingIt(t *testing.T) {
	tests := []struct {
		name    string
		oldName string
		newName string
	}{
		{"a renamed destination", "old name", "new name"},
		{"a cosmetic edit that changed nothing", "same name", "same name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			running := &destination{
				row:      &db.Destination{ID: 1, Name: tc.oldName},
				compiled: routing.Result{Summary: "one video, two audio"},
				port:     1234,
				subName:  "dest-1",
				spec:     "unchanged",
			}
			e := &Engine{dests: map[int64]*destination{1: running}}

			e.startDestinations(map[int64]destPlan{
				1: {row: &db.Destination{ID: 1, Name: tc.newName}},
			})

			if e.dests[1] == running {
				t.Fatal("refresh republished the same pointer; Status reads it without the lock")
			}
			if got := running.row.Name; got != tc.oldName {
				t.Errorf("refresh wrote through the old pointer: name = %q, want it left at %q", got, tc.oldName)
			}
			if got := e.dests[1].row.Name; got != tc.newName {
				t.Errorf("refreshed name = %q, want %q", got, tc.newName)
			}
		})
	}
}

// The refresh is meant to update cosmetic row fields only. Losing any of the
// state that ties the entry to its already-running FFmpeg would orphan the
// process: the next reconcile would see a stranger and restart the stream.
func TestRefreshingARunningDestinationKeepsTheStateThatIdentifiesItsProcess(t *testing.T) {
	running := &destination{
		row:      &db.Destination{ID: 7, Name: "before"},
		compiled: routing.Result{Summary: "one video, two audio"},
		port:     9001,
		subName:  "dest-7",
		spec:     "restart-me-only-if-this-changes",
		err:      "",
	}
	e := &Engine{dests: map[int64]*destination{7: running}}

	e.startDestinations(map[int64]destPlan{
		7: {row: &db.Destination{ID: 7, Name: "after"}},
	})

	got := e.dests[7]
	tests := []struct {
		field string
		got   any
		want  any
	}{
		{"port", got.port, running.port},
		{"subName", got.subName, running.subName},
		{"spec", got.spec, running.spec},
		{"compiled.Summary", got.compiled.Summary, running.compiled.Summary},
		{"proc", got.proc, running.proc},
		{"hub", got.hub, running.hub},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %v after refresh, want %v", tc.field, tc.got, tc.want)
		}
	}
}
