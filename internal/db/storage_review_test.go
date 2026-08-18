package db

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/hooks"
)

/* EDITING A HOOK DESTROYED A SECRET THAT WAS ONLY UNREADABLE.
 *
 * Two deliberate designs compose badly. scanHook swallows a decrypt failure and
 * leaves Secret empty, so one unopenable row cannot fail the whole listing.
 * UpdateHook reads an empty Secret as "keep the stored one", and honoured that
 * by calling GetHook and re-sealing what it returned.
 *
 * When the ciphertext will not open -- a restore against the wrong key, a
 * half-finished rotation -- GetHook returns "". The update seals THAT. Bytes
 * that would have opened again once the right box came back are replaced by a
 * valid seal of the empty string, permanently. Renaming the hook was enough.
 */
func TestEditingAHookDoesNotDestroyASecretItCannotRead(t *testing.T) {
	d := keyDB(t, t.TempDir()+"/h.db")

	// Sealed by one box, edited under another: the state after a restore with
	// the wrong key, which is exactly when scanHook's tolerance matters.
	original := testBox(t)
	created, _, err := d.CreateHook(original, &hooks.Hook{
		Name: "receiver", URL: "https://example.invalid/hook", Secret: "s3cret-value",
	})
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	sealedBefore := rawHookSecret(t, d, created.ID)
	if len(sealedBefore) == 0 {
		t.Fatal("fixture: no ciphertext was stored, so this proves nothing")
	}

	stranger := testBox(t)
	if _, err := d.GetHook(stranger, created.ID); err != nil {
		t.Fatalf("GetHook under the wrong box should degrade, not fail: %v", err)
	}

	// The operator renames it. They did not touch the secret.
	created.Name = "receiver (renamed)"
	created.Secret = ""
	if _, err := d.UpdateHook(stranger, created); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}

	if got := rawHookSecret(t, d, created.ID); string(got) != string(sealedBefore) {
		t.Fatalf("the stored ciphertext changed during a rename. The unreadable "+
			"secret was re-sealed as the empty string, so the original can never "+
			"be recovered even with the right key.\n before %d bytes\n after  %d bytes",
			len(sealedBefore), len(got))
	}

	// And the original box still opens it, which is the whole point.
	back, err := d.GetHook(original, created.ID)
	if err != nil {
		t.Fatalf("GetHook under the original box: %v", err)
	}
	if back.Secret != "s3cret-value" {
		t.Errorf("secret came back as %q, want the original — the row survived "+
			"the edit but the value did not", back.Secret)
	}
	if back.Name != "receiver (renamed)" {
		t.Errorf("the rename did not land: name is %q", back.Name)
	}
}

// The keep-path must not stop a deliberate secret change from landing.
func TestSettingAHookSecretStillReplacesIt(t *testing.T) {
	d := keyDB(t, t.TempDir()+"/h2.db")
	box := testBox(t)

	h, _, err := d.CreateHook(box, &hooks.Hook{
		Name: "r", URL: "https://example.invalid/h", Secret: "first",
	})
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	h.Secret = "second"
	if _, err := d.UpdateHook(box, h); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}
	got, err := d.GetHook(box, h.ID)
	if err != nil {
		t.Fatalf("GetHook: %v", err)
	}
	if got.Secret != "second" {
		t.Errorf("secret is %q, want %q — keeping the old ciphertext must not "+
			"swallow a rotation the operator asked for", got.Secret, "second")
	}
}

func rawHookSecret(t *testing.T, d *DB, id int64) []byte {
	t.Helper()
	var b []byte
	if err := d.sql.QueryRow(`SELECT secret FROM hooks WHERE id = ?`, id).Scan(&b); err != nil {
		t.Fatalf("read raw secret: %v", err)
	}
	return b
}

/* THE BULK MOVE LEFT THE SESSION IT TOOK FROM REPORTING RECORDINGS IT NO
 * LONGER HAD.
 *
 * SetSessionRecordings upserts each recording with
 * ON CONFLICT(recording_id) DO UPDATE SET session_id=excluded.session_id, so a
 * recording that belonged to another session is STOLEN from it. Only the
 * destination was recalculated, so the donor kept its cached count and span.
 *
 * TestMovingARecordingRecomputesBothSessions already pins this for the
 * one-at-a-time path, and AddRecordingToSession says why in its own comment:
 * "The session it came from is now shorter and its span is stale." The bulk
 * path is the same steal and never did it. The library then showed a session
 * claiming recordings that are visibly filed under a different one.
 */
func TestSettingSessionRecordingsRecomputesTheSessionsItTookFrom(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	c := seedRecordingAt(t, d, "rec-2.mkv", base.Add(2*time.Hour), time.Hour)

	one, _ := d.CreateSession(Metadata{Title: "one"}, false)
	two, _ := d.CreateSession(Metadata{Title: "two"}, false)
	if err := d.SetSessionRecordings(one.ID, []int64{a, b, c}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// The bulk edit an operator makes when re-filing a broadcast: two of the
	// three move across in one call.
	if err := d.SetSessionRecordings(two.ID, []int64{b, c}); err != nil {
		t.Fatalf("bulk move: %v", err)
	}

	gotOne, _ := d.GetSession(one.ID)
	gotTwo, _ := d.GetSession(two.ID)
	if gotOne.Recordings != 1 {
		t.Errorf("the session they were taken from reports %d recordings, want 1 — "+
			"its cached span was never recomputed, so the library shows it "+
			"claiming recordings that are now filed under another session",
			gotOne.Recordings)
	}
	if gotTwo.Recordings != 2 {
		t.Errorf("the destination reports %d recordings, want 2", gotTwo.Recordings)
	}
}
