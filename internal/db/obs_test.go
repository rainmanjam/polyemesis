package db

import (
	"strings"
	"testing"
)

func TestOBSPasswordIsSealedAndClearable(t *testing.T) {
	d, box := testDB(t), testBox(t)

	if pw, err := d.GetOBSPassword(box); err != nil || pw != "" {
		t.Fatalf("a fresh install returned (%q, %v), want (\"\", nil); obs-websocket without auth is a normal LAN setup", pw, err)
	}
	secret := "obs-ws-" + strings.Repeat("p4ss", 4)
	if err := d.PutOBSPassword(box, secret); err != nil {
		t.Fatalf("PutOBSPassword: %v", err)
	}
	if got, err := d.GetOBSPassword(box); err != nil || got != secret {
		t.Fatalf("GetOBSPassword = (%q, %v), want the password back", got, err)
	}
	if has, _ := d.HasOBSPassword(); !has {
		t.Error("HasOBSPassword = false after storing one")
	}
	// The stored bytes are not the password: without this the round trip
	// passes just as happily against plaintext.
	var raw []byte
	if err := d.sql.QueryRow(`SELECT password_enc FROM obs_creds WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("reading the raw column: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the obs-websocket password is stored in plaintext")
	}
	if err := d.PutOBSPassword(box, ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if has, _ := d.HasOBSPassword(); has {
		t.Error("the password survived being cleared")
	}
}

func TestOBSConfigRoundTripsAndIsAbsentOnAFreshInstall(t *testing.T) {
	d := testDB(t)
	if doc, err := d.GetOBSConfig(); err != nil || doc != nil {
		t.Fatalf("fresh install = (%q, %v), want (nil, nil): no row is a bridge that is off", doc, err)
	}
	for _, doc := range []string{`{"enabled":true}`, `{"enabled":false,"scenes":[]}`} {
		if err := d.PutOBSConfig([]byte(doc)); err != nil {
			t.Fatal(err)
		}
		if got, err := d.GetOBSConfig(); err != nil || string(got) != doc {
			t.Fatalf("GetOBSConfig = (%q, %v), want %q", got, err, doc)
		}
	}
}
