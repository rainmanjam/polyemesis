package db

import (
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
