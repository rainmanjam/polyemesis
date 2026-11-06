package db

import (
	"strings"
	"testing"
)

func TestADestinationKeepsItsPresetIdThroughCreateAndUpdate(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.PresetID = "instagram"
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil || got.PresetID != "instagram" {
		t.Fatalf("preset id after create = %q, %v", got.PresetID, err)
	}
	got.PresetID = "rumble"
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := d.GetDestination(created.ID)
	if again.PresetID != "rumble" {
		t.Fatalf("preset id after update = %q", again.PresetID)
	}
}

// A destination with no preset stores NULL, not an empty string, so the column
// can tell "made by hand" from "nothing recorded".
func TestADestinationWithNoPresetStoresNull(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := d.sql.QueryRow(`SELECT preset_id IS NULL FROM destinations WHERE id = ?`, created.ID).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Error("preset_id should be NULL for a destination with no preset")
	}
	if got, _ := d.GetDestination(created.ID); got.PresetID != "" {
		t.Errorf("PresetID = %q", got.PresetID)
	}
}

// SETTING an unknown preset, or one for another platform, is refused; a stored
// one the catalogue no longer has is not, or it would make the row unwritable.
func TestOnlyANewUnknownOrMismatchedPresetIsRefused(t *testing.T) {
	in := validDest()
	in.PresetID = "no-such-preset"
	if p := in.PresetProblem(); !strings.Contains(p, "unknown preset") {
		t.Fatalf("PresetProblem = %q, want an unknown preset refusal", p)
	}
	if _, err := testDB(t).CreateDestination(in); err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("create with an unknown preset = %v", err)
	}
	yt := validDest()
	yt.Platform, yt.PresetID = PlatformYouTube, "instagram"
	if p := yt.PresetProblem(); !strings.Contains(p, "saves as platform") {
		t.Fatalf("a youtube destination from the instagram preset: %q", p)
	}
	ok := validDest() // platform custom
	ok.PresetID = "instagram"
	if p := ok.PresetProblem(); p != "" {
		t.Fatalf("instagram saves as custom: %q", p)
	}
	// Validate and every write path leave a STORED unknown id alone.
	stored := validDest()
	stored.PresetID = "removed-in-a-later-release"
	if err := stored.Validate(); err != nil {
		t.Fatalf("Validate refused a stored unknown preset: %v", err)
	}
	d := testDB(t)
	created, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE destinations SET preset_id = 'removed-in-a-later-release' WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	row, _ := d.GetDestination(created.ID)
	row.Name = "renamed"
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatalf("a stored unknown preset blocked an update: %v", err)
	}
	if got := RenditionConcerns(&Rendition{Width: 3840, Height: 2160, VideoBitrate: 40000}, PlatformCustom, row.PresetID); len(got) != 0 {
		t.Errorf("an unknown stored preset gave guidance: %+v", got)
	}
}

// A preset only names guidance for the platform it saves as.
func TestAPresetForAnotherPlatformGivesNoGuidance(t *testing.T) {
	r := &Rendition{Width: 3840, Height: 2160, FPS: 60, VideoBitrate: 40000}
	got := RenditionConcerns(r, PlatformYouTube, "instagram")
	for _, c := range got {
		if strings.Contains(c.Detail, "Instagram") {
			t.Errorf("youtube with preset=instagram got Instagram's guidance: %+v", c)
		}
	}
	if len(got) == 0 {
		t.Error("youtube should still fall back to its own guidance")
	}
}

func TestPresetForIngestIsUnambiguousOrNothing(t *testing.T) {
	cases := []struct {
		plat Platform
		url  string
		want string
	}{
		{PlatformYouTube, "rtmp://a.rtmp.youtube.com/live2", "youtube"},
		{PlatformYouTube, "rtmps://a.rtmps.youtube.com:443/live2", "youtube-rtmps"},
		// Twitch's two presets share a host; the scheme tells them apart.
		{PlatformTwitch, "rtmp://live.twitch.tv/app", "twitch"},
		{PlatformTwitch, "rtmps://live.twitch.tv/app", "twitch-rtmps"},
		{PlatformCustom, "rtmps://live.cloudflare.com:443/live/", "cloudflare-stream"},
		{PlatformCustom, "rtmps://global-live.mux.com:443/app", "mux"},
		// A platform that does not match what the preset saves as is not guessed at.
		{PlatformCustom, "rtmp://a.rtmp.youtube.com/live2", ""},
		// A regional Twitch ingest is not any preset's fixed host.
		{PlatformTwitch, "rtmp://live-sfo.twitch.tv/app", ""},
		// A template host stands for anyone's server.
		{PlatformCustom, "rtmp://my-owncast.example:1935/live", ""},
		{PlatformCustom, "not a url", ""},
	}
	for _, c := range cases {
		got, ok := presetForIngest(c.plat, c.url)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("presetForIngest(%s, %q) = %q, %v; want %q", c.plat, c.url, got, ok, c.want)
		}
	}
}

