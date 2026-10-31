package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// The last credential verdict is stored beside the pair it was reached about
// (settings-council R10).
//
// The settings page used to forget the verdict the moment it was drawn: the
// check ran on Save and on Re-check, its answer lived in one component's state,
// and a reload showed "configured" over a pair nobody could tell had been
// accepted or refused. These tests hold the storage half of the fix.

// A verdict recorded against a stored pair comes back out of the list the
// settings page reads, with when it was reached.
func TestARecordedCredentialVerdictIsListedWithTheTimeItWasReached(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	if err := d.PutPlatformCreds(box, PlatformTwitch, "an-id", "a-secret"); err != nil {
		t.Fatalf("put: %v", err)
	}

	at := time.Unix(1_800_000_000, 0)
	if err := d.RecordPlatformCredsCheck(PlatformTwitch, "rejected", "client_credentials",
		"The platform refused these credentials.", at); err != nil {
		t.Fatalf("record: %v", err)
	}

	list, err := d.ListPlatformCreds()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].LastCheck == nil {
		t.Fatalf("list = %+v, want one row carrying its last verdict", list)
	}
	got := list[0].LastCheck
	if got.State != "rejected" || got.Method != "client_credentials" ||
		got.Detail != "The platform refused these credentials." || !got.CheckedAt.Equal(at) {
		t.Fatalf("last check = %+v, want the verdict recorded at %v", got, at)
	}
}

// A pair that has never been checked says nothing, rather than a zero-value
// verdict the page would have to know to ignore.
func TestAPairThatWasNeverCheckedListsNoVerdict(t *testing.T) {
	d := testDB(t)
	if err := d.PutPlatformCreds(testBox(t), PlatformKick, "an-id", "a-secret"); err != nil {
		t.Fatalf("put: %v", err)
	}
	list, err := d.ListPlatformCreds()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].LastCheck != nil {
		t.Fatalf("list = %+v, want no verdict for a pair that was never checked", list)
	}
}

// SAVING A NEW PAIR DROPS THE OLD PAIR'S VERDICT. The verdict is about the
// pair it was reached on; carrying "verified" over to a pair that was just
// pasted would show a tick nobody earned. The handler records the new pair's
// own verdict straight after, so the gap is the length of one request.
func TestSavingANewPairDropsTheVerdictTheOldPairEarned(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	if err := d.PutPlatformCreds(box, PlatformTwitch, "old-id", "old-secret"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := d.RecordPlatformCredsCheck(PlatformTwitch, "verified", "client_credentials",
		"ok", time.Now()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := d.PutPlatformCreds(box, PlatformTwitch, "new-id", "new-secret"); err != nil {
		t.Fatalf("second put: %v", err)
	}
	list, err := d.ListPlatformCreds()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].LastCheck != nil {
		t.Fatalf("the new pair inherited the old pair's verdict: %+v", list[0].LastCheck)
	}
}

// Recording against a platform with nothing stored is refused rather than
// creating a verdict for no pair.
func TestRecordingAVerdictWithNoStoredPairIsNotFound(t *testing.T) {
	d := testDB(t)
	err := d.RecordPlatformCredsCheck(PlatformYouTube, "verified", "format", "", time.Now())
	if err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The upgrade path: platform_creds as it existed before the verdict columns,
// holding a pair an operator already saved. The migration adds the columns,
// the stored pair survives, and it lists with no verdict.
func TestMigratePlatformCredsCheckKeepsAnExistingPairAndAddsTheColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE platform_creds (
			platform          TEXT PRIMARY KEY,
			client_id         TEXT NOT NULL,
			client_secret_enc BLOB NOT NULL,
			updated_at        INTEGER NOT NULL
		);
		INSERT INTO platform_creds VALUES ('twitch', 'kept-id', x'00', 1);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	d := &DB{}
	d.sql, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.sql.Close()

	if has, err := columnExists(d.sql, "platform_creds", "check_state"); err != nil || has {
		t.Fatalf("the fixture already had check_state (has=%v, err=%v); this test would prove nothing", has, err)
	}
	if err := d.MigratePlatformCredsCheck(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Twice, because Open runs every migration on every start.
	if err := d.MigratePlatformCredsCheck(); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
	list, err := d.ListPlatformCreds()
	if err != nil {
		t.Fatalf("list after migration: %v", err)
	}
	if len(list) != 1 || list[0].ClientID != "kept-id" || list[0].LastCheck != nil {
		t.Fatalf("list = %+v, want the stored pair with no verdict", list)
	}
}
