package alerts

import (
	"testing"
	"time"
)

func fieldOf(ev Event, name string) string {
	for _, f := range ev.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// TWO PROGRAMMES LOSING THEIR INGEST USED TO BE TWO IDENTICAL ALERTS.
//
// Each engine has its own notifier and its own watcher, and the watcher wrote
// Key "ingest" and Title "Ingest lost" whichever programme it spoke for. So the
// channel got the same message twice with nothing to say which studio had gone
// dark, and a receiver deduplicating on key dropped the second outage as a
// repeat of the first.
//
// Mutation: return key unchanged from Watcher.subject. Observed to fail with
// `key = "ingest"`.
func TestAScopedWatcherNamesItsProgrammeOnEveryProgrammeEvent(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: time.Second})
	w.SetSource(SourceRef{ID: 7, Name: "Studio B"})

	w.Observe(Snapshot{At: base, IngestConfigured: true})
	evs := w.Observe(Snapshot{At: base.Add(2 * time.Second), IngestConfigured: true})
	if len(evs) != 1 || evs[0].Type != TypeIngestLost {
		t.Fatalf("events = %+v, want one ingest.lost", evs)
	}
	ev := evs[0]
	if ev.Key != "ingest:7" {
		t.Errorf("key = %q, want %q: two programmes' outages share a subject and "+
			"coalesce or deduplicate into one", ev.Key, "ingest:7")
	}
	if ev.Title != "Ingest lost: Studio B" {
		t.Errorf("title = %q, want the programme named in it", ev.Title)
	}
	if fieldOf(ev, "sourceId") != "7" || fieldOf(ev, "sourceName") != "Studio B" {
		t.Errorf("fields = %+v, want sourceId and sourceName", ev.Fields)
	}

	// Failover and clipping name something every programme has, too.
	w.Observe(Snapshot{At: base.Add(3 * time.Second), Failover: &FailoverState{Active: "primary"}})
	evs = w.Observe(Snapshot{At: base.Add(4 * time.Second), Failover: &FailoverState{Active: "backup", Switches: 1}})
	if len(evs) != 1 || evs[0].Key != "failover:7" || evs[0].Title != "Source switched to backup: Studio B" {
		t.Errorf("failover events = %+v, want key failover:7 naming Studio B", evs)
	}
	var clip []Event
	for i := 0; i < DefaultClipHits; i++ {
		clip = w.Observe(Snapshot{At: base.Add(time.Duration(5+i) * time.Second),
			Peaks: []PeakState{{Track: 1, Channel: 1, PeakDB: 0}}})
	}
	if len(clip) != 1 || clip[0].Key != "clipping:track1:7" || fieldOf(clip[0], "sourceId") != "7" {
		t.Errorf("clipping events = %+v, want key clipping:track1:7 with the programme", clip)
	}
}

// The disk is the install's, not the programme's: a scoped watcher leaves its
// key alone and stamps no programme on it, so InstallGate can recognise every
// engine's copy as the same subject.
func TestTheDiskIsNotStampedWithAProgramme(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	w.SetSource(SourceRef{ID: 7, Name: "Studio B"})
	evs := w.Observe(Snapshot{At: base, Disk: DiskState{FreeBytes: 1, TotalBytes: 1 << 40}})
	if len(evs) != 1 || evs[0].Type != TypeDiskLow {
		t.Fatalf("events = %+v, want one disk.low", evs)
	}
	if evs[0].Key != "disk" || fieldOf(evs[0], "sourceId") != "" {
		t.Errorf("disk.low = %+v, want key \"disk\" and no programme", evs[0])
	}
}

// An unscoped watcher keeps the keys it always wrote. It is what a hand-built
// test watcher is, and what every key documented before programmes existed
// describes.
func TestAnUnscopedWatcherKeepsTheOldKeys(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: time.Second})
	w.Observe(Snapshot{At: base, IngestConfigured: true})
	evs := w.Observe(Snapshot{At: base.Add(2 * time.Second), IngestConfigured: true})
	if len(evs) != 1 || evs[0].Key != "ingest" || evs[0].Title != "Ingest lost" || len(evs[0].Fields) != 0 {
		t.Errorf("events = %+v, want the unscoped key and title and no programme fields", evs)
	}
}

// ONE disk.low PER INSTALL, however many engines measure the volume.
//
// Mutation: drop the `g.last[ev.Key] == ev.Type` refusal from Admit. Observed
// to fail with "the second engine's disk.low was admitted".
func TestTheInstallGateLetsOneEngineSpeakForTheDisk(t *testing.T) {
	g := NewInstallGate()
	low := Event{Type: TypeDiskLow, Key: "disk"}
	rec := Event{Type: TypeDiskRecovered, Key: "disk"}

	if !g.Admit(low) {
		t.Fatal("the first disk.low was refused; nobody hears the disk filling")
	}
	if g.Admit(low) {
		t.Error("the second engine's disk.low was admitted: every rule gets one copy per programme")
	}
	if !g.Admit(rec) {
		t.Error("the first disk.recovered was refused; the install is never told it cleared")
	}
	if g.Admit(rec) {
		t.Error("the second engine's disk.recovered was admitted")
	}
	if !g.Admit(low) {
		t.Error("a second episode was refused as a repeat of the first")
	}

	// Programme events are never the gate's business, however alike they are.
	lost := Event{Type: TypeIngestLost, Key: "ingest:1"}
	if !g.Admit(lost) || !g.Admit(lost) {
		t.Error("the gate refused a programme-scoped event")
	}
	// And no gate is the old behaviour, for an engine built by hand.
	var none *InstallGate
	if !none.Admit(low) || !none.Admit(low) {
		t.Error("a nil gate refused an event")
	}
}
