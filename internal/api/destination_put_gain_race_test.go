package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// PUT /destinations/{id} and POST .../tracks/{n}/gain are both read-modify-
// writes of the whole row. Unserialised, a mute stored between the PUT's read
// and its write was written back over by the PUT -- one that never mentioned
// the profile at all -- and the operator's mute was gone while both requests
// answered 200.
//
// Made DETERMINISTIC by holding the PUT between its read and its write: the
// first save that turns on a Facebook backup ingest calls Facebook in exactly
// that gap, and the platform stub blocks the call until the test releases it.
// The mute is sent while the PUT is held.
//
// MUTATION: remove the trackGainMu Lock/Unlock from handleUpdateDestination.
// Observed: FAIL, "the PUT wrote back over a mute stored during it".
func TestAPutCannotUndoAMuteStoredWhileItRuns(t *testing.T) {
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})
	acct := linkFacebookAccount(t, s, store)
	src := onlySourceID(t, h, sign)
	dest, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp", StreamKey: "778899",
		Enabled: false, AccountID: &acct, AudioBitrate: 160, SourceID: &src,
		Profile: routing.Profile{
			Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
			Tracks: []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}},
		},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	held, release := make(chan struct{}), make(chan struct{})
	stub.setOnCall(func(c stubCall) {
		if strings.HasSuffix(c.Path, "/input_streams") {
			close(held)
			<-release
		}
	})

	put := make(chan struct{})
	go func() {
		defer close(put)
		// Turning on the backup is what sends the PUT to Facebook mid-write.
		// The body says nothing about the profile.
		r := jsonRequest(t, http.MethodPut, destPath(dest.ID), map[string]any{"backupIngestWanted": true})
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Errorf("PUT = %d: %s", w.Code, w.Body.String())
		}
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the PUT never reached Facebook; the race is not being staged")
	}

	muted := make(chan struct{})
	go func() {
		defer close(muted)
		r := jsonRequest(t, http.MethodPost, trackGainPath(dest.ID, 0), map[string]any{"muted": true})
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Errorf("gain = %d: %s", w.Code, w.Body.String())
		}
	}()
	// Unserialised, the mute lands now, inside the PUT's read-to-write gap.
	// Serialised, it waits for the PUT. Either way give it the chance.
	select {
	case <-muted:
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	<-put
	<-muted

	row, err := store.GetDestination(dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.BackupIngestWanted {
		t.Fatal("the PUT did not apply; the test proves nothing")
	}
	if !row.Profile.Tracks[0].Muted {
		t.Error("the PUT wrote back over a mute stored during it: track 0 is unmuted")
	}
}
