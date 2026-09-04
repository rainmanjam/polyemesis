package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// testBox comes from db_test.go; one per package.

func validHook() *hooks.Hook {
	return &hooks.Hook{
		Name: "deploy", Enabled: true,
		URL:      "https://hooks.example.com/services/T0/B1/XXXXsecretXXXX",
		Triggers: []hooks.Trigger{hooks.TriggerIngestPublished},
	}
}

func TestHookRoundTrips(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if plaintext == "" {
		t.Fatal("no plaintext secret returned; the operator can never see it " +
			"again, so create is the only chance to hand it over")
	}
	got, err := d.GetHook(box, created.ID)
	if err != nil {
		t.Fatalf("GetHook: %v", err)
	}
	if got.URL != validHook().URL {
		t.Errorf("url = %q, want the stored one", got.URL)
	}
	if got.Secret != plaintext {
		t.Errorf("secret did not survive the round trip; every signature this " +
			"hook sends would be unverifiable")
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != hooks.TriggerIngestPublished {
		t.Errorf("triggers = %v, want [ingest.published]", got.Triggers)
	}
}

// The secret is sealed, not stored in the clear. A database file copied off a
// backup drive must not hand somebody the ability to forge deliveries.
func TestHookSecretIsSealedOnDisk(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := d.SQL().QueryRow(`SELECT secret FROM hooks WHERE id = ?`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Fatal("the signing secret is stored in the clear")
	}
}

// An operator supplying their own secret must get theirs, not a generated one:
// they are pasting it into the receiver at the same moment.
func TestASuppliedSecretIsKept(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	h := validHook()
	h.Secret = "my-own-key"
	created, plaintext, err := d.CreateHook(box, h)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "my-own-key" {
		t.Fatalf("plaintext = %q, want the supplied secret", plaintext)
	}
	got, _ := d.GetHook(box, created.ID)
	if got.Secret != "my-own-key" {
		t.Fatalf("stored secret = %q, want the supplied one", got.Secret)
	}
}

func TestEnabledHooksReturnsOnlyTheEnabledOnes(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	on, _, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	off := validHook()
	off.Name, off.Enabled = "off", false
	if _, _, err := d.CreateHook(box, off); err != nil {
		t.Fatal(err)
	}

	all, err := d.ListHooks(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListHooks = %d, want both", len(all))
	}
	live, err := d.EnabledHooks(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != on.ID {
		t.Fatalf("EnabledHooks = %+v, want only the enabled one", live)
	}
}

func TestUpdateHookWithNoSecretKeepsTheStoredOne(t *testing.T) {
	// The UI never renders the secret, so an edit form submits an empty one.
	// Overwriting the stored key with "" would silently unsign every future
	// delivery, and the receiver would start rejecting them with no error here.
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	edit := *created
	edit.Name, edit.Secret = "renamed", ""
	if _, err := d.UpdateHook(box, &edit); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}
	got, _ := d.GetHook(box, created.ID)
	if got.Secret != plaintext {
		t.Fatalf("secret = %q after an edit that did not mention it, want the "+
			"stored one", got.Secret)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}
}

func TestHookValidationRunsOnWrite(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	bad := validHook()
	bad.URL = "ftp://example.com/x"
	if _, _, err := d.CreateHook(box, bad); err == nil {
		t.Fatal("CreateHook accepted an ftp endpoint")
	}
}

// DELETING A HOOK, AND SAYING SO WHEN THERE WAS NOTHING TO DELETE.
//
// DeleteHook was at 0% coverage while every other operation on the table was
// tested. Both halves matter to a caller: the row has to actually go, and an id
// that names nothing has to come back as ErrNotFound rather than as success --
// the API turns that distinction into 404 versus 204, and an operator who
// deletes a hook twice should be told the second one was not there rather than
// believing they removed a delivery that is still firing.
func TestDeletingAHookRemovesItAndSaysSoWhenThereWasNothingToRemove(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	created, _, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if err := d.DeleteHook(created.ID); err != nil {
		t.Fatalf("DeleteHook: %v", err)
	}
	if _, err := d.GetHook(box, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete, GetHook = %v, want ErrNotFound -- the row is still there", err)
	}

	// The same id again. A delete that reports success for a row it did not
	// touch tells an operator a delivery has stopped when it has not.
	if err := d.DeleteHook(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a hook twice returned %v, want ErrNotFound", err)
	}
	if err := d.DeleteHook(999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting an id that never existed returned %v, want ErrNotFound", err)
	}
}

// AN UPDATE THAT COLLIDES WITH ANOTHER HOOK'S NAME IS REFUSED, FOLDED.
//
// UpdateHook checks this in Go rather than with a UNIQUE index, deliberately:
// the comparison is case- and space-folded, because "Disk " and "disk" are
// indistinguishable on screen and that is the whole harm, while SQLite's NOCASE
// collation is ASCII-only. A test that only tried an exact duplicate would pass
// against a plain unique index and prove nothing about the folding.
func TestRenamingAHookOntoAnothersNameIsRefusedEvenWhenOnlyCaseOrSpaceDiffers(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	first, _, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	second := validHook()
	second.Name = "alerts"
	created, _, err := d.CreateHook(box, second)
	if err != nil {
		t.Fatalf("CreateHook second: %v", err)
	}

	for _, name := range []string{first.Name, strings.ToUpper(first.Name), " " + first.Name + " "} {
		attempt := validHook()
		attempt.ID = created.ID
		attempt.Name = name
		if _, err := d.UpdateHook(box, attempt); err == nil {
			t.Errorf("renaming to %q was allowed; the list is the only thing telling two "+
				"hooks apart, and the one an operator disables may not be the one that "+
				"has been firing", name)
		}
	}

	// And its own name is not a collision with itself.
	keep := validHook()
	keep.ID = created.ID
	keep.Name = second.Name
	if _, err := d.UpdateHook(box, keep); err != nil {
		t.Fatalf("a hook could not keep its own name: %v", err)
	}
}

// UPDATING A HOOK THAT IS NOT THERE IS ErrNotFound, NOT A SILENT NO-OP.
func TestUpdatingAHookThatDoesNotExistIsNotFound(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	h := validHook()
	h.ID = 4242
	if _, err := d.UpdateHook(box, h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateHook on a missing row = %v, want ErrNotFound. A caller told "+
			"the update succeeded believes a delivery was reconfigured when nothing "+
			"was written.", err)
	}
}
