package db

import (
	"strings"
	"testing"
)

func storeEndpoints(t *testing.T, d *DB, primary, secondary string) {
	t.Helper()
	s := mergeBaseSettings()
	s.Automod.Model.Endpoint, s.Automod.Secondary.Endpoint = primary, secondary
	if err := d.PutSettings(s); err != nil {
		t.Fatal(err)
	}
}

func moveEndpoint(d *DB, slot, to string) error {
	_, err := d.UpdateSettings(func(s *Settings) error {
		if slot == AutomodSlotSecondary {
			s.Automod.Secondary.Endpoint = to
		} else {
			s.Automod.Model.Endpoint = to
		}
		return nil
	})
	return err
}

// A key is bound to the endpoint stored when it is set, and a settings write
// that moves the endpoint clears it in the same write -- while one that leaves
// the endpoint alone (even spelled differently) keeps it.
func TestAKeyIsBoundToItsEndpointAndAMoveClearsIt(t *testing.T) {
	d, box := testDB(t), testBox(t)
	storeEndpoints(t, d, "https://api.typesafe.ai/v1/systemone", "https://a.example/v1")
	if err := d.PutAutomodKeyFor(box, AutomodSlotSecondary, "k-"+"fake"); err != nil {
		t.Fatal(err)
	}
	if _, b, _ := d.GetAutomodKeyBound(box, AutomodSlotSecondary); b != NormalizeEndpoint("https://a.example/v1") {
		t.Fatalf("binding = %q", b)
	}
	if err := moveEndpoint(d, AutomodSlotSecondary, "HTTPS://A.EXAMPLE:443/v1/"); err != nil {
		t.Fatal(err)
	}
	if has, _ := d.HasAutomodKeyFor(AutomodSlotSecondary); !has {
		t.Fatal("respelling the same endpoint cleared the key")
	}
	if err := moveEndpoint(d, AutomodSlotSecondary, "https://b.example/v1"); err != nil {
		t.Fatal(err)
	}
	if has, _ := d.HasAutomodKeyFor(AutomodSlotSecondary); has {
		t.Fatal("the key survived its endpoint moving")
	}
	if has, _ := d.HasAutomodKeyFor("primary"); has {
		t.Fatal("test setup: the primary had no key to begin with")
	}
}

