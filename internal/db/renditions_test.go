package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

func validRendition() *Rendition {
	return &Rendition{
		Name:         "1080p60",
		Width:        1920,
		Height:       1080,
		FPS:          60,
		VideoBitrate: 6000,
		Encoder:      EncoderX264,
		Preset:       "veryfast",
		GOPSeconds:   2,
		Note:         "for platforms that will not take the 4K source",
	}
}

func mustCreateRendition(t *testing.T, d *DB, r *Rendition) *Rendition {
	t.Helper()
	got, err := d.CreateRendition(r)
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	return got
}

func mustCreateDest(t *testing.T, d *DB, name string, enabled bool, rendition *int64) *Destination {
	t.Helper()
	dst := validDest()
	dst.Name = name
	dst.Enabled = enabled
	dst.RenditionID = rendition
	got, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination(%s): %v", name, err)
	}
	return got
}

// -------------------------------------------------------------- validation

func TestRenditionValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Rendition)
		wantAll []string
	}{
		{
			name:    "an unnamed rendition",
			mutate:  func(r *Rendition) { r.Name = "  " },
			wantAll: []string{"name is required"},
		},
		{
			// H.264/HEVC encode 4:2:0, so an odd axis has no chroma plane and
			// FFmpeg refuses to start.
			name:    "an odd width, which H.264 cannot represent",
			mutate:  func(r *Rendition) { r.Width = 1921 },
			wantAll: []string{"width 1921 must be an even number of pixels"},
		},
		{
			name:    "an odd height, which H.264 cannot represent",
			mutate:  func(r *Rendition) { r.Height = 1081 },
			wantAll: []string{"height 1081 must be an even number of pixels"},
		},
		{
			name:    "a width below the sane floor",
			mutate:  func(r *Rendition) { r.Width = 8 },
			wantAll: []string{"width 8 out of range"},
		},
		{
			name:    "a width beyond the sane ceiling",
			mutate:  func(r *Rendition) { r.Width = 20000 },
			wantAll: []string{"width 20000 out of range"},
		},
		{
			name:    "a negative frame rate",
			mutate:  func(r *Rendition) { r.FPS = -1 },
			wantAll: []string{"fps -1 out of range"},
		},
		{
			// Refused here rather than at start time for the same reason an
			// unknown aspect mode is: deinterlaceFilter degrades an
			// unrecognised mode to OFF, so the stored setting would say
			// "deinterlacing" while the picture stayed combed, and nothing
			// anywhere would say which one was running.
			name:    "an unknown deinterlace mode, which the filter builder would silently ignore",
			mutate:  func(r *Rendition) { r.Deinterlace = "yadif" },
			wantAll: []string{`unknown deinterlace mode "yadif"`},
		},
		{
			name:    "an absurd frame rate",
			mutate:  func(r *Rendition) { r.FPS = 1000 },
			wantAll: []string{"fps 1000 out of range"},
		},
		{
			// The classic unit mix-up: 6 Mbps typed as 6.
			name:    "a bitrate typed in Mbps instead of kbps",
			mutate:  func(r *Rendition) { r.VideoBitrate = 6 },
			wantAll: []string{"video bitrate 6 kbps out of range"},
		},
		{
			name:    "a bitrate beyond the sane ceiling",
			mutate:  func(r *Rendition) { r.VideoBitrate = 500000 },
			wantAll: []string{"video bitrate 500000 kbps out of range"},
		},
		{
			name:    "an encoder that is not an FFmpeg encoder",
			mutate:  func(r *Rendition) { r.Encoder = "libx264 " },
			wantAll: []string{`unknown encoder "libx264 "`},
		},
		{
			name:    "an audio encoder, which a rendition must never name",
			mutate:  func(r *Rendition) { r.Encoder = "aac" },
			wantAll: []string{`unknown encoder "aac"`},
		},
		{
			// Preset becomes a bare argv entry, so anything needing quoting is
			// refused rather than escaped.
			name:    "a preset carrying shell punctuation",
			mutate:  func(r *Rendition) { r.Preset = "veryfast; rm -rf /" },
			wantAll: []string{"preset"},
		},
		{
			name:    "an empty preset",
			mutate:  func(r *Rendition) { r.Preset = "" },
			wantAll: []string{`preset ""`},
		},
		{
			name:    "a sub-second GOP",
			mutate:  func(r *Rendition) { r.GOPSeconds = 0.5 },
			wantAll: []string{"gop 0.5s out of range"},
		},
		{
			name:    "a GOP longer than any live platform accepts",
			mutate:  func(r *Rendition) { r.GOPSeconds = 30 },
			wantAll: []string{"gop 30s out of range"},
		},
		{
			// Every problem at once, so the UI marks up the whole form rather
			// than surfacing one error per round trip.
			name: "every problem at once",
			mutate: func(r *Rendition) {
				r.Name = ""
				r.Width = 1921
				r.FPS = -5
				r.VideoBitrate = 0
				r.Encoder = "nope"
				r.GOPSeconds = 99
			},
			wantAll: []string{
				"name is required",
				"width 1921 must be an even",
				"fps -5 out of range",
				"video bitrate 0 kbps out of range",
				`unknown encoder "nope"`,
				"gop 99s out of range",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRendition()
			tt.mutate(r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			for _, want := range tt.wantAll {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestRenditionValidateAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Rendition)
	}{
		{
			// 0 on every geometry field is the "keep the source's" sentinel:
			// this rendition only changes the bitrate.
			name: "zero width, height and fps meaning keep the source's",
			mutate: func(r *Rendition) {
				r.Width, r.Height, r.FPS = 0, 0, 0
			},
		},
		{
			// Only one axis pinned: the encoder preserves aspect ratio.
			name:   "height alone, letting the width follow the aspect ratio",
			mutate: func(r *Rendition) { r.Width = 0 },
		},
		{
			name:   "a hardware encoder with its own preset vocabulary",
			mutate: func(r *Rendition) { r.Encoder, r.Preset = EncoderNVENCH264, "p4" },
		},
		{
			name:   "an Apple hardware encoder",
			mutate: func(r *Rendition) { r.Encoder, r.Preset = EncoderVideoToolboxH264, "quality" },
		},
		{
			name:   "a one-second GOP",
			mutate: func(r *Rendition) { r.GOPSeconds = 1 },
		},
		{
			name:   "a ten-second GOP",
			mutate: func(r *Rendition) { r.GOPSeconds = 10 },
		},
		{
			name:   "deinterlacing only the frames the source flagged",
			mutate: func(r *Rendition) { r.Deinterlace = string(ffmpeg.DeinterlaceAuto) },
		},
		{
			name:   "deinterlacing unconditionally, for kit that flags nothing",
			mutate: func(r *Rendition) { r.Deinterlace = string(ffmpeg.DeinterlaceAll) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRendition()
			tt.mutate(r)
			if err := r.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestEncoderCodecDistinguishesH264FromHEVC(t *testing.T) {
	tests := []struct {
		encoder VideoEncoder
		want    string
	}{
		{EncoderX264, "h264"},
		{EncoderNVENCH264, "h264"},
		{EncoderQSVH264, "h264"},
		{EncoderVideoToolboxH264, "h264"},
		{EncoderAMFH264, "h264"},
		{EncoderX265, "hevc"},
		{EncoderNVENCHEVC, "hevc"},
		{EncoderQSVHEVC, "hevc"},
		{EncoderVideoToolboxHEVC, "hevc"},
		{EncoderVAAPIHEVC, "hevc"},
	}

	for _, tt := range tests {
		t.Run(string(tt.encoder), func(t *testing.T) {
			if got := tt.encoder.Codec(); got != tt.want {
				t.Errorf("Codec() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ----------------------------------------------------------------- presets

func TestEveryPresetIsAValidEditableStartingPoint(t *testing.T) {
	presets := RenditionPresets()

	// The tiers the feature is required to ship with.
	wantKeys := []string{"passthrough", "1080p60", "1080p30", "720p60"}
	got := map[string]RenditionPreset{}
	for _, p := range presets {
		if _, dup := got[p.Key]; dup {
			t.Errorf("preset key %q appears twice", p.Key)
		}
		got[p.Key] = p
	}
	for _, key := range wantKeys {
		if _, ok := got[key]; !ok {
			t.Errorf("preset %q is missing", key)
		}
	}

	for _, p := range presets {
		t.Run(p.Key, func(t *testing.T) {
			if p.Label == "" {
				t.Error("preset has no label")
			}
			if p.Passthrough {
				// Passthrough is the absence of a rendition, not a row.
				if p.Rendition != nil {
					t.Error("the passthrough preset carries a rendition; it must be the NULL rendition")
				}
				return
			}
			if p.Rendition == nil {
				t.Fatal("non-passthrough preset carries no rendition template")
			}
			if err := p.Rendition.Validate(); err != nil {
				t.Errorf("preset does not validate: %v", err)
			}
			if p.Rendition.ID != 0 {
				t.Errorf("preset template has ID %d, want an unsaved zero", p.Rendition.ID)
			}
			// Presets are starting points, never authoritative ceilings, and
			// the note is where the user is told so.
			if !strings.Contains(p.Rendition.Note, PresetDisclaimer) {
				t.Errorf("note %q does not carry the disclaimer %q", p.Rendition.Note, PresetDisclaimer)
			}
		})
	}
}

// ------------------------------------------------------------------- store

func TestRenditionRoundTripsEveryFieldThroughTheStore(t *testing.T) {
	d := testDB(t)

	want := validRendition()
	want.Encoder = EncoderNVENCH264
	want.Preset = "p6"
	want.GOPSeconds = 1.5

	created := mustCreateRendition(t, d, want)
	got, err := d.GetRendition(created.ID)
	if err != nil {
		t.Fatalf("GetRendition: %v", err)
	}

	if got.Name != want.Name || got.Width != want.Width || got.Height != want.Height ||
		got.FPS != want.FPS || got.VideoBitrate != want.VideoBitrate ||
		got.Encoder != want.Encoder || got.Preset != want.Preset ||
		got.GOPSeconds != want.GOPSeconds || got.Note != want.Note {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}
}

func TestCreateRenditionFillsInTheFieldsAnAPIPayloadMayOmit(t *testing.T) {
	d := testDB(t)

	// The shortest useful create request: a name, a size and a bitrate.
	got := mustCreateRendition(t, d, &Rendition{
		Name: "720p", Height: 720, VideoBitrate: 3000,
	})

	if got.Encoder != EncoderX264 {
		t.Errorf("Encoder = %q, want the software default %q", got.Encoder, EncoderX264)
	}
	if got.Preset != "veryfast" {
		t.Errorf("Preset = %q, want %q", got.Preset, "veryfast")
	}
	if got.GOPSeconds != 2 {
		t.Errorf("GOPSeconds = %v, want 2", got.GOPSeconds)
	}
	if got.Width != 0 {
		t.Errorf("Width = %d, want 0 so the aspect ratio follows the source", got.Width)
	}
}

func TestCreateRenditionRejectsAnInvalidRenditionBeforeItReachesTheTable(t *testing.T) {
	d := testDB(t)

	bad := validRendition()
	bad.Width = 1921
	if _, err := d.CreateRendition(bad); err == nil {
		t.Fatal("CreateRendition accepted an odd width")
	}

	list, err := d.ListRenditions()
	if err != nil {
		t.Fatalf("ListRenditions: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListRenditions = %d rows, want 0", len(list))
	}
}

func TestUpdateRenditionRewritesTheTierAndReportsMissingRows(t *testing.T) {
	d := testDB(t)
	r := mustCreateRendition(t, d, validRendition())

	r.Name = "1080p30"
	r.FPS = 30
	r.VideoBitrate = 4500
	got, err := d.UpdateRendition(r)
	if err != nil {
		t.Fatalf("UpdateRendition: %v", err)
	}
	if got.FPS != 30 || got.VideoBitrate != 4500 || got.Name != "1080p30" {
		t.Errorf("update = %+v, want the 1080p30 tier", got)
	}

	missing := validRendition()
	missing.ID = 9999
	if _, err := d.UpdateRendition(missing); err != ErrNotFound {
		t.Errorf("UpdateRendition(unknown id) = %v, want ErrNotFound", err)
	}
}

func TestGetAndDeleteRenditionReportMissingRows(t *testing.T) {
	d := testDB(t)

	if _, err := d.GetRendition(1); err != ErrNotFound {
		t.Errorf("GetRendition(1) = %v, want ErrNotFound", err)
	}
	if err := d.DeleteRendition(1); err != ErrNotFound {
		t.Errorf("DeleteRendition(1) = %v, want ErrNotFound", err)
	}
}

// ------------------------------------------------- destinations x renditions

func TestDestinationWithNoRenditionRoundTripsAsPassthrough(t *testing.T) {
	d := testDB(t)

	// This is exactly what every destination created before the renditions
	// feature existed looks like, and it must keep working untouched.
	created := mustCreateDest(t, d, "Existing", true, nil)
	if created.RenditionID != nil {
		t.Fatalf("RenditionID = %v, want nil (passthrough)", *created.RenditionID)
	}

	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.RenditionID != nil {
		t.Errorf("RenditionID after reload = %v, want nil", *got.RenditionID)
	}

	// An update that says nothing about renditions must not invent one.
	got.Name = "Existing, renamed"
	updated, err := d.UpdateDestination(got)
	if err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if updated.RenditionID != nil {
		t.Errorf("RenditionID after update = %v, want nil", *updated.RenditionID)
	}

	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	if len(list) != 1 || list[0].RenditionID != nil {
		t.Errorf("ListDestinations = %+v, want one passthrough destination", list)
	}
}

func TestDestinationSelectsARenditionAndKeepsItAcrossReloads(t *testing.T) {
	d := testDB(t)
	r := mustCreateRendition(t, d, validRendition())

	dst := mustCreateDest(t, d, "Twitch", true, &r.ID)
	got, err := d.GetDestination(dst.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.RenditionID == nil || *got.RenditionID != r.ID {
		t.Fatalf("RenditionID = %v, want %d", got.RenditionID, r.ID)
	}

	// Moving a destination back to passthrough is just clearing the field.
	got.RenditionID = nil
	updated, err := d.UpdateDestination(got)
	if err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if updated.RenditionID != nil {
		t.Errorf("RenditionID = %v, want nil after moving back to passthrough", *updated.RenditionID)
	}
}

func TestDestinationRejectsARenditionThatDoesNotExist(t *testing.T) {
	d := testDB(t)
	ghost := int64(4242)

	if _, err := d.CreateDestination(func() *Destination {
		dst := validDest()
		dst.RenditionID = &ghost
		return dst
	}()); err == nil || !strings.Contains(err.Error(), "rendition 4242 does not exist") {
		t.Errorf("CreateDestination = %v, want a named missing-rendition error", err)
	}

	existing := mustCreateDest(t, d, "Main", false, nil)
	existing.RenditionID = &ghost
	if _, err := d.UpdateDestination(existing); err == nil ||
		!strings.Contains(err.Error(), "rendition 4242 does not exist") {
		t.Errorf("UpdateDestination = %v, want a named missing-rendition error", err)
	}
}

func TestDeletingARenditionFallsItsDestinationsBackToPassthrough(t *testing.T) {
	d := testDB(t)
	r := mustCreateRendition(t, d, validRendition())

	twitch := mustCreateDest(t, d, "Twitch", true, &r.ID)
	kick := mustCreateDest(t, d, "Kick", true, &r.ID)
	youtube := mustCreateDest(t, d, "YouTube", true, nil)

	if err := d.DeleteRendition(r.ID); err != nil {
		t.Fatalf("DeleteRendition: %v", err)
	}

	// Losing an encode tier must never lose the endpoints the user configured.
	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListDestinations = %d rows, want all 3 destinations to survive", len(list))
	}
	for _, id := range []int64{twitch.ID, kick.ID, youtube.ID} {
		got, err := d.GetDestination(id)
		if err != nil {
			t.Fatalf("GetDestination(%d): %v", id, err)
		}
		if got.RenditionID != nil {
			t.Errorf("destination %q still points at rendition %d, want passthrough",
				got.Name, *got.RenditionID)
		}
		if !got.Enabled {
			t.Errorf("destination %q was disabled by the delete", got.Name)
		}
	}
}

func TestCountEnabledDestinationsByRenditionDrivesRefCounting(t *testing.T) {
	d := testDB(t)
	hot := mustCreateRendition(t, d, validRendition())

	cold := validRendition()
	cold.Name = "720p60"
	cold.Width, cold.Height, cold.FPS, cold.VideoBitrate = 1280, 720, 60, 4500
	coldR := mustCreateRendition(t, d, cold)

	mustCreateDest(t, d, "Twitch", true, &hot.ID)
	mustCreateDest(t, d, "Kick", true, &hot.ID)
	mustCreateDest(t, d, "X", false, &hot.ID)       // disabled: not a reference
	mustCreateDest(t, d, "Spare", false, &coldR.ID) // nobody enabled: must not burn CPU
	mustCreateDest(t, d, "YouTube", true, nil)      // passthrough: no process to count

	counts, err := d.CountEnabledDestinationsByRendition()
	if err != nil {
		t.Fatalf("CountEnabledDestinationsByRendition: %v", err)
	}
	if counts[hot.ID] != 2 {
		t.Errorf("counts[%d] = %d, want 2 enabled destinations", hot.ID, counts[hot.ID])
	}
	if _, present := counts[coldR.ID]; present {
		t.Errorf("rendition %d with no enabled destinations appears in the map", coldR.ID)
	}
	if len(counts) != 1 {
		t.Errorf("counts = %v, want only the referenced rendition", counts)
	}

	// The last enabled destination releasing the rendition drops it to zero.
	for _, name := range []string{"Twitch", "Kick"} {
		list, err := d.ListDestinations()
		if err != nil {
			t.Fatalf("ListDestinations: %v", err)
		}
		for _, dst := range list {
			if dst.Name == name {
				if err := d.SetDestinationEnabled(dst.ID, false); err != nil {
					t.Fatalf("SetDestinationEnabled: %v", err)
				}
			}
		}
	}
	counts, err = d.CountEnabledDestinationsByRendition()
	if err != nil {
		t.Fatalf("CountEnabledDestinationsByRendition: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts = %v, want an empty map once every destination is disabled", counts)
	}
}

// --------------------------------------------------------------- migration

// TestMigrateRenditionsUpgradesAPreRenditionsDatabase builds a database with
// the destinations table exactly as it was before this feature, because
// schema.sql's CREATE TABLE IF NOT EXISTS is a no-op against it and cannot add
// the column. An existing install must come back up with its destinations
// intact and on passthrough.
func TestMigrateRenditionsUpgradesAPreRenditionsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE destinations (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		name          TEXT    NOT NULL,
		kind          TEXT    NOT NULL,
		platform      TEXT    NOT NULL DEFAULT '',
		account_id    INTEGER,
		url           TEXT    NOT NULL DEFAULT '',
		stream_key    TEXT    NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 0,
		audio_bitrate INTEGER NOT NULL DEFAULT 160,
		profile       TEXT    NOT NULL,
		position      INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);
	INSERT INTO destinations (name, kind, url, stream_key, enabled, audio_bitrate, profile, position, created_at, updated_at)
	VALUES ('Legacy', 'rtmp', 'rtmp://ingest.example/live', 'abc-123', 1, 160,
		'{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],"normalize":"auto","sampleRate":48000}',
		0, 1000, 1000);`); err != nil {
		t.Fatalf("build pre-renditions database: %v", err)
	}
	// Applying schema.sql is not enough on its own: CREATE TABLE IF NOT EXISTS
	// leaves the old destinations table alone, so the column is still missing
	// and every destination query fails. Proving that here, against the same
	// handle and before Open is ever called, is what makes the assertion below
	// load-bearing rather than decorative — if schema.sql ever grew the column
	// on its own, this would fail and say so.
	if _, err := old.Exec(schemaSQL); err != nil {
		t.Fatalf("apply schema to pre-renditions database: %v", err)
	}
	if _, err := (&DB{sql: old}).ListDestinations(); err == nil {
		t.Fatal("ListDestinations succeeded with schema.sql alone; the migration is not load-bearing and this test proves nothing")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	// Open, and only Open, is what an existing install actually runs. It must
	// migrate, because nothing else will.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-renditions database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Idempotent, because Open runs it again on every subsequent start.
	if err := d.MigrateRenditions(); err != nil {
		t.Fatalf("MigrateRenditions (second call): %v", err)
	}

	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations after migration: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListDestinations = %d rows, want the pre-existing destination", len(list))
	}
	legacy := list[0]
	if legacy.Name != "Legacy" || !legacy.Enabled || legacy.Target() != "rtmp://ingest.example/live/abc-123" {
		t.Errorf("pre-existing destination came back as %+v", legacy)
	}
	if legacy.RenditionID != nil {
		t.Errorf("RenditionID = %d, want nil so the destination stays passthrough", *legacy.RenditionID)
	}

	// The migrated column carries the same foreign key as a fresh install, so
	// deleting a rendition still drops its destinations back to passthrough.
	r := mustCreateRendition(t, d, validRendition())
	legacy.RenditionID = &r.ID
	if _, err := d.UpdateDestination(legacy); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if err := d.DeleteRendition(r.ID); err != nil {
		t.Fatalf("DeleteRendition: %v", err)
	}
	got, err := d.GetDestination(legacy.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.RenditionID != nil {
		t.Errorf("RenditionID = %d after deleting the rendition, want nil", *got.RenditionID)
	}
}
