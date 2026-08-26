package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A PLATFORM CAN SUPPLY THE KEY AND NOT THE URL, and until Trovo landed nothing
// in this package had to hold that.
//
// handleRefreshKey assigned `dest.URL = b.Ingest.URL` unconditionally, which was
// right while every provider answered with both fields. Trovo answers with one:
// it publishes the stream key on its channel resource and publishes the ingest
// hostname nowhere at all -- the host is regional and lives only in the creator
// dashboard, so the operator copies it across once and it must survive every key
// refresh afterwards.
//
// Mutation run against both tests: restore the unconditional
// `dest.URL = b.Ingest.URL`. Observed FAIL on both --
// `refresh-key: status 400, body {"error":"invalid destination: an RTMP URL is
// required"}`. That message is the whole complaint in one line: the operator
// pressed Refresh stream key, the URL they pasted was overwritten with nothing,
// and the product told them a field they had filled in was missing.
func TestRefreshKeyKeepsAPastedIngestURLWhenThePlatformPublishesNone(t *testing.T) {
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})

	acctID := connectAccount(t, store, s.box, db.PlatformTrovo, "leafinsummer")
	if err := store.PutPlatformCreds(s.box, db.PlatformTrovo, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	const pasted = "rtmp://livepush.trovo.live/live"
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Trovo", Kind: db.DestRTMP, Platform: db.PlatformTrovo,
		URL: pasted, StreamKey: "", AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	got := refreshKey(t, h, sign, dest.ID)

	if got.StreamKey != "live/sk-from-trovo" {
		t.Errorf("stream key = %q, want the one Trovo returned", got.StreamKey)
	}
	if got.URL != pasted {
		t.Errorf("refresh-key blanked the ingest URL the operator pasted: %q, want %q.\n"+
			"Trovo publishes no ingest URL, so overwriting with what the provider "+
			"returned leaves the destination unable to publish -- and the failure "+
			"arrives as \"an RTMP URL is required\" from a button labelled Refresh "+
			"stream key.", got.URL, pasted)
	}
}

// The complement, and the reason the handler branches rather than just skipping
// the assignment: a destination that has NO url yet and a platform that will
// never supply one is a dead end, and it has to be named as one.
//
// Without the branch this is a 400 saying "an RTMP URL is required", which is
// true, arrives from a Refresh stream key button, and tells the operator
// nothing about which field to go and find.
func TestRefreshKeySaysWhichFieldIsMissingWhenThePlatformPublishesNoIngestURL(t *testing.T) {
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})

	acctID := connectAccount(t, store, s.box, db.PlatformTrovo, "leafinsummer")
	if err := store.PutPlatformCreds(s.box, db.PlatformTrovo, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	// Saved with a URL, because Validate refuses one without; then emptied
	// underneath, which is the state a destination created from the Trovo
	// preset (url: "") reaches before anything has been pasted into it.
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Trovo", Kind: db.DestRTMP, Platform: db.PlatformTrovo,
		URL: "rtmp://placeholder.invalid/live", AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if _, err := store.SQL().Exec(`UPDATE destinations SET url = '' WHERE id = ?`, dest.ID); err != nil {
		t.Fatalf("blank the url: %v", err)
	}

	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "does not publish an ingest URL") {
		t.Errorf("the refusal does not say which field is missing: %s", body)
	}
}