// A CLEAR THAT FAILS ROLLS THE WHOLE WRITE BACK. Logging and continuing would
// leave the new endpoint stored beside the old endpoint's key -- the pairing
// the binding exists to prevent -- so the settings write itself fails.
func TestAKeyThatCannotBeClearedRollsTheEndpointChangeBack(t *testing.T) {
	d, box := testDB(t), testBox(t)
	storeEndpoints(t, d, "https://a.example/v1", "")
	if err := d.PutAutomodKeyFor(box, "primary", "k-"+"fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`CREATE TRIGGER refuse_clear BEFORE DELETE ON automod_creds
		BEGIN SELECT RAISE(ABORT, 'injected clear failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := moveEndpoint(d, "primary", "https://b.example/v1")
	if err == nil || !strings.Contains(err.Error(), "injected clear failure") {
		t.Fatalf("the write succeeded over a failed clear: %v", err)
	}
	s, _ := d.GetSettings()
	if s.Automod.Model.Endpoint != "https://a.example/v1" {
		t.Fatalf("the endpoint change was not rolled back: %q", s.Automod.Model.Endpoint)
	}
	if _, b, _ := d.GetAutomodKeyBound(box, "primary"); b != NormalizeEndpoint("https://a.example/v1") {
		t.Fatalf("the key's binding moved: %q", b)
	}
}

// A key that passed a Test against an UNSAVED address is held pending, bound
// to it; a write moving the checker to exactly that address adopts it, and a
// write moving it anywhere else discards it.
func TestAPendingKeyIsAdoptedOnlyByAMoveToItsOwnEndpoint(t *testing.T) {
	d, box := testDB(t), testBox(t)
	storeEndpoints(t, d, "https://a.example/v1", "")
	d.PutAutomodKeyFor(box, "primary", "old-"+"fake")
	pending, err := d.SealTestedAutomodKey(box, "primary", "new-"+"fake", NormalizeEndpoint("https://b.example/v1"))
	if err != nil || !pending {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if k, _ := d.GetAutomodKeyFor(box, "primary"); k != "old-"+"fake" {
		t.Fatal("a pending key replaced the checker's key before the address was saved")
	}
	if err := moveEndpoint(d, "primary", "https://c.example/v1"); err != nil {
		t.Fatal(err)
	}
	if has, _ := d.HasAutomodKeyFor("primary"); has {
		t.Fatal("moving to an address with no key for it kept a key")
	}
	if k, _ := d.GetAutomodPendingKey(box, "primary"); k != "" {
		t.Fatal("the pending key outlived a move to a different address")
	}

	d.SealTestedAutomodKey(box, "primary", "new-"+"fake", NormalizeEndpoint("https://b.example/v1"))
	if err := moveEndpoint(d, "primary", "https://b.example/v1/"); err != nil {
		t.Fatal(err)
	}
	if k, b, _ := d.GetAutomodKeyBound(box, "primary"); k != "new-"+"fake" || b != NormalizeEndpoint("https://b.example/v1") {
		t.Fatalf("the pending key was not adopted for its own address: %q bound %q", k, b)
	}
	// A typed key for the SAVED address becomes the key directly.
	pending, _ = d.SealTestedAutomodKey(box, "primary", "direct-"+"fake", NormalizeEndpoint("https://b.example/v1"))
	if k, _ := d.GetAutomodKeyFor(box, "primary"); pending || k != "direct-"+"fake" {
		t.Fatalf("pending=%v key=%q", pending, k)
	}
}

// THE UPGRADE. A key stored before bindings existed is bound, once, to the
// endpoint stored at upgrade time -- the one it has been sent to all along --
// and a database without the column gains it.
func TestTheMigrationBindsKeysStoredBeforeBindingsExisted(t *testing.T) {
	d, box := testDB(t), testBox(t)
	storeEndpoints(t, d, "https://api.openai.com/v1/chat/completions", "")
	enc, _ := box.Seal("legacy-" + "fake")
	if _, err := d.sql.Exec(`ALTER TABLE automod_creds DROP COLUMN endpoint_fp`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`INSERT INTO automod_creds (id, key_enc, updated_at) VALUES (1, ?, 0)`, enc); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateAutomodKeyBinding(); err != nil {
		t.Fatal(err)
	}
	k, b, err := d.GetAutomodKeyBound(box, "primary")
	if err != nil || k != "legacy-"+"fake" || b != NormalizeEndpoint("https://api.openai.com/v1/chat/completions") {
		t.Fatalf("key %q bound %q err %v", k, b, err)
	}
	// One-time: a later run does not re-bind to whatever is stored then.
	d.sql.Exec(`UPDATE settings SET json = json_set(json, '$.automod.model.endpoint', 'https://evil.example/v1') WHERE id = 1`)
	if err := d.MigrateAutomodKeyBinding(); err != nil {
		t.Fatal(err)
	}
	if _, b2, _ := d.GetAutomodKeyBound(box, "primary"); b2 != b {
		t.Fatalf("a second run re-bound the key to %q", b2)
	}
}

func TestNormalizeEndpointRefusesWhatCannotBeComparedSafely(t *testing.T) {
	for _, bad := range []string{"", "ftp://h/x", "https://user:pw@h/x", "https://h/x#frag", "not a url", "/relative"} {
		if NormalizeEndpoint(bad) != "" {
			t.Errorf("%q normalised to %q", bad, NormalizeEndpoint(bad))
		}
	}
	if NormalizeEndpoint("HTTP://Host/x/") != NormalizeEndpoint("http://host:80/x") {
		t.Error("equivalent spellings differ")
	}
	if NormalizeEndpoint("https://h/x?a=1") == NormalizeEndpoint("https://h/x?a=2") {
		t.Error("different queries matched")
	}
}
