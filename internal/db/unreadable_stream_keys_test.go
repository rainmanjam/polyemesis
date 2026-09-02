package db

import (
	"path/filepath"
	"testing"
)

// THE RESTORE THAT LOOKS LIKE A SUCCESS.
//
// secrets.LoadOrCreate mints a fresh random key whenever secret.key is absent
// and says nothing about it, which is right on a first boot and wrong on a data
// directory restored from a backup that copied polyemesis.db and not the key
// beside it. Every destination then holds a stream key sealed under a secret
// that no longer exists. Nothing errors, nothing logs, and the install reads as
// restored until somebody tries to go live.
//
// UnreadableStreamKeys is the measurement that makes that state sayable. It is
// deliberately a question about the CONSEQUENCE rather than about whether a key
// was minted: it also catches a key that is present and wrong, and it is silent
// on the fresh install where minting is the correct thing to have happened.
func TestUnreadableStreamKeysNamesTheDestinationsAWrongSecretStrands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	sealed := keyDB(t, path, WithSecretBox(testBox(t)))
	first := validDest()
	first.Name = "Twitch main"
	if _, err := sealed.CreateDestination(first); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	second := validDest()
	second.Name = "YouTube backup"
	if _, err := sealed.CreateDestination(second); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The same database, opened with the key a silent mint would have produced.
	restored := keyDB(t, path, WithSecretBox(otherBox(t)))
	names, err := restored.UnreadableStreamKeys()
	if err != nil {
		t.Fatalf("UnreadableStreamKeys: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("UnreadableStreamKeys = %v, want both destinations -- a restore that "+
			"lost secret.key strands every sealed key", names)
	}
	for i, want := range []string{"Twitch main", "YouTube backup"} {
		if names[i] != want {
			t.Errorf("names[%d] = %q, want %q; the operator needs the names, not just a count",
				i, names[i], want)
		}
	}
}

// The control on the other side: the ordinary install, where the key opens what
// it sealed, must report nothing at all. A boot-time warning that cries wolf on
// every healthy start is a warning nobody reads by the time it is true.
func TestUnreadableStreamKeysIsSilentWhenTheSecretIsTheRightOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	sealed := keyDB(t, path, WithSecretBox(box))
	if _, err := sealed.CreateDestination(validDest()); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again := keyDB(t, path, WithSecretBox(box))
	names, err := again.UnreadableStreamKeys()
	if err != nil {
		t.Fatalf("UnreadableStreamKeys: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("UnreadableStreamKeys = %v on an install whose key is the right one", names)
	}
}

// An install with no box at all is the pre-encryption shape, and its keys are
// in the plaintext column where they have always been. Nothing is unreadable.
func TestUnreadableStreamKeysIsSilentOnAnInstallWithNoSecretAtAll(t *testing.T) {
	d := testDB(t)
	if _, err := d.CreateDestination(validDest()); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	names, err := d.UnreadableStreamKeys()
	if err != nil {
		t.Fatalf("UnreadableStreamKeys: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("UnreadableStreamKeys = %v on an install that never sealed anything", names)
	}
}
