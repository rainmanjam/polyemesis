package db

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// app-council A14. The rolling clip buffer used to be a switch that lived only
// in the engine's memory, so every restart -- an upgrade, a reboot, a crash --
// quietly switched it off, and the operator found out by pressing Capture after
// the moment they wanted had already happened. These pin the storage half of
// the fix: one row per PROGRAMME, never one for the install, because
// SetClipBuffer is per programme and two programmes can want different answers.

func TestAProgrammeWithNoStoredClipBufferReadsAsOffWithNoWindow(t *testing.T) {
	// Every source that existed before this table, and every new one, has no
	// row. That must read as OFF: the buffer holds a window of video in memory,
	// and an upgrade must not start paying for one nobody asked for.
	d := testDB(t)
	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	got, err := d.GetSourceClipBuffer(src.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled || got.WindowSeconds != 0 {
		t.Errorf("an untouched programme read %+v, want off with no window", got)
	}
}

func TestTheClipBufferIsStoredPerProgrammeAndSurvivesReopeningTheDatabase(t *testing.T) {
	// Reopening the file is the restart, at the storage layer. Studio B's
	// switch must not leak into Main's, in either direction.
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mainSrc := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	studioB := &Source{Name: "Studio B", Enabled: true, Ingest: DefaultSettings().Ingest}
	for _, s := range []*Source{mainSrc, studioB} {
		if err := d.CreateSource(s); err != nil {
			t.Fatalf("create %s: %v", s.Name, err)
		}
	}
	if err := d.SetSourceClipBuffer(studioB.ID, SourceClipBuffer{Enabled: true, WindowSeconds: 45}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Written twice, so the second write is an update of the row rather than a
	// second insert the primary key would refuse.
	if err := d.SetSourceClipBuffer(studioB.ID, SourceClipBuffer{Enabled: true, WindowSeconds: 90}); err != nil {
		t.Fatalf("second set: %v", err)
	}
	d.Close()

	d, err = Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()

	b, err := d.GetSourceClipBuffer(studioB.ID)
	if err != nil {
		t.Fatalf("get Studio B: %v", err)
	}
	if !b.Enabled || b.WindowSeconds != 90 {
		t.Errorf("Studio B after reopen = %+v, want on at 90s", b)
	}
	m, err := d.GetSourceClipBuffer(mainSrc.ID)
	if err != nil {
		t.Fatalf("get Main: %v", err)
	}
	if m.Enabled {
		t.Error("Main's buffer came back on; Studio B's switch leaked across programmes")
	}
}

func TestDeletingAProgrammeTakesItsClipBufferRowWithIt(t *testing.T) {
	// A row left behind for a deleted source would be inherited by nothing --
	// ids are AUTOINCREMENT -- but it is still a row about a programme that is
	// gone, and the cascade is what keeps the table describing what exists.
	d := testDB(t)
	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := d.SetSourceClipBuffer(src.ID, SourceClipBuffer{Enabled: true, WindowSeconds: 30}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.DeleteSource(src.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM source_clip_buffers WHERE source_id = ?`, src.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d clip-buffer rows outlived their programme", n)
	}
}

func TestStoringAClipBufferForAProgrammeThatDoesNotExistIsRefused(t *testing.T) {
	// The foreign key, surfaced as the package's own not-found error, so a
	// caller can tell "no such programme" from a disk fault.
	d := testDB(t)
	err := d.SetSourceClipBuffer(987654, SourceClipBuffer{Enabled: true})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("err = %v, want ErrSourceNotFound", err)
	}
}

func TestAStaleSourceSaveDoesNotTouchTheStoredClipBuffer(t *testing.T) {
	// PUT /sources/{id} decodes the request over the stored row and writes it
	// back, from whatever tab the operator had open. The buffer lives in its own
	// table precisely so that path has no column to write -- the same device the
	// token columns use against a stale tab (#505).
	d := testDB(t)
	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := d.SetSourceClipBuffer(src.ID, SourceClipBuffer{Enabled: true, WindowSeconds: 30}); err != nil {
		t.Fatalf("set: %v", err)
	}
	src.Name = "Main (renamed)"
	if err := d.UpdateSource(src); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := d.GetSourceClipBuffer(src.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || got.WindowSeconds != 30 {
		t.Errorf("after a rename the buffer reads %+v, want on at 30s", got)
	}
}
