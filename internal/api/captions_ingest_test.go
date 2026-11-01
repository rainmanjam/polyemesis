package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// THE CAPTION INGESTION URL BETWEEN YOUTUBE AND THE STORE (#8).
//
// YouTube documents the caption URL only on the liveStreams.insert response, so
// the caption branch of the stream pick creates a stream -- a new key -- when no
// URL is stored. These tests hold the rule that keeps that rotation behind the
// operator's own button: the five-minute pre-announce sweep and the recurring
// look-ahead must never move a key an encoder is publishing with.

// captionServer is testServerWith on a store that has a secret box, because the
// caption URL is stored sealed or not at all.
func captionServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := dbtest.OpenCheap(t, db.WithSecretBox(box))
	s := New(Options{
		DB: store, Secrets: box, Version: "test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return s, store
}

func fakeCaptionIngestURL(tag string) string {
	return "https://upload.youtube.com/closedcaption?cid=" + "fake-" + tag
}

func captionPOSTDestination(t *testing.T, s *Server, store *db.DB) *db.Destination {
	t.Helper()
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "captioner")
	d, err := store.CreateDestination(&db.Destination{
		Name: "Captioned", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live2", StreamKey: "held-key", AccountID: &acct,
		DelaySeconds: 30, CaptionMode: "youtube_post",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return d
}

// TestASweepNeverAsksForACaptionStreamWhenNoCaptionURLIsStored is the
// key-rotation hazard: CaptionPOST with no stored URL creates a new stream.
//
// MUTATION: the stored-URL condition in ingestOptions dropped (CaptionPOST set
// for every caption destination). Observed: FAIL, the sweep asked for a caption
// stream with no URL, which rotates the key.
func TestASweepNeverAsksForACaptionStreamWhenNoCaptionURLIsStored(t *testing.T) {
	s, store := captionServer(t)
	d := captionPOSTDestination(t, s, store)

	opts := s.ingestOptions(d, time.Now().Add(time.Hour))
	if opts.CaptionPOST {
		t.Fatalf("a sweep asked for a caption stream with no stored URL, which creates a stream and moves the key")
	}

	if err := store.PutCaptionIngestURL(d.ID, d.StreamKey, fakeCaptionIngestURL("held")); err != nil {
		t.Fatal(err)
	}
	opts = s.ingestOptions(d, time.Now().Add(time.Hour))
	if !opts.CaptionPOST || opts.CaptionIngestURL != fakeCaptionIngestURL("held") {
		t.Fatalf("with a URL stored for the held key the sweep must reuse it: CaptionPOST=%t URL set=%t",
			opts.CaptionPOST, opts.CaptionIngestURL != "")
	}
	if opts.CaptionDelay != 30*time.Second {
		t.Fatalf("CaptionDelay = %s, want the destination's 30s", opts.CaptionDelay)
	}
}

// TestRefreshStreamKeyIsThePathThatMayGiveACaptionDestinationItsOwnStream.
func TestRefreshStreamKeyIsThePathThatMayGiveACaptionDestinationItsOwnStream(t *testing.T) {
	s, store := captionServer(t)
	d := captionPOSTDestination(t, s, store)
	opts := s.ingestOptionsForRefresh(d)
	if !opts.CaptionPOST {
		t.Fatalf("Refresh stream key did not ask for a caption stream")
	}
	if opts.CaptionIngestURL != "" {
		t.Fatalf("no URL is stored, so none may be passed")
	}

	other, err := store.CreateDestination(&db.Destination{
		Name: "Plain", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live2", StreamKey: "k2", AccountID: d.AccountID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ingestOptionsForRefresh(other).CaptionPOST {
		t.Fatalf("a destination without POST captions asked for a caption stream")
	}
}

// TestTheCaptionURLACreateReturnsIsStoredForTheKeyItCameWith.
//
// MUTATION: keepCaptionIngest stores under dest.StreamKey (the old key) instead
// of b.Ingest.Key. Observed: FAIL, the URL read as absent for the new key.
func TestTheCaptionURLACreateReturnsIsStoredForTheKeyItCameWith(t *testing.T) {
	s, store := captionServer(t)
	d := captionPOSTDestination(t, s, store)
	b := &oauth.Broadcast{Ingest: oauth.Ingest{Key: "fresh-key"}, CaptionIngestURL: fakeCaptionIngestURL("fresh")}
	if err := s.keepCaptionIngest(d, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.CaptionIngestURL(d.ID, "fresh-key"); got != fakeCaptionIngestURL("fresh") {
		t.Fatalf("the URL was not stored for the key the create returned")
	}
	if got, _ := store.CaptionIngestURL(d.ID, "held-key"); got != "" {
		t.Fatalf("the URL was stored for the key the destination had before the create")
	}
}

// TestStartingACaptionDestinationWithNoURLIsAConflictThatNamesTheButton, and the
// destination read the console gets says it is not ready without the URL.
func TestStartingACaptionDestinationWithNoURLIsAConflictThatNamesTheButton(t *testing.T) {
	s, store := captionServer(t)
	d := captionPOSTDestination(t, s, store)
	err := store.SetDestinationEnabled(d.ID, true)
	w := httptest.NewRecorder()
	writeStoreError(w, err)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Refresh stream key") {
		t.Fatalf("the refusal does not name the button: %s", w.Body.String())
	}

	if err := store.PutCaptionIngestURL(d.ID, d.StreamKey, fakeCaptionIngestURL("view")); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetDestination(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(readSafeDestination(*row))
	if strings.Contains(string(raw), "fake-view") || strings.Contains(string(raw), "closedcaption") {
		t.Fatalf("the caption URL reached the destination view: %s", raw)
	}
	if !strings.Contains(string(raw), `"captionIngestReady":true`) {
		t.Fatalf("the view does not say the URL is ready: %s", raw)
	}
}
