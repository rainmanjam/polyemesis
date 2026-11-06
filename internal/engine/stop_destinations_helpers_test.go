package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

func TestDestUpstream(t *testing.T) {
	rid := int64(7)
	gone := int64(8)
	cases := []struct {
		name    string
		rend    *int64
		want    string
		wantErr string
	}{
		{"passthrough reads the silence tier", nil, "silence", ""},
		{"a rendition's own signature", &rid, "rend-7", ""},
		{"a deleted rendition", &gone, "", "rendition 8 is no longer available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p destPlan
			got := destUpstream(&db.Destination{RenditionID: tc.rend}, map[int64]string{7: "rend-7"}, "silence", &p)
			if got != tc.want || p.err != tc.wantErr {
				t.Errorf("destUpstream = %q, err %q; want %q, err %q", got, p.err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDropRepointedOnlyDropsTheCurrentEntry(t *testing.T) {
	d := &destination{row: &db.Destination{ID: 1, Name: "x"}}
	other := &destination{row: &db.Destination{ID: 1, Name: "x"}}

	e := &Engine{log: testLogger(), dests: map[int64]*destination{1: other}}
	e.dropRepointed(d)
	if e.dests[1] != other {
		t.Fatal("dropRepointed removed an entry that had already been replaced")
	}

	e.dests[1] = d
	e.dropRepointed(d)
	if _, ok := e.dests[1]; ok {
		t.Fatal("dropRepointed left the destination it could not re-point in the map")
	}
}

// A line whose input cannot follow the new upstream is dropped from the map
// (and torn down, when it is still the entry) rather than left looking healthy
// with nothing reaching its buffer. Here a reconcile already replaced the
// entry, so the replacement must survive.
func TestApplyRepointsFailureLeavesAReplacedEntryAlone(t *testing.T) {
	to, err := relay.New(testLogger(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = to.Close() }()
	// The name is already taken on the new hub, so the line's Subscribe fails.
	if _, err := to.Subscribe("line", 1); err != nil {
		t.Fatal(err)
	}
	d := &destination{row: &db.Destination{ID: 3, Name: "x"}, delay: &destDelay{inSub: "line", inPort: 2}}
	other := &destination{row: d.row}
	e := &Engine{log: testLogger(), dests: map[int64]*destination{3: other}}
	e.applyRepoints([]delayRepoint{{d: d, to: to}})
	if e.dests[3] != other {
		t.Fatal("a failed re-point removed the entry that had replaced it")
	}
}

func TestNoteKeptLevelsLocked(t *testing.T) {
	running := liveGraph("0.4", "0.5")
	moved := liveGraph("0.4", "0")
	row := backupRow()

	cases := []struct {
		name     string
		copyPath bool
		want     destPlan
		wantTry  int
		wantNote string
	}{
		{"unchanged levels do nothing", false, destPlan{row: row, compiled: running}, 0, ""},
		{"moved levels are tried live", false, destPlan{row: row, compiled: moved}, 1, ""},
		{"a copy destination has no level to move", true, destPlan{row: row, compiled: moved}, 0, AppliedLive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := *tc.want.row
			r.Audio.Copy = tc.copyPath
			r.Kind = db.DestRTMP
			p := tc.want
			p.row = &r
			e := &Engine{}
			var sets stopSets
			d := &destination{graph: newGraphHolder(running)}
			e.noteKeptLevelsLocked(&sets, 5, d, p)
			if len(sets.liveTry) != tc.wantTry {
				t.Errorf("liveTry = %d, want %d", len(sets.liveTry), tc.wantTry)
			}
			if got := e.liveNotes[5].applied; got != tc.wantNote {
				t.Errorf("note = %q, want %q", got, tc.wantNote)
			}
		})
	}
}
