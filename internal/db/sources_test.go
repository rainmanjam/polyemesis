package db

import (
	"strings"
	"testing"
)

// The migration is the dangerous part of this feature. An operator upgrades,
// their encoder is already pointed at a port, and if the first source does not
// come up on exactly that port with exactly that protocol they have lost their
// broadcast with no obvious cause. These tests exist for that.

func TestMigrationCarriesAnExistingIngestOntoTheFirstSource(t *testing.T) {
	d := testDB(t)

	// A single-ingest install: RTMP on a non-default port, with a stream key.
	// If any of this fails to survive, the encoder stops connecting.
	want := DefaultSettings()
	want.Ingest.Mode = IngestRTMP
	want.Ingest.RTMP.Port = 1937
	want.Ingest.RTMP.App = "live"
	want.Ingest.RTMP.StreamKey = "secretkey"
	want.Ingest.SRT.Port = 6123
	if err := d.PutSettings(want); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	// Drop the source the fresh-database open already created, so this test
	// exercises the upgrade path rather than the first-run path.
	if _, err := d.SQL().Exec(`DELETE FROM sources`); err != nil {
		t.Fatalf("clear sources: %v", err)
	}
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want exactly 1", len(got))
	}
	s := got[0]
	if s.Name != DefaultSourceName {
		t.Errorf("name = %q, want %q", s.Name, DefaultSourceName)
	}
	if !s.Enabled {
		t.Error("the migrated source is disabled; the install would stop ingesting")
	}
	if s.Ingest.Mode != IngestRTMP {
		t.Errorf("mode = %q, want rtmp", s.Ingest.Mode)
	}
	if s.Ingest.RTMP.Port != 1937 {
		t.Errorf("rtmp port = %d, want 1937 (the encoder is pointed there)", s.Ingest.RTMP.Port)
	}
	if s.Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("stream key = %q, want it carried across", s.Ingest.RTMP.StreamKey)
	}
	if s.Ingest.SRT.Port != 6123 {
		t.Errorf("srt port = %d, want 6123", s.Ingest.SRT.Port)
	}
	if s.Token == "" {
		t.Error("migrated source has no publish token")
	}
}

func TestMigrationBackfillsExistingRowsOntoTheFirstSource(t *testing.T) {
	d := testDB(t)

	dst, err := d.CreateDestination(&Destination{
		Name: "pre-existing", Kind: DestRTMP, URL: "rtmp://example/live"})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Simulate a row written before sources existed.
	if _, err := d.SQL().Exec(`UPDATE destinations SET source_id = NULL`); err != nil {
		t.Fatalf("null out source_id: %v", err)
	}
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}

	want, err2 := d.DefaultSourceID()
	if err2 != nil {
		t.Fatalf("DefaultSourceID: %v", err2)
	}
	var got *int64
	if err := d.SQL().QueryRow(`SELECT source_id FROM destinations WHERE id = ?`, dst.ID).Scan(&got); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if got == nil {
		t.Fatal("destination still has a NULL source_id after the backfill")
	}
	if *got != want {
		t.Errorf("source_id = %d, want %d", *got, want)
	}
}

func TestMigrateSourcesIsIdempotent(t *testing.T) {
	d := testDB(t)
	// It runs on every open, so running it repeatedly must not accumulate
	// sources or fail on the already-added columns.
	for i := 0; i < 3; i++ {
		if err := d.MigrateSources(); err != nil {
			t.Fatalf("MigrateSources run %d: %v", i+1, err)
		}
	}
	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources after 3 migrations, want 1", len(got))
	}
}

func TestSourceTokensAreUniqueAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := NewSourceToken()
		if err != nil {
			t.Fatalf("NewSourceToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token after %d draws: %q", i, tok)
		}
		seen[tok] = true
		// This value goes into an SRT streamid and an RTMP path. A '+', '/'
		// or '=' would need escaping in at least one of them, which is how you
		// ship an ingest that works everywhere except for the one operator
		// whose token happened to contain a slash.
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q contains a character that needs URL escaping", tok)
		}
	}
}