// An existing database gains the column and the backfill together, once. A row
// that matches one preset is labelled, one that matches none stays NULL, and a
// second run changes nothing -- including a row the operator has since cleared.
func TestTheMigrationBackfillsOnceFromTheIngestUrl(t *testing.T) {
	d := testDB(t)
	yt := validDest()
	yt.Name, yt.Platform, yt.URL = "yt", PlatformYouTube, "rtmps://a.rtmps.youtube.com:443/live2"
	hand := validDest()
	hand.Name, hand.URL = "by hand", "rtmp://ingest.example/app"
	a, err := d.CreateDestination(yt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateDestination(hand)
	if err != nil {
		t.Fatal(err)
	}
	// Back to what a database from before this release looks like.
	if _, err := d.sql.Exec(`ALTER TABLE destinations DROP COLUMN preset_id`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if err := d.MigrateDestinationPresetID(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got, _ := d.GetDestination(a.ID); got.PresetID != "youtube-rtmps" {
		t.Errorf("youtube destination backfilled to %q, want youtube-rtmps", got.PresetID)
	}
	if got, _ := d.GetDestination(b.ID); got.PresetID != "" {
		t.Errorf("a hand-typed destination was labelled %q", got.PresetID)
	}
	// Once: a later run leaves a cleared row cleared.
	if _, err := d.sql.Exec(`UPDATE destinations SET preset_id = NULL WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateDestinationPresetID(); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetDestination(a.ID); got.PresetID != "" {
		t.Errorf("the backfill ran again and set %q", got.PresetID)
	}
}

// A failure in the backfill must not leave the column behind as "done". The
// whole step is one transaction, so the ALTER rolls back with it and the next
// boot runs the backfill again from the start.
func TestAFailedBackfillRollsTheColumnBackAndTheNextBootRetries(t *testing.T) {
	d := testDB(t)
	yt := validDest()
	yt.Name, yt.Platform, yt.URL = "yt", PlatformYouTube, "rtmps://a.rtmps.youtube.com:443/live2"
	a, err := d.CreateDestination(yt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations DROP COLUMN preset_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`CREATE TRIGGER boom BEFORE UPDATE ON destinations BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateDestinationPresetID(); err == nil {
		t.Fatal("the backfill was made to fail and the migration reported success")
	}
	if has, _ := columnExists(d.sql, "destinations", "preset_id"); has {
		t.Fatal("the ALTER survived a failed backfill, so the next boot would skip it for good")
	}
	if _, err := d.sql.Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateDestinationPresetID(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got, _ := d.GetDestination(a.ID); got.PresetID != "youtube-rtmps" {
		t.Errorf("after the retry the destination has preset %q, want youtube-rtmps", got.PresetID)
	}
}

// The release fixtures carry no destination rows, so the backfill is run here
// over a realistic spread of them: matched, ambiguous-by-scheme, a hand-typed
// host, a platform that does not fit the preset, a non-RTMP kind and an
// untouched row with no URL.
func TestTheBackfillOverARealisticSetOfDestinations(t *testing.T) {
	d := testDB(t)
	cases := []struct {
		name     string
		kind     DestKind
		platform Platform
		url      string
		want     string
	}{
		{"yt rtmp", DestRTMP, PlatformYouTube, "rtmp://a.rtmp.youtube.com/live2", "youtube"},
		{"yt rtmps", DestRTMP, PlatformYouTube, "rtmps://a.rtmps.youtube.com:443/live2", "youtube-rtmps"},
		{"twitch rtmp", DestRTMP, PlatformTwitch, "rtmp://live.twitch.tv/app", "twitch"},
		{"twitch rtmps", DestRTMP, PlatformTwitch, "rtmps://live.twitch.tv/app", "twitch-rtmps"},
		{"twitch regional", DestRTMP, PlatformTwitch, "rtmp://live-sfo.twitch.tv/app", ""},
		{"facebook", DestRTMP, PlatformFacebook, "rtmps://live-api-s.facebook.com:443/rtmp/", "facebook"},
		{"cloudflare", DestRTMP, PlatformCustom, "rtmps://live.cloudflare.com:443/live/", "cloudflare-stream"},
		{"mux", DestRTMP, PlatformCustom, "rtmps://global-live.mux.com:443/app", "mux"},
		{"youtube host on a custom row", DestRTMP, PlatformCustom, "rtmp://a.rtmp.youtube.com/live2", ""},
		{"own server", DestRTMP, PlatformCustom, "rtmp://ingest.example/app", ""},
		{"srt", DestSRT, PlatformCustom, "srt://ingest.example:9000", ""},
	}
	ids := make([]int64, len(cases))
	for i, c := range cases {
		in := validDest()
		in.Name, in.Kind, in.Platform, in.URL = c.name, c.kind, c.platform, c.url
		if c.kind != DestRTMP {
			in.StreamKey = ""
		}
		created, err := d.CreateDestination(in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ids[i] = created.ID
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations DROP COLUMN preset_id`); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateDestinationPresetID(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i, c := range cases {
		got, err := d.GetDestination(ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if got.PresetID != c.want {
			t.Errorf("%s: preset %q, want %q", c.name, got.PresetID, c.want)
		}
	}
}
