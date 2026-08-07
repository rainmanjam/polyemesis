package db

import "testing"

/* A fresh install must not have an ingest mode chosen for it, and an existing
 * install must not lose the one it has.
 *
 * These pull in opposite directions and the same constant cannot serve both:
 * DefaultSettings seeds a new database, and it is ALSO the base that an existing
 * settings blob is decoded over. Making it unset without splitting the two took
 * every install whose stored blob omitted the field from "silently SRT" to "no
 * ingest" on upgrade. */

func TestFreshInstallHasNoIngestModeChosen(t *testing.T) {
	if got := DefaultSettings().Ingest.Mode; got != IngestUnset {
		t.Fatalf("a fresh install starts with mode %q; it must start unset so first run has to ask", got)
	}
}

func TestAnExistingBlobWithoutAModeKeepsWorking(t *testing.T) {
	// The upgrade case: a stored document that never carried ingest.mode.
	// Decoding it must not hand back "no ingest".
	if got := mergeBaseSettings().Ingest.Mode; got != IngestSRT {
		t.Fatalf("merge base is %q; a stored blob missing ingest.mode would inherit it and the install would stop ingesting", got)
	}
	if got := mergeBaseSettings().Failover.Backup.Mode; got != IngestSRT {
		t.Fatalf("backup merge base is %q, want srt", got)
	}
}

func TestUnsetIsStorable(t *testing.T) {
	// It has to be: the migration creates the Main source during DB open, and a
	// validation error there stops the database opening at all.
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("a fresh, unchosen settings document must validate, or the server cannot start: %v", err)
	}
}

func TestARealModeStillValidates(t *testing.T) {
	for _, m := range []IngestMode{IngestSRT, IngestRTMP} {
		s := DefaultSettings()
		s.Ingest.Mode = m
		if err := s.Validate(); err != nil {
			t.Errorf("mode %q should validate: %v", m, err)
		}
	}
}

func TestAnUnknownModeIsStillRejected(t *testing.T) {
	// Unset is a state; a typo is not.
	s := DefaultSettings()
	s.Ingest.Mode = IngestMode("srtp")
	if err := s.Validate(); err == nil {
		t.Fatal("an unknown ingest mode must still be refused")
	}
}