func TestSourceByTokenRejectsTheEmptyToken(t *testing.T) {
	d := testDB(t)
	// A publisher sending no token must not authenticate as whichever source
	// happens to have an empty one stored.
	if _, err := d.SQL().Exec(`UPDATE sources SET token = ''`); err != nil {
		t.Fatalf("blank the token: %v", err)
	}
	if _, err := d.SourceByToken(""); err == nil {
		t.Fatal("SourceByToken(\"\") matched a source; anyone could publish")
	}
	if _, err := d.SourceByToken("   "); err == nil {
		t.Fatal("SourceByToken(whitespace) matched a source")
	}
}

func TestSourceByTokenResolvesTheRightSource(t *testing.T) {
	d := testDB(t)
	vert := &Source{Name: "Vertical", Enabled: true, Ingest: DefaultSettings().Ingest}
	vert.Ingest.SRT.Port = 6001
	if err := d.CreateSource(vert); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	got, err := d.SourceByToken(vert.Token)
	if err != nil {
		t.Fatalf("SourceByToken: %v", err)
	}
	if got.ID != vert.ID {
		t.Errorf("resolved source %d, want %d", got.ID, vert.ID)
	}
}

func TestDeletingASourceTakesItsDestinationsButKeepsItsRecordings(t *testing.T) {
	d := testDB(t)

	extra := &Source{Name: "Vertical", Enabled: true, Ingest: DefaultSettings().Ingest}
	extra.Ingest.SRT.Port = 6001
	if err := d.CreateSource(extra); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	dst, err := d.CreateDestination(&Destination{
		Name: "tiktok", Kind: DestRTMP, URL: "rtmp://example/live", SourceID: &extra.ID})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('v.mkv', 1, ?)`, extra.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}

	if err := d.DeleteSource(extra.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	var dests int
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM destinations WHERE id = ?`, dst.ID).Scan(&dests); err != nil {
		t.Fatalf("count destinations: %v", err)
	}
	if dests != 0 {
		t.Error("destination survived its source; it points nowhere meaningful now")
	}

	// The file is still on disk and still playable, and its transcript and
	// clips hang off this row. Deleting it would orphan all of that.
	var recs int
	var srcID *int64
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM recordings WHERE filename = 'v.mkv'`).Scan(&recs); err != nil {
		t.Fatalf("count recordings: %v", err)
	}
	if recs != 1 {
		t.Fatal("recording was deleted with its source; the file is now orphaned")
	}
	if err := d.SQL().QueryRow(`SELECT source_id FROM recordings WHERE filename = 'v.mkv'`).Scan(&srcID); err != nil {
		t.Fatalf("read recording source_id: %v", err)
	}
	if srcID != nil {
		t.Errorf("recording source_id = %d, want NULL after the source was deleted", *srcID)
	}
}

func TestTheLastSourceCannotBeDeleted(t *testing.T) {
	d := testDB(t)
	id, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	// An install with no sources has no ingest and no way back through the UI.
	if err := d.DeleteSource(id); err == nil {
		t.Fatal("deleted the only source; the install now has no ingest at all")
	}
}

func TestSourceValidationRejectsABadIngest(t *testing.T) {
	d := testDB(t)
	bad := &Source{Name: "Broken", Enabled: true, Ingest: DefaultSettings().Ingest}
	bad.Ingest.SRT.Port = 70000
	err := d.CreateSource(bad)
	if err == nil {
		t.Fatal("CreateSource accepted an out-of-range SRT port")
	}
	// Shared with Settings.Validate, so the message the form shows is the same
	// wording in both places.
	if !strings.Contains(err.Error(), "srt port") {
		t.Errorf("error = %q, want it to name the srt port", err)
	}
}

func TestUpdateSourceNeverStoresAnEmptyToken(t *testing.T) {
	d := testDB(t)
	got, err := d.ListSources()
	if err != nil || len(got) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	s := got[0]
	s.Token = ""
	if err := d.UpdateSource(s); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if s.Token == "" {
		t.Fatal("UpdateSource stored an empty token: anyone reaching the port could publish")
	}
	back, err := d.GetSource(s.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if back.Token != s.Token {
		t.Errorf("stored token %q, want %q", back.Token, s.Token)
	}
}
