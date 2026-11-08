package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestViewerNotesTableIsCreatedIdempotentlyAndLeavesTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateViewerNotes(); err != nil {
			t.Fatalf("MigrateViewerNotes pass %d: %v", pass, err)
		}
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'viewer_notes'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("viewer_notes missing after the slot ran: n=%d err=%v", n, err)
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateViewerNotes moved the schema version from %d to %d", before, after)
	}
}

func TestViewerNoteRoundTripAndKey(t *testing.T) {
	d := testDB(t)
	at := time.UnixMilli(1_700_000_000_000)
	if err := d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, Account: "a", AuthorID: "u1",
		Note: "  helpful  ", Tags: []string{"regular", "Regular", " ", "watch"}, UpdatedAt: at, UpdatedBy: "admin"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := d.GetViewerNote(PlatformTwitch, "a", "u1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Note != "helpful" || len(got.Tags) != 2 || got.Tags[0] != "regular" || got.Tags[1] != "watch" ||
		got.UpdatedBy != "admin" || !got.UpdatedAt.Equal(at) {
		t.Fatalf("round trip = %+v", got)
	}
	// A different account or platform is a different viewer.
	for _, k := range [][3]string{{"twitch", "b", "u1"}, {"youtube", "a", "u1"}, {"twitch", "a", "u2"}} {
		if _, ok, _ := d.GetViewerNote(Platform(k[0]), k[1], k[2]); ok {
			t.Fatalf("note leaked to %v", k)
		}
	}
	// Saving again replaces.
	if err := d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, Account: "a", AuthorID: "u1", Note: "second"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = d.GetViewerNote(PlatformTwitch, "a", "u1")
	if got.Note != "second" || len(got.Tags) != 0 {
		t.Fatalf("replace = %+v", got)
	}
}

func TestViewerNoteEmptyDeletesAndDeleteIsIdempotent(t *testing.T) {
	d := testDB(t)
	if err := d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, AuthorID: "u1", Note: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, AuthorID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetViewerNote(PlatformTwitch, "", "u1"); ok {
		t.Fatal("an empty save should remove the row")
	}
	for i := 0; i < 2; i++ {
		if err := d.DeleteViewerNote(PlatformTwitch, "", "u1"); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
}

func TestViewerNoteCaps(t *testing.T) {
	d := testDB(t)
	long := strings.Repeat("é", MaxViewerNoteLen)
	if err := d.PutViewerNote(ViewerNote{Platform: PlatformX, AuthorID: "u", Note: long}); err != nil {
		t.Fatalf("exactly the cap must store: %v", err)
	}
	cases := map[string]ViewerNote{
		"note":    {Platform: PlatformX, AuthorID: "u", Note: long + "x"},
		"tagLen":  {Platform: PlatformX, AuthorID: "u", Tags: []string{strings.Repeat("a", MaxViewerTagLen+1)}},
		"tagsMax": {Platform: PlatformX, AuthorID: "u", Tags: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}},
	}
	for name, n := range cases {
		if err := d.PutViewerNote(n); !errors.Is(err, ErrViewerNoteInvalid) {
			t.Errorf("%s: err = %v, want ErrViewerNoteInvalid", name, err)
		}
	}
}

func TestListViewerTagsCarriesNoNoteText(t *testing.T) {
	d := testDB(t)
	_ = d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, AuthorID: "u1", Note: "secret words", Tags: []string{"watch"}})
	_ = d.PutViewerNote(ViewerNote{Platform: PlatformTwitch, AuthorID: "u2", Note: "only a note"})
	got, err := d.ListViewerTags()
	if err != nil || len(got) != 1 || got[0].AuthorID != "u1" || got[0].Tags[0] != "watch" {
		t.Fatalf("ListViewerTags = %+v err=%v", got, err)
	}
}
