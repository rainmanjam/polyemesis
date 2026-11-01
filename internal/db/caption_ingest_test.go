package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The caption ingestion URL fixtures are built at run time and are obviously
// fake, so nothing here looks like a real signed URL to a secret scanner.
func fakeCaptionURL(tag string) string {
	return "https://upload.youtube.com/closedcaption?cid=" + "fake-cid-" + tag
}

// captionPOSTDest is a YouTube destination that sends captions by HTTP POST, the
// one mode that needs a stored URL. Delay 30 is one YouTube accepts for it.
func captionPOSTDest(t *testing.T, d *DB) *Destination {
	t.Helper()
	dst := captionDest(PlatformYouTube, 30)
	dst.CaptionMode = "youtube_post"
	dst.StreamKey = "yt-key-one"
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return created
}

func boxedCaptionDB(t *testing.T) *DB {
	t.Helper()
	return keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"), WithSecretBox(testBox(t)))
}

// TestACaptionIngestURLIsStoredSealedAndOnlyForTheKeyItWasIssuedWith reads the
// raw column, so an implementation that stored the URL in the clear and handed it
// back would fail here, and then proves a URL issued for one stream key reads as
// absent once the key has changed.
//
// MUTATION: the hash comparison in CaptionIngestURL removed. Observed: FAIL, the
// old URL came back for the new key.
func TestACaptionIngestURLIsStoredSealedAndOnlyForTheKeyItWasIssuedWith(t *testing.T) {
	d := boxedCaptionDB(t)
	dst := captionPOSTDest(t, d)
	url := fakeCaptionURL("sealed")
	if err := d.PutCaptionIngestURL(dst.ID, dst.StreamKey, url); err != nil {
		t.Fatalf("put: %v", err)
	}
	var raw []byte
	var hash string
	if err := d.sql.QueryRow(`SELECT url_secret_enc, stream_key_hash FROM caption_ingest WHERE destination_id=?`,
		dst.ID).Scan(&raw, &hash); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if strings.Contains(string(raw), "fake-cid-sealed") || strings.Contains(string(raw), "upload.youtube.com") {
		t.Fatalf("the caption URL is stored in the clear")
	}
	if strings.Contains(hash, dst.StreamKey) {
		t.Fatalf("the stream key is stored in the clear beside the URL")
	}
	got, err := d.CaptionIngestURL(dst.ID, dst.StreamKey)
	if err != nil || got != url {
		t.Fatalf("read back = %q, %v; want the stored URL", got, err)
	}
	if got, _ := d.CaptionIngestURL(dst.ID, "yt-key-typed-over"); got != "" {
		t.Fatalf("a URL issued for another stream key was returned for the new key")
	}
	row, err := d.GetDestination(dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.CaptionIngestReady {
		t.Fatalf("CaptionIngestReady is false with a URL stored for the current key")
	}
}

// TestACaptionIngestURLIsRefusedWithoutASecretBox: no plaintext fallback.
func TestACaptionIngestURLIsRefusedWithoutASecretBox(t *testing.T) {
	d := testDB(t)
	dst := captionPOSTDest(t, d)
	if err := d.PutCaptionIngestURL(dst.ID, dst.StreamKey, fakeCaptionURL("nobox")); err == nil {
		t.Fatalf("a caption URL was stored with no secret box")
	}
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM caption_ingest`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d caption rows written without a box", n)
	}
}

// TestADestinationThatPostsCaptionsCannotStartWithoutItsCaptionURL is the go-live
// refusal: every start path arrives at SetDestinationEnabled, and the turn-on
// edge of UpdateDestination is the other door.
//
// MUTATION: the captionIngestRefusal call removed from SetDestinationEnabled.
// Observed: FAIL, the destination started with no URL.
func TestADestinationThatPostsCaptionsCannotStartWithoutItsCaptionURL(t *testing.T) {
	d := boxedCaptionDB(t)
	dst := captionPOSTDest(t, d)
	on := captionDest(PlatformYouTube, 30)
	on.CaptionMode, on.Enabled = "youtube_post", true
	if _, err := d.CreateDestination(on); !errors.Is(err, ErrCaptionIngestMissing) {
		t.Fatalf("creating it already on = %v, want ErrCaptionIngestMissing", err)
	}
	err := d.SetDestinationEnabled(dst.ID, true)
	if !errors.Is(err, ErrCaptionIngestMissing) {
		t.Fatalf("start with no caption URL = %v, want ErrCaptionIngestMissing", err)
	}
	if !strings.Contains(err.Error(), "Refresh stream key") {
		t.Fatalf("the refusal does not name the fix: %v", err)
	}
	row, _ := d.GetDestination(dst.ID)
	if row.Enabled {
		t.Fatalf("the refused destination is enabled")
	}
	row.Enabled = true
	if _, err := d.UpdateDestination(row); !errors.Is(err, ErrCaptionIngestMissing) {
		t.Fatalf("turning it on through a save = %v, want ErrCaptionIngestMissing", err)
	}

	if err := d.PutCaptionIngestURL(dst.ID, dst.StreamKey, fakeCaptionURL("start")); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDestinationEnabled(dst.ID, true); err != nil {
		t.Fatalf("start with the URL stored = %v", err)
	}
	// Already on: a save that keeps it on is not a turn-on edge, so a URL lost
	// mid-show never stops the stream.
	if err := d.DeleteCaptionIngestURL(dst.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = d.GetDestination(dst.ID)
	row.Name = "renamed while live"
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatalf("a save of a running destination was refused: %v", err)
	}
	if err := d.SetDestinationEnabled(dst.ID, true); err != nil {
		t.Fatalf("a repeated start of a running destination was refused: %v", err)
	}
}

// TestOtherCaptionModesStartWithoutAURL: only youtube_post needs one.
func TestOtherCaptionModesStartWithoutAURL(t *testing.T) {
	d := boxedCaptionDB(t)
	dst := captionDest(PlatformYouTube, 0)
	dst.CaptionMode = "sei"
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetDestinationEnabled(created.ID, true); err != nil {
		t.Fatalf("an embedded-caption destination was refused: %v", err)
	}
}

// TestTurningPOSTCaptionsOffForgetsTheURLAndDeletingTheDestinationDoesToo.
//
// MUTATION: the DeleteCaptionIngestURL call in UpdateDestination removed.
// Observed: FAIL, the row survived the mode change.
func TestTurningPOSTCaptionsOffForgetsTheURLAndDeletingTheDestinationDoesToo(t *testing.T) {
	d := boxedCaptionDB(t)
	dst := captionPOSTDest(t, d)
	if err := d.PutCaptionIngestURL(dst.ID, dst.StreamKey, fakeCaptionURL("off")); err != nil {
		t.Fatal(err)
	}
	row, _ := d.GetDestination(dst.ID)
	row.CaptionMode = "off"
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM caption_ingest WHERE destination_id=?`, dst.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("the caption URL outlived the caption mode")
	}

	other := captionPOSTDest(t, d)
	if err := d.PutCaptionIngestURL(other.ID, other.StreamKey, fakeCaptionURL("del")); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteDestination(other.ID); err != nil {
		t.Fatal(err)
	}
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM caption_ingest WHERE destination_id=?`, other.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("the caption URL outlived its destination")
	}
}
