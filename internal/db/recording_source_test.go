package db

import "testing"

// source_id IS WRITTEN, and for a long time it was not.
//
// Two SELECTs in this file read the column and no statement anywhere set it, so
// every recording on every install carried NULL -- not merely the rows written
// before sources existed, which is what the code reading it assumed. That made
// a real defence inert while reading as solid: clipTracks resolves a
// recording's own programme so a clip cut from Studio B is not labelled with
// Main's track names, and it fell back to the default programme every single
// time.
//
// Mutation: drop source_id from the INSERT column list. Observed to fail with
// "indexed a segment for programme 2 and read back <nil>".
func TestUpsertRecordingKeepsTheProgrammeThatRecordedIt(t *testing.T) {
	d := testDB(t)

	// A REAL PROGRAMME, because recordings.source_id carries a foreign key --
	// which is its own Control and worth naming: a bogus attribution cannot be
	// written at all, only a missing one.
	src := &Source{Name: "Studio B", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	two := src.ID

	if err := d.UpsertRecording(&Recording{Filename: "b.mkv", Bytes: 10, SourceID: &two}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := findRecording(t, d, "b.mkv")
	if got.SourceID == nil {
		t.Fatalf("indexed a segment for programme %d and read back <nil>: every clip "+
			"cut from it will be labelled with the default programme's track names", two)
	}
	if *got.SourceID != two {
		t.Fatalf("indexed for programme %d, read back %d", two, *got.SourceID)
	}

	// A LATER SWEEP MUST NOT WIPE IT. The re-index that measures a finished
	// segment does not know the programme, and duration_ms and tracks beside it
	// are guarded for exactly this reason.
	if err := d.UpsertRecording(&Recording{Filename: "b.mkv", Bytes: 20, DurationMS: 10010}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got = findRecording(t, d, "b.mkv")
	if got.SourceID == nil || *got.SourceID != two {
		t.Errorf("a re-index with no programme wiped the attribution: %v", got.SourceID)
	}
}

func findRecording(t *testing.T, d *DB, name string) Recording {
	t.Helper()
	rows, err := d.ListRecordings()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Filename == name {
			return r
		}
	}
	t.Fatalf("no recording %q in the index", name)
	return Recording{}
}
