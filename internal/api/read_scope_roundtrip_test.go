package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The guard against the failure mode that would be WORSE than the leak.
//
// Every rejected design for this fix -- a redacting MarshalJSON on db.Settings,
// a json:"-" on the credential fields, a Secret string type -- shares one
// property: the redaction becomes part of how the type serialises, and these
// types are marshalled for STORAGE as well as for the wire. A settings page
// that reads, edits one unrelated field and writes back would then persist the
// mask, and the operator's SRT passphrase would be gone from the database with
// no error anywhere.
//
// That is not a hypothetical. The settings and destination editors are
// read-modify-write, the PUT side refuses unknown fields, and the acceptance
// drivers do exactly the same round trip from a shell script.
//
// So this test does the round trip against the real router and asserts the
// credentials come back BYTE-IDENTICAL. It is deliberately a session, because
// the session is the principal that can write: a read token is refused the PUT,
// which is why blanking VALUES rather than reshaping the body is safe.

func TestSettingsSurviveAReadModifyWriteFromTheConsole(t *testing.T) {
	h, _, sign := plantedServer(t)

	get := func() map[string]any {
		t.Helper()
		r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /settings: %d %s", w.Code, w.Body.String())
		}
		var doc map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		return doc
	}

	before := get()

	// Straight back, unmodified. The UI sends the whole document on save, and
	// the PUT side uses DisallowUnknownFields, so a response whose SHAPE had
	// changed would 400 right here rather than at some later release.
	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", before)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /settings echoed straight back was refused: %d %s",
			w.Code, w.Body.String())
	}

	after := get()

	for _, path := range [][]string{
		{"ingest", "srt", "passphrase"},
		{"ingest", "rtmp", "streamKey"},
		{"ingest", "pull", "url"},
		{"failover", "backup", "srt", "passphrase"},
		{"failover", "backup", "rtmp", "streamKey"},
		{"failover", "backup", "pull", "url"},
		{"mqtt", "brokerUrl"},
	} {
		wantV := dig(t, before, path)
		gotV := dig(t, after, path)
		if wantV != gotV {
			t.Errorf("%v changed across a read-modify-write: %q -> %q. A redaction that "+
				"reaches the STORE erases the operator's credential silently.",
				path, wantV, gotV)
		}
		if wantV == "" {
			t.Errorf("%v was empty before the round trip, so this assertion proves "+
				"nothing; the fixture must plant a value here", path)
		}
	}
}

func TestDestinationSurvivesAReadModifyWriteFromTheConsole(t *testing.T) {
	h, _, sign := plantedServer(t)

	get := func() map[string]any {
		t.Helper()
		r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/1", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /destinations/1: %d %s", w.Code, w.Body.String())
		}
		var doc struct {
			Destination map[string]any `json:"destination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode destination: %v", err)
		}
		return doc.Destination
	}

	before := get()
	if before["streamKey"] != sentinelDestKey {
		t.Fatalf("the console did not receive the stream key it is meant to show: %v",
			before["streamKey"])
	}

	// A rename, which is what the destination editor actually sends: everything
	// the GET returned, with one field changed.
	edited := map[string]any{}
	for k, v := range before {
		edited[k] = v
	}
	edited["name"] = "twitch renamed"

	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/1", edited)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /destinations/1: %d %s", w.Code, w.Body.String())
	}

	after := get()
	if after["name"] != "twitch renamed" {
		t.Errorf("the rename did not take: %v", after["name"])
	}
	if after["streamKey"] != sentinelDestKey {
		t.Errorf("streamKey changed across a rename: %v -> %v; the stored publish "+
			"credential must survive an edit to an unrelated field",
			sentinelDestKey, after["streamKey"])
	}
	if after["backupStreamKey"] != sentinelDestBackupKey {
		t.Errorf("backupStreamKey changed across a rename: %v -> %v",
			sentinelDestBackupKey, after["backupStreamKey"])
	}
}

// dig walks a decoded JSON document to a leaf string.
func dig(t *testing.T, doc map[string]any, path []string) string {
	t.Helper()
	cur := any(doc)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %q is not an object", path, key)
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("%v: no key %q", path, key)
		}
	}
	s, ok := cur.(string)
	if !ok {
		t.Fatalf("%v is not a string: %T", path, cur)
	}
	return s
}
