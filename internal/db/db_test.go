package db

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// templateOnce guards building the one migrated database every testDB copies.
//
// Open is not cheap: it executes the whole of schema.sql and then six
// migrations, and that DDL -- not the queries the tests actually run -- is
// where this package spent its time. 181 call sites paid for it once each. So
// we pay it once for the package instead, snapshot the resulting file, and
// hand every test a byte copy.
//
// The copy is still opened through Open rather than handed to the test as a
// live handle, which is what keeps this a pure speed change: the options a
// caller passes (WithPasswordCost) still apply, and schema.sql plus the six
// migrations still run -- they are simply no-ops now, because every CREATE is
// IF NOT EXISTS and every migration first checks for the column it adds. A
// test cannot tell the difference except by the clock.
//
// An in-memory database was the obvious alternative and it was measured and
// rejected: 15.4ms/op against 3.27ms/op for the file copy. The cost being
// removed here is the DDL, which :memory: still has to execute in full on
// every open, while a copied template has already done it.
var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// testTemplate returns the bytes of a freshly migrated database file.
func testTemplate() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "polyemesis-db-template")
		if err != nil {
			templateErr = fmt.Errorf("template tempdir: %w", err)
			return
		}
		// Removed as soon as the bytes are in hand; nothing reopens this path.
		defer os.RemoveAll(dir)

		path := filepath.Join(dir, "polyemesis.db")
		d, err := Open(path)
		if err != nil {
			templateErr = fmt.Errorf("template open: %w", err)
			return
		}
		// The template creates its own source. Open no longer seeds one on a
		// fresh database -- MigrateSources only builds "Main" for an install
		// upgrading from single-ingest -- and almost every test here needs a
		// source to exist, because CreateDestination and CreateRendition resolve
		// a default one and refuse without it.
		//
		// The source belongs in the fixture, NOT in a looser migration rule.
		// Widening the discriminator to keep this suite green would put the seed
		// back on every fresh install, which is the whole thing the rule exists
		// to stop. A test that wants the zero-source state opens its own
		// database instead of taking this template.
		if err := d.CreateSource(&Source{
			Name: DefaultSourceName, Enabled: true, Ingest: DefaultSettings().Ingest, Position: 1,
		}); err != nil {
			templateErr = fmt.Errorf("template source: %w", err)
			return
		}
		// Closed before reading, and that ordering is load-bearing rather than
		// tidiness: Open runs in WAL mode, so until the last connection closes
		// and checkpoints, the committed schema lives in polyemesis.db-wal and
		// the file we are about to read is empty.
		if err := d.Close(); err != nil {
			templateErr = fmt.Errorf("template close: %w", err)
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	return templateBytes, templateErr
}

func testDB(t *testing.T) *DB {
	t.Helper()
	tmpl, err := testTemplate()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	if err := os.WriteFile(path, tmpl, 0o600); err != nil {
		t.Fatalf("write template copy: %v", err)
	}
	d, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return box
}

func validDest() *Destination {
	return &Destination{
		Name:         "Main",
		Kind:         DestRTMP,
		URL:          "rtmp://ingest.example/live",
		StreamKey:    "abc-123",
		AudioBitrate: 160,
		Profile:      routing.DefaultProfile(),
	}
}

// ------------------------------------------------------------- destinations

func TestDestinationRoundTripPreservesMatrixProfileExactly(t *testing.T) {
	d := testDB(t)

	want := routing.Profile{
		Mode: routing.ModeMatrix,
		Matrix: []routing.Cell{
			{Track: 0, Channel: 0, Out: routing.OutL, Gain: 1.0},
			{Track: 0, Channel: 1, Out: routing.OutR, Gain: 0.5},
			{Track: 3, Channel: 4, Out: routing.OutL, Gain: 0.25},
			{Track: 3, Channel: 5, Out: routing.OutR, Gain: 0.25},
		},
		Normalize:  routing.NormLimiter,
		SampleRate: 44100,
	}

	dst := validDest()
	dst.Profile = want
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if !reflect.DeepEqual(got.Profile, want) {
		t.Errorf("profile did not survive JSON round trip\n got: %+v\nwant: %+v", got.Profile, want)
	}
}

func TestDestinationRoundTripPreservesScalarFields(t *testing.T) {
	d := testDB(t)

	dst := validDest()
	dst.Kind = DestSRT
	dst.Platform = PlatformTwitch
	dst.URL = "srt://ingest.example:9000"
	dst.StreamKey = ""
	dst.Enabled = true
	dst.AudioBitrate = 320

	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}

	if got.Name != "Main" || got.Kind != DestSRT || got.Platform != PlatformTwitch ||
		got.URL != "srt://ingest.example:9000" || !got.Enabled || got.AudioBitrate != 320 {
		t.Errorf("scalar fields did not survive round trip: %+v", got)
	}
	if got.AccountID != nil {
		t.Errorf("AccountID = %v, want nil for an unlinked destination", *got.AccountID)
	}
}

// Creating a destination used to be impossible: an empty profile went through
// ApplyDefaults alone, which produced six disabled tracks and then failed
// validation with "no track is enabled".
func TestCreateDestinationWithEmptyProfileGetsDefaultProfile(t *testing.T) {
	d := testDB(t)

	dst := validDest()
	dst.Profile = routing.Profile{}

	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination with an empty profile: %v", err)
	}

	want := routing.DefaultProfile()
	if !reflect.DeepEqual(created.Profile, want) {
		t.Errorf("empty profile was not seeded with DefaultProfile\n got: %+v\nwant: %+v", created.Profile, want)
	}
	if got := created.Profile.SelectedTracks(); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("SelectedTracks() = %v, want [0]", got)
	}
}

func TestCreateDestinationDefaultsBitrateAndPlatform(t *testing.T) {
	d := testDB(t)

	dst := validDest()
	dst.AudioBitrate = 0
	dst.Platform = ""

	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if created.AudioBitrate != 160 {
		t.Errorf("AudioBitrate = %d, want 160", created.AudioBitrate)
	}
	if created.Platform != PlatformCustom {
		t.Errorf("Platform = %q, want %q", created.Platform, PlatformCustom)
	}
}

func TestADestinationRoundTripsItsFacebookBlock(t *testing.T) {
	d := testDB(t)
	dst := validDest()
	dst.Platform = PlatformFacebook
	dst.URL = "rtmps://live-api.facebook.com:443/rtmp/"
	dst.Facebook = FacebookSettings{
		Crosspost: []CrosspostTarget{
			{PageID: "1234", CreatePost: true},
			{PageID: "5678"},
		},
		DonateCharityID: "999",
	}
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if len(got.Facebook.Crosspost) != 2 {
		t.Fatalf("crosspost = %+v, want two targets", got.Facebook.Crosspost)
	}
	// CreatePost is what selects enable_crossposting_and_create_post over
	// enable_crossposting. Losing it posts as a Page nobody asked to post as.
	if !got.Facebook.Crosspost[0].CreatePost || got.Facebook.Crosspost[1].CreatePost {
		t.Errorf("createPost flags = %v/%v, want true/false",
			got.Facebook.Crosspost[0].CreatePost, got.Facebook.Crosspost[1].CreatePost)
	}
	if got.Facebook.DonateCharityID != "999" {
		t.Errorf("donateCharityId = %q, want 999", got.Facebook.DonateCharityID)
	}
}

// The Create path above proves the column round-trips. It does not prove
// UPDATE writes it: a destination is edited far more often than it is
// created, and the crosspost list this task just made reachable from the
// dialog now travels through UpdateDestination on every save after the
// first. Writing "{}" there instead of the real value stays green on every
// other test in this file, because they all check GetDestination against a
// row that was only ever Created, never edited.
func TestUpdateDestinationPersistsTheFacebookBlock(t *testing.T) {
	d := testDB(t)
	dst := validDest()
	dst.Platform = PlatformFacebook
	dst.URL = "rtmps://live-api.facebook.com:443/rtmp/"
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if !created.Facebook.Empty() {
		t.Fatalf("a destination created with no Facebook block already carries one: %+v", created.Facebook)
	}

	created.Facebook = FacebookSettings{
		Crosspost:       []CrosspostTarget{{PageID: "1234", CreatePost: true}},
		DonateCharityID: "999",
	}
	updated, err := d.UpdateDestination(created)
	if err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if len(updated.Facebook.Crosspost) != 1 || updated.Facebook.Crosspost[0].PageID != "1234" {
		t.Fatalf("crosspost after update = %+v, want the one Page just set", updated.Facebook.Crosspost)
	}
	if updated.Facebook.DonateCharityID != "999" {
		t.Errorf("donateCharityId after update = %q, want 999", updated.Facebook.DonateCharityID)
	}

	// Read back independently of UpdateDestination's own return value, in case
	// the write silently failed but the in-memory struct still reflects what
	// the caller asked for.
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if len(got.Facebook.Crosspost) != 1 || got.Facebook.DonateCharityID != "999" {
		t.Errorf("stored facebook block = %+v, want the update to have persisted", got.Facebook)
	}
}

// This creates a fresh row through CreateDestination, which marshals a zero
// FacebookSettings the same way it would marshal any other value -- it does
// NOT exercise the column's SQL default or the migration path. What actually
// happens to a row that predates the facebook column is covered separately by
// TestMigrateDestinationExpertArgsBackfillsFacebookOnAnUpgradedDatabase below.
func TestADestinationWithNoFacebookBlockReadsBackEmpty(t *testing.T) {
	d := testDB(t)
	dst := validDest()
	dst.Name = "plain"
	created, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if len(got.Facebook.Crosspost) != 0 || got.Facebook.DonateCharityID != "" {
		t.Errorf("facebook = %+v, want zero", got.Facebook)
	}
}

// seedDestinations creates one destination per name and returns their ids in
// creation order, which is also the order they are listed in.
func seedDestinations(t *testing.T, d *DB, names ...string) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		dst := validDest()
		dst.Name = name
		created, err := d.CreateDestination(dst)
		if err != nil {
			t.Fatalf("CreateDestination(%q): %v", name, err)
		}
		ids = append(ids, created.ID)
	}
	return ids
}

func listedDestinationIDs(t *testing.T, d *DB) []int64 {
	t.Helper()
	rows, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestReorderDestinationsMakesListOrderMatchTheGivenIDs(t *testing.T) {
	d := testDB(t)
	ids := seedDestinations(t, d, "First", "Second", "Third")

	want := []int64{ids[2], ids[0], ids[1]}
	if err := d.ReorderDestinations(want); err != nil {
		t.Fatalf("ReorderDestinations: %v", err)
	}
	if got := listedDestinationIDs(t, d); !reflect.DeepEqual(got, want) {
		t.Errorf("list order = %v, want %v", got, want)
	}
}

// Position is display order, nothing more: the engine keys restarts off a spec
// hash that does not include it, and updated_at must not move either, or a
// reorder would look like an edit to every destination.
func TestReorderDestinationsLeavesEveryOtherFieldAlone(t *testing.T) {
	d := testDB(t)
	ids := seedDestinations(t, d, "First", "Second")

	before, err := d.GetDestination(ids[0])
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if err := d.ReorderDestinations([]int64{ids[1], ids[0]}); err != nil {
		t.Fatalf("ReorderDestinations: %v", err)
	}
	after, err := d.GetDestination(ids[0])
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}

	before.Position, after.Position = 0, 0
	if !reflect.DeepEqual(before, after) {
		t.Errorf("reorder changed more than position\nbefore: %+v\n after: %+v", before, after)
	}
}

func TestReorderDestinationsRejectsAnOrderItCannotApplyWhole(t *testing.T) {
	tests := []struct {
		name    string
		ids     func(seeded []int64) []int64
		wantErr string
	}{
		{
			name:    "an id that does not exist",
			ids:     func(s []int64) []int64 { return []int64{s[0], s[1], 9999} },
			wantErr: "destination 9999 does not exist",
		},
		{
			name:    "the same id twice",
			ids:     func(s []int64) []int64 { return []int64{s[0], s[1], s[1]} },
			wantErr: "listed twice",
		},
		{
			name:    "a subset that leaves a destination unplaced",
			ids:     func(s []int64) []int64 { return []int64{s[1], s[0]} },
			wantErr: "got 2 ids for 3 destinations",
		},
		{
			name:    "no ids at all",
			ids:     func(s []int64) []int64 { return nil },
			wantErr: "got 0 ids for 3 destinations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDB(t)
			seeded := seedDestinations(t, d, "First", "Second", "Third")

			err := d.ReorderDestinations(tt.ids(seeded))
			if err == nil {
				t.Fatalf("ReorderDestinations = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ReorderDestinations = %q, want it to contain %q", err, tt.wantErr)
			}
			if got := listedDestinationIDs(t, d); !reflect.DeepEqual(got, seeded) {
				t.Errorf("a rejected reorder was partially applied: order = %v, want %v", got, seeded)
			}
		})
	}
}

func TestDestinationValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Destination)
		wantErr string
	}{
		{
			name:   "a well-formed rtmp destination is accepted",
			mutate: func(d *Destination) {},
		},
		{
			name: "a well-formed srt destination is accepted",
			mutate: func(d *Destination) {
				d.Kind, d.URL, d.StreamKey = DestSRT, "srt://ingest.example:9000", ""
			},
		},
		{
			name: "a relative file destination is accepted",
			mutate: func(d *Destination) {
				d.Kind, d.URL, d.StreamKey = DestFile, "mixes/booth.mkv", ""
			},
		},
		{
			name:    "an empty name is rejected",
			mutate:  func(d *Destination) { d.Name = "" },
			wantErr: "name is required",
		},
		{
			name:    "a whitespace-only name is rejected",
			mutate:  func(d *Destination) { d.Name = "   " },
			wantErr: "name is required",
		},
		{
			name:    "an rtmp destination pointed at an srt url is rejected",
			mutate:  func(d *Destination) { d.URL = "srt://ingest.example:9000" },
			wantErr: "must start with rtmp:// or rtmps://",
		},
		{
			name:    "an rtmp destination pointed at an http url is rejected",
			mutate:  func(d *Destination) { d.URL = "https://ingest.example/live" },
			wantErr: "must start with rtmp:// or rtmps://",
		},
		{
			name:   "rtmps is accepted for an rtmp destination",
			mutate: func(d *Destination) { d.URL = "rtmps://ingest.example/live" },
		},
		{
			name: "an srt destination pointed at an rtmp url is rejected",
			mutate: func(d *Destination) {
				d.Kind, d.URL = DestSRT, "rtmp://ingest.example/live"
			},
			wantErr: "must start with srt://",
		},
		{
			name: "a file destination containing .. is rejected",
			mutate: func(d *Destination) {
				d.Kind, d.URL = DestFile, "../../etc/crontab"
			},
			wantErr: "must be a relative name inside the recordings directory",
		},
		{
			name: "a file destination with a .. segment mid-path is rejected",
			mutate: func(d *Destination) {
				d.Kind, d.URL = DestFile, "mixes/../../etc/crontab"
			},
			wantErr: "must be a relative name inside the recordings directory",
		},
		{
			name: "an absolute file destination is rejected",
			mutate: func(d *Destination) {
				d.Kind, d.URL = DestFile, "/etc/crontab"
			},
			wantErr: "must be a relative name inside the recordings directory",
		},
		{
			name:    "a bitrate below 32 kbps is rejected",
			mutate:  func(d *Destination) { d.AudioBitrate = 31 },
			wantErr: "out of range (32-512)",
		},
		{
			name:    "a bitrate above 512 kbps is rejected",
			mutate:  func(d *Destination) { d.AudioBitrate = 513 },
			wantErr: "out of range (32-512)",
		},
		{
			name:    "an unknown kind is rejected",
			mutate:  func(d *Destination) { d.Kind = "webrtc" },
			wantErr: `unknown destination kind "webrtc"`,
		},
		{
			name:    "an invalid routing profile is surfaced on the destination",
			mutate:  func(d *Destination) { d.Profile.SampleRate = 22050 },
			wantErr: "unsupported sample rate 22050",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDest()
			tt.mutate(d)

			err := d.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDestinationTarget(t *testing.T) {
	tests := []struct {
		name string
		dest Destination
		want string
	}{
		{
			name: "rtmp joins the stream key onto the url",
			dest: Destination{Kind: DestRTMP, URL: "rtmp://ingest.example/live", StreamKey: "abc-123"},
			want: "rtmp://ingest.example/live/abc-123",
		},
		{
			name: "rtmp does not double the separator on a trailing slash",
			dest: Destination{Kind: DestRTMP, URL: "rtmp://ingest.example/live/", StreamKey: "abc-123"},
			want: "rtmp://ingest.example/live/abc-123",
		},
		{
			name: "rtmp without a stream key is left alone",
			dest: Destination{Kind: DestRTMP, URL: "rtmp://ingest.example/live"},
			want: "rtmp://ingest.example/live",
		},
		{
			name: "srt never joins the stream key",
			dest: Destination{Kind: DestSRT, URL: "srt://ingest.example:9000", StreamKey: "abc-123"},
			want: "srt://ingest.example:9000",
		},
		{
			name: "file never joins the stream key",
			dest: Destination{Kind: DestFile, URL: "mixes/booth.mkv", StreamKey: "abc-123"},
			want: "mixes/booth.mkv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dest.Target(); got != tt.want {
				t.Errorf("Target() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------------ settings

func TestGetSettingsSeedsDefaultsOnFirstCall(t *testing.T) {
	d := testDB(t)

	got, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Errorf("first GetSettings() = %+v, want DefaultSettings()", got)
	}

	// The seed must have been persisted, not just returned.
	var n int
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM settings WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if n != 1 {
		t.Errorf("settings rows = %d, want 1 seeded row", n)
	}

	again, err := d.GetSettings()
	if err != nil {
		t.Fatalf("second GetSettings: %v", err)
	}
	if !reflect.DeepEqual(again, got) {
		t.Errorf("second GetSettings() = %+v, want %+v", again, got)
	}
}

func TestGetSettingsFillsDefaultsForFieldsMissingFromAnOlderBlob(t *testing.T) {
	d := testDB(t)

	// What a build that predated the preview and meter sections would have written.
	sparse := `{"ingest":{"mode":"rtmp","rtmp":{"port":1936,"app":"stream","streamKey":"k"}}}`
	if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, sparse); err != nil {
		t.Fatalf("seed sparse settings: %v", err)
	}

	got, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if got.Ingest.Mode != IngestRTMP || got.Ingest.RTMP.App != "stream" {
		t.Errorf("stored ingest values were lost: %+v", got.Ingest)
	}

	def := DefaultSettings()
	if got.Ingest.SRT != def.Ingest.SRT {
		t.Errorf("srt section = %+v, want defaults %+v", got.Ingest.SRT, def.Ingest.SRT)
	}
	if got.Preview != def.Preview {
		t.Errorf("preview section = %+v, want defaults %+v", got.Preview, def.Preview)
	}
	if got.Meters != def.Meters {
		t.Errorf("meters section = %+v, want defaults %+v", got.Meters, def.Meters)
	}
	if got.Recording != def.Recording {
		t.Errorf("recording section = %+v, want defaults %+v", got.Recording, def.Recording)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("settings recovered from an older blob must be valid: %v", err)
	}
}

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{
			name:   "the shipped defaults are valid",
			mutate: func(s *Settings) {},
		},
		{
			name:   "an empty srt passphrase means encryption off, not an error",
			mutate: func(s *Settings) { s.Ingest.SRT.Passphrase = "" },
		},
		{
			// Zero would be a coherent wish -- "do not buffer" -- but on the
			// page it is indistinguishable from chat being broken, and the
			// operator has no way to tell those apart.
			name:    "a zero chat history ring is refused rather than read as no-buffering",
			mutate:  func(s *Settings) { s.Chat.HistoryMessages = 0 },
			wantErr: "chat history",
		},
		{
			name:    "a chat history ring above the ceiling is refused",
			mutate:  func(s *Settings) { s.Chat.HistoryMessages = MaxChatHistoryMessages + 1 },
			wantErr: "chat history",
		},
		{
			name:   "the chat history ceiling itself is accepted",
			mutate: func(s *Settings) { s.Chat.HistoryMessages = MaxChatHistoryMessages },
		},
		{
			// 1 is "never retry", which is a real answer for an endpoint whose
			// owner would rather drop an alert than see it twice.
			name:   "one delivery attempt means never retry, and is allowed",
			mutate: func(s *Settings) { s.Alerts.RetryAttempts = 1 },
		},
		{
			name:    "zero delivery attempts would never deliver at all",
			mutate:  func(s *Settings) { s.Alerts.RetryAttempts = 0 },
			wantErr: "alert retry attempts",
		},
		{
			name:    "delivery attempts above the ceiling are refused",
			mutate:  func(s *Settings) { s.Alerts.RetryAttempts = MaxAlertRetryAttempts + 1 },
			wantErr: "alert retry attempts",
		},
		{
			name:   "a 10-character srt passphrase is accepted",
			mutate: func(s *Settings) { s.Ingest.SRT.Passphrase = strings.Repeat("a", 10) },
		},
		{
			name:   "a 79-character srt passphrase is accepted",
			mutate: func(s *Settings) { s.Ingest.SRT.Passphrase = strings.Repeat("a", 79) },
		},
		{
			name:    "a 9-character srt passphrase is rejected",
			mutate:  func(s *Settings) { s.Ingest.SRT.Passphrase = strings.Repeat("a", 9) },
			wantErr: "srt passphrase must be 10-79 characters (got 9)",
		},
		{
			name:    "an 80-character srt passphrase is rejected",
			mutate:  func(s *Settings) { s.Ingest.SRT.Passphrase = strings.Repeat("a", 80) },
			wantErr: "srt passphrase must be 10-79 characters (got 80)",
		},
		{
			// The listeners are install-wide now; a source has no port to
			// clash with. What can still collide is the two listeners.
			name:    "the srt and rtmp listeners sharing a port is rejected",
			mutate:  func(s *Settings) { s.Listeners.RTMPPort = s.Listeners.SRTPort },
			wantErr: "srt and rtmp listeners cannot share port 6000",
		},
		{
			name:    "a listener port out of range is rejected",
			mutate:  func(s *Settings) { s.Listeners.SRTPort = 70000 },
			wantErr: "srt listener port 70000 out of range",
		},
		{
			// Port 0 is not an error to the kernel -- it means "any free
			// port" -- so a listener bound to it would come up, report itself
			// listening, and be reachable at an address nobody was told.
			name:    "listener port 0 is rejected rather than bound",
			mutate:  func(s *Settings) { s.Listeners.SRTPort = 0 },
			wantErr: "srt listener port 0 out of range",
		},
		{
			name:    "an unknown ingest mode is rejected",
			mutate:  func(s *Settings) { s.Ingest.Mode = "webrtc" },
			wantErr: `unknown ingest mode "webrtc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultSettings()
			tt.mutate(&s)

			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// --------------------------------------------------------------------- users

func TestCreateUserRefusesToRunTwice(t *testing.T) {
	d := testDB(t)

	if has, err := d.HasUser(); err != nil || has {
		t.Fatalf("HasUser() = %v, %v on a fresh database; want false, nil", has, err)
	}
	if _, err := d.CreateUser("admin", "correct-horse"); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	if has, err := d.HasUser(); err != nil || !has {
		t.Fatalf("HasUser() = %v, %v after setup; want true, nil", has, err)
	}

	// Setup must not be a takeover vector once an install is live.
	if _, err := d.CreateUser("attacker", "hunter2hunter2"); err == nil {
		t.Fatal("second CreateUser succeeded; setup must refuse to run twice")
	}

	u, err := d.GetUser()
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("Username = %q, want %q — the second CreateUser overwrote the admin", u.Username, "admin")
	}
}

func TestCreateUserRejectsWeakInput(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "a password shorter than the minimum is rejected", username: "admin", password: "short"},
		{name: "an empty username is rejected", username: "", password: "correct-horse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDB(t)
			if _, err := d.CreateUser(tt.username, tt.password); err == nil {
				t.Fatal("CreateUser() = nil error, want a rejection")
			}
			if has, _ := d.HasUser(); has {
				t.Error("a rejected CreateUser still wrote a user row")
			}
		})
	}
}

func TestCheckPassword(t *testing.T) {
	d := testDB(t)
	if _, err := d.CreateUser("admin", "correct-horse"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := d.GetUser()
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{name: "the right password is accepted", password: "correct-horse", want: true},
		{name: "a wrong password is rejected", password: "correct-horsey", want: false},
		{name: "an empty password is rejected", password: "", want: false},
		{name: "the bcrypt hash itself is not accepted as the password", password: u.hash, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := u.CheckPassword(tt.password); got != tt.want {
				t.Errorf("CheckPassword(%q) = %v, want %v", tt.password, got, tt.want)
			}
		})
	}
}

func TestSetPasswordReplacesTheStoredHash(t *testing.T) {
	d := testDB(t)
	if _, err := d.CreateUser("admin", "correct-horse"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := d.GetUser()
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if err := d.SetPassword(u.ID, "battery-staple"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	u, err = d.GetUser()
	if err != nil {
		t.Fatalf("GetUser after SetPassword: %v", err)
	}
	if !u.CheckPassword("battery-staple") {
		t.Error("the new password was not accepted")
	}
	if u.CheckPassword("correct-horse") {
		t.Error("the old password still works after SetPassword")
	}
}

// --------------------------------------------------------------- oauth state

func TestTakeOAuthStateIsSingleUse(t *testing.T) {
	d := testDB(t)

	if err := d.PutOAuthState("state-abc", PlatformYouTube, "verifier-xyz"); err != nil {
		t.Fatalf("PutOAuthState: %v", err)
	}

	p, verifier, err := d.TakeOAuthState("state-abc")
	if err != nil {
		t.Fatalf("first TakeOAuthState: %v", err)
	}
	if p != PlatformYouTube || verifier != "verifier-xyz" {
		t.Errorf("TakeOAuthState = (%q, %q), want (%q, %q)", p, verifier, PlatformYouTube, "verifier-xyz")
	}

	// A replayed callback must find nothing.
	if _, _, err := d.TakeOAuthState("state-abc"); err == nil {
		t.Fatal("second TakeOAuthState succeeded; the state must be consumed on first use")
	}
}

func TestTakeOAuthStateRejectsUnknownState(t *testing.T) {
	d := testDB(t)
	if _, _, err := d.TakeOAuthState("never-issued"); err == nil {
		t.Fatal("TakeOAuthState accepted a state that was never issued")
	}
}

func TestTakeOAuthStateRejectsAndConsumesAnExpiredState(t *testing.T) {
	d := testDB(t)

	if err := d.PutOAuthState("state-old", PlatformTwitch, "verifier"); err != nil {
		t.Fatalf("PutOAuthState: %v", err)
	}
	stale := time.Now().Add(-11 * time.Minute).Unix()
	if _, err := d.SQL().Exec(`UPDATE oauth_states SET created_at = ? WHERE state = ?`, stale, "state-old"); err != nil {
		t.Fatalf("age the state: %v", err)
	}

	if _, _, err := d.TakeOAuthState("state-old"); err == nil {
		t.Fatal("TakeOAuthState accepted a state older than the 10 minute window")
	}
	var n int
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE state = ?`, "state-old").Scan(&n); err != nil {
		t.Fatalf("count states: %v", err)
	}
	if n != 0 {
		t.Error("an expired state was left in the table for a second attempt")
	}
}

// TestTakeOAuthStateReturningWorksWithThisDriver pins the fact the fix for #8
// depends on: modernc.org/sqlite (the only driver this repo ships) honours
// `DELETE ... RETURNING`. Nothing else in the tree used RETURNING before this,
// so a driver upgrade that dropped it would otherwise only be caught by
// TakeOAuthState itself returning the wrong error, which reads like an
// unrelated failure.
func TestTakeOAuthStateReturningWorksWithThisDriver(t *testing.T) {
	d := testDB(t)
	if err := d.PutOAuthState("returning-probe", PlatformYouTube, "v"); err != nil {
		t.Fatalf("PutOAuthState: %v", err)
	}
	var p Platform
	var verifier string
	var created int64
	err := d.SQL().QueryRow(`DELETE FROM oauth_states WHERE state = ? RETURNING platform, verifier, created_at`,
		"returning-probe").Scan(&p, &verifier, &created)
	if err != nil {
		t.Fatalf("DELETE ... RETURNING is not supported by this driver/SQLite build: %v", err)
	}
	if p != PlatformYouTube || verifier != "v" {
		t.Errorf("RETURNING gave back (%q, %q), want (%q, %q)", p, verifier, PlatformYouTube, "v")
	}
}

// TestTakeOAuthStateIsAtomicUnderConcurrentCallbacks is finding #8: the old
// implementation ran a SELECT and then a separate DELETE. db.go's
// SetMaxOpenConns(1) only holds the pool's single connection for one
// statement at a time, not across two, so a second concurrent callback with
// the same state could acquire the connection between the first callback's
// SELECT and its DELETE and see the row too -- both callbacks would then
// treat a single-use state as valid. Two goroutines race the same state many
// times over; with the atomic `DELETE ... RETURNING` fix, exactly one can
// ever win, every time.
//
// Mutation: put the SELECT and DELETE back as two separate statements in
// TakeOAuthState (any two-statement form still compiles) -- observed FAIL,
// "trial N: 2 concurrent callbacks consumed state".
func TestTakeOAuthStateIsAtomicUnderConcurrentCallbacks(t *testing.T) {
	d := testDB(t)
	const trials = 30
	for i := 0; i < trials; i++ {
		state := fmt.Sprintf("race-state-%d", i)
		if err := d.PutOAuthState(state, PlatformYouTube, "verifier"); err != nil {
			t.Fatalf("PutOAuthState: %v", err)
		}

		var successes int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		for g := 0; g < 2; g++ {
			go func() {
				defer wg.Done()
				<-start
				if _, _, err := d.TakeOAuthState(state); err == nil {
					atomic.AddInt32(&successes, 1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if successes != 1 {
			t.Fatalf("trial %d: %d concurrent callbacks consumed state %q; want exactly 1 (single-use is not atomic)",
				i, successes, state)
		}
	}
}

// ----------------------------------------------------------- platform tokens

func TestPlatformCredsClientSecretIsEncryptedAtRest(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	const secret = "super-secret-client-secret"
	if err := d.PutPlatformCreds(box, PlatformYouTube, "client-id-123", secret); err != nil {
		t.Fatalf("PutPlatformCreds: %v", err)
	}

	// A leaked database file must not be a leaked set of live credentials.
	var enc []byte
	if err := d.SQL().QueryRow(`SELECT client_secret_enc FROM platform_creds WHERE platform = ?`,
		PlatformYouTube).Scan(&enc); err != nil {
		t.Fatalf("read raw client_secret_enc: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("client_secret_enc is empty")
	}
	if bytes.Contains(enc, []byte(secret)) {
		t.Errorf("client_secret_enc contains the plaintext secret: %q", enc)
	}

	got, err := d.GetPlatformCreds(box, PlatformYouTube)
	if err != nil {
		t.Fatalf("GetPlatformCreds: %v", err)
	}
	if got.ClientSecret != secret {
		t.Errorf("ClientSecret = %q, want %q", got.ClientSecret, secret)
	}
	if got.ClientID != "client-id-123" {
		t.Errorf("ClientID = %q, want %q", got.ClientID, "client-id-123")
	}
	if !got.HasSecret {
		t.Error("HasSecret = false, want true")
	}
}

func TestPlatformCredsWithTheWrongKeyDoNotDecrypt(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	if err := d.PutPlatformCreds(box, PlatformKick, "client-id", "super-secret"); err != nil {
		t.Fatalf("PutPlatformCreds: %v", err)
	}

	other, err := secrets.New(bytes.Repeat([]byte{0x99}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	if _, err := d.GetPlatformCreds(other, PlatformKick); err == nil {
		t.Fatal("GetPlatformCreds decrypted a secret with the wrong key")
	}
}

func TestPlatformAccountTokensAreEncryptedAtRest(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	const (
		access  = "ya29-access-token-plaintext"
		refresh = "1//refresh-token-plaintext"
	)
	acct, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform:     PlatformYouTube,
		AccountName:  "Main Channel",
		AccountRef:   "UC123",
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    time.Now().Add(time.Hour).Truncate(time.Second),
		Scopes:       "youtube.readonly",
	})
	if err != nil {
		t.Fatalf("UpsertPlatformAccount: %v", err)
	}

	var accessEnc, refreshEnc []byte
	if err := d.SQL().QueryRow(
		`SELECT access_token_enc, refresh_token_enc FROM platform_accounts WHERE id = ?`, acct.ID).
		Scan(&accessEnc, &refreshEnc); err != nil {
		t.Fatalf("read raw token columns: %v", err)
	}
	if bytes.Contains(accessEnc, []byte(access)) {
		t.Errorf("access_token_enc contains the plaintext token: %q", accessEnc)
	}
	if bytes.Contains(refreshEnc, []byte(refresh)) {
		t.Errorf("refresh_token_enc contains the plaintext token: %q", refreshEnc)
	}

	got, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if got.AccessToken != access {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, access)
	}
	if got.RefreshToken != refresh {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, refresh)
	}

	// ListPlatformAccounts feeds the settings UI and must never carry tokens.
	list, err := d.ListPlatformAccounts()
	if err != nil {
		t.Fatalf("ListPlatformAccounts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListPlatformAccounts returned %d accounts, want 1", len(list))
	}
	if list[0].AccessToken != "" || list[0].RefreshToken != "" {
		t.Error("ListPlatformAccounts returned token material")
	}
}

// A token refresh response carries no new refresh token, and losing the old
// one would silently break unattended reconnects.
func TestUpsertPlatformAccountKeepsTheRefreshTokenWhenNoneIsSupplied(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	if _, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform: PlatformTwitch, AccountRef: "42",
		AccessToken: "access-1", RefreshToken: "refresh-1",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	got, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform: PlatformTwitch, AccountRef: "42",
		AccessToken: "access-2",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if got.AccessToken != "access-2" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-2")
	}
	if got.RefreshToken != "refresh-1" {
		t.Errorf("RefreshToken = %q, want the retained %q", got.RefreshToken, "refresh-1")
	}
}

// ------------------------------------------------------------- pull settings

// The pull URL only has to be dialable when pull is actually the ingest mode.
// The asymmetry is the point: a half-filled pull form must not stop somebody
// saving an unrelated SRT change, and switching to pull must not silently start
// an ingest with nowhere to dial.
func TestSettingsValidatePullURLOnlyInPullMode(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{
			name: "pull mode with an http source is accepted",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "https://example.test/live.ts"
			},
		},
		{
			name: "pull mode with an rtsp camera is accepted",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "rtsp://cam.local/stream1"
			},
		},
		{
			name: "pull mode with a relative file source is accepted",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "file://loops/bars.ts"
			},
		},
		{
			name:    "pull mode with no URL is rejected",
			mutate:  func(s *Settings) { s.Ingest.Mode = IngestPull },
			wantErr: "pull source URL is required",
		},
		{
			name: "pull mode with a scheme outside the allowlist is rejected",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "gopher://example.test/1"
			},
			wantErr: "unsupported pull source scheme",
		},
		{
			name: "a file source escaping the data directory is rejected",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "file://../../etc/shadow"
			},
			wantErr: "must be a relative path inside the data directory",
		},
		{
			name: "an absolute file source is rejected",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestPull
				s.Ingest.Pull.URL = "file:///etc/shadow"
			},
			wantErr: "must be a relative path inside the data directory",
		},
		{
			// The half-filled form. Nothing about SRT depends on the pull URL.
			name:   "srt mode ignores an empty pull URL",
			mutate: func(s *Settings) { s.Ingest.Mode = IngestSRT },
		},
		{
			name: "rtmp mode ignores a nonsense pull URL",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestRTMP
				s.Ingest.Pull.URL = "not a url at all"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultSettings()
			tt.mutate(&s)
			assertValidate(t, s, tt.wantErr)
		})
	}
}

// The pull tuning is range-checked whatever the mode, because a value out of
// range is a typo now and a dead ingest the next time somebody switches to pull.
func TestSettingsValidatePullTuning(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{
			name:   "zero reconnect delay means the built-in default",
			mutate: func(s *Settings) { s.Ingest.Pull.ReconnectDelayMaxSeconds = 0 },
		},
		{
			name:   "the maximum reconnect delay is accepted",
			mutate: func(s *Settings) { s.Ingest.Pull.ReconnectDelayMaxSeconds = 3600 },
		},
		{
			name:    "a negative reconnect delay is rejected",
			mutate:  func(s *Settings) { s.Ingest.Pull.ReconnectDelayMaxSeconds = -1 },
			wantErr: "pull reconnect delay -1s out of range",
		},
		{
			name:    "a reconnect delay past an hour is rejected",
			mutate:  func(s *Settings) { s.Ingest.Pull.ReconnectDelayMaxSeconds = 3601 },
			wantErr: "pull reconnect delay 3601s out of range",
		},
		{
			name:   "an empty rtsp transport means the default",
			mutate: func(s *Settings) { s.Ingest.Pull.RTSPTransport = "" },
		},
		{
			name:   "udp_multicast is a transport FFmpeg knows",
			mutate: func(s *Settings) { s.Ingest.Pull.RTSPTransport = "udp_multicast" },
		},
		{
			name:    "a misspelt rtsp transport is caught in the form",
			mutate:  func(s *Settings) { s.Ingest.Pull.RTSPTransport = "tpc" },
			wantErr: `unknown rtsp transport "tpc"`,
		},
		{
			// Range checks run in srt mode too: the tuning is stored either way.
			name: "the tuning is checked even when pull is not the mode",
			mutate: func(s *Settings) {
				s.Ingest.Mode = IngestSRT
				s.Ingest.Pull.ReconnectDelayMaxSeconds = 99999
			},
			wantErr: "pull reconnect delay 99999s out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultSettings()
			tt.mutate(&s)
			assertValidate(t, s, tt.wantErr)
		})
	}
}

// Silence synthesis defaults ON. A video-only ingest is refused by every major
// platform, so the default that "just works" is the one that fixes it — and an
// upgraded install has to inherit it rather than keeping the old broken
// behaviour because its stored blob predates the field.
func TestSilenceOnVideoOnlyDefaultsOnIncludingForAnOlderBlob(t *testing.T) {
	if !DefaultSettings().Synth.SilenceOnVideoOnly {
		t.Error("DefaultSettings().Synth.SilenceOnVideoOnly = false, want true")
	}

	d := testDB(t)
	// A settings blob written before the field existed.
	if _, err := d.SQL().Exec(
		`INSERT INTO settings (id, json) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET json = excluded.json`,
		`{"ingest":{"mode":"srt","srt":{"port":6000,"latencyMs":200}}}`); err != nil {
		t.Fatalf("seed old settings: %v", err)
	}
	s, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !s.Synth.SilenceOnVideoOnly {
		t.Error("an upgraded install did not inherit the silence default")
	}
}

func assertValidate(t *testing.T, s Settings, wantErr string) {
	t.Helper()
	err := s.Validate()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("Validate() = nil, want an error containing %q", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Errorf("Validate() = %q, want it to contain %q", err, wantErr)
	}
}

// ------------------------------------------------------------- expert args

// Expert arguments now live on the destination row rather than in a sidecar,
// because the engine folds them into the restart signature and a signature
// assembled from two reads of two tables can be assembled from a torn pair.
func TestDestinationRoundTripPreservesExpertArgs(t *testing.T) {
	d := testDB(t)

	created, err := d.CreateDestination(&Destination{
		Name: "twitch", Kind: DestRTMP, URL: "rtmp://ingest.example/app",
		StreamKey: "key", AudioBitrate: 160,
		ExtraInputArgs: "-analyzeduration 10M", ExtraOutputArgs: `-metadata "title=My Show"`,
		ExpertAckReencode: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ExtraInputArgs != "-analyzeduration 10M" {
		t.Errorf("input args = %q", created.ExtraInputArgs)
	}
	if created.ExtraOutputArgs != `-metadata "title=My Show"` {
		t.Errorf("output args = %q", created.ExtraOutputArgs)
	}
	if !created.ExpertAckReencode {
		t.Error("the acknowledgement did not survive the round trip")
	}

	// And clearing them clears them, rather than leaving the previous strings
	// because an empty value looked like "field omitted".
	created.ExtraInputArgs, created.ExtraOutputArgs = "", ""
	created.ExpertAckReencode = false
	updated, err := d.UpdateDestination(created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ExpertArgsSet() || updated.ExpertAckReencode {
		t.Errorf("expert args survived being cleared: %+v", updated)
	}
}

// A destination that predates expert mode reads back as two empty strings,
// which is exactly "expert mode off" — never as a NULL scan error.
func TestMigrateDestinationExpertArgsIsIdempotentAndDefaultsToOff(t *testing.T) {
	d := testDB(t)
	// Open already ran it once; running it again must be a no-op rather than a
	// duplicate-column error, because it runs on every open.
	if err := d.MigrateDestinationExpertArgs(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	created, err := d.CreateDestination(&Destination{
		Name: "plain", Kind: DestSRT, URL: "srt://example.test:9000", AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ExpertArgsSet() {
		t.Errorf("a fresh destination reports expert args: %+v", created)
	}
}

// The sidecar table expert mode first shipped in is drained, not abandoned. A
// developer upgrading across that change keeps the arguments they saved.
func TestMigrateDestinationExpertArgsDrainsTheOldSidecarTable(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(&Destination{
		Name: "twitch", Kind: DestRTMP, URL: "rtmp://ingest.example/app", AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := d.SQL().Exec(`CREATE TABLE destination_expert_args (
		destination_id INTEGER PRIMARY KEY,
		input_args TEXT NOT NULL DEFAULT '',
		output_args TEXT NOT NULL DEFAULT '',
		ack_reencode INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create sidecar: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO destination_expert_args VALUES (?, '-re', '-muxdelay 0', 1, 0)`,
		created.ID); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	if err := d.MigrateDestinationExpertArgs(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	moved, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if moved.ExtraInputArgs != "-re" || moved.ExtraOutputArgs != "-muxdelay 0" || !moved.ExpertAckReencode {
		t.Errorf("sidecar values were not folded in: %+v", moved)
	}
	// And the table is gone, so there is one answer to "what does this
	// destination run with" rather than two that can disagree.
	if ok, err := tableExists(d.SQL(), "destination_expert_args"); err != nil || ok {
		t.Errorf("sidecar table still present (exists=%v err=%v)", ok, err)
	}
}

// TestMigrateDestinationExpertArgsBackfillsFacebookOnAnUpgradedDatabase builds
// a destinations table exactly as it was the release before this one -- every
// column MigrateDestinationExpertArgs has ever added except facebook itself --
// because that is the one column an upgrading install is actually missing.
// Same shape of proof as TestMigrateRenditionsUpgradesAPreRenditionsDatabase
// in renditions_test.go, narrowed to the one column this task added: the
// claim "a pre-existing row reads back as a zero FacebookSettings" is a claim
// about the ALTER's default and the scan guard, and neither of those run
// unless the column is actually missing when Open() is called.
func TestMigrateDestinationExpertArgsBackfillsFacebookOnAnUpgradedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-facebook.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE destinations (
		id                      INTEGER PRIMARY KEY AUTOINCREMENT,
		name                    TEXT    NOT NULL,
		kind                    TEXT    NOT NULL,
		platform                TEXT    NOT NULL DEFAULT '',
		account_id              INTEGER,
		url                     TEXT    NOT NULL DEFAULT '',
		stream_key              TEXT    NOT NULL DEFAULT '',
		enabled                 INTEGER NOT NULL DEFAULT 0,
		audio_bitrate           INTEGER NOT NULL DEFAULT 160,
		profile                 TEXT    NOT NULL,
		rendition_id            INTEGER,
		source_id               INTEGER,
		extra_input_args        TEXT    NOT NULL DEFAULT '',
		extra_output_args       TEXT    NOT NULL DEFAULT '',
		expert_ack_reencode     INTEGER NOT NULL DEFAULT 0,
		tr_no_duration_filesize INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_packets    INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_bytes      INTEGER NOT NULL DEFAULT 0,
		tr_rw_timeout_seconds   INTEGER NOT NULL DEFAULT 0,
		rs_min_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_max_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_give_up_after        INTEGER NOT NULL DEFAULT 0,
		au_codec                TEXT    NOT NULL DEFAULT '',
		au_mono                 INTEGER NOT NULL DEFAULT 0,
		compliance              TEXT    NOT NULL DEFAULT '{}',
		position                INTEGER NOT NULL DEFAULT 0,
		created_at              INTEGER NOT NULL,
		updated_at              INTEGER NOT NULL
	);
	INSERT INTO destinations
		(name, kind, platform, url, stream_key, enabled, audio_bitrate, profile,
		 rendition_id, source_id, compliance, position, created_at, updated_at)
	VALUES ('Legacy FB', 'rtmp', 'facebook', 'rtmps://live-api.facebook.com:443/rtmp/', 'abc-123', 1, 160,
		'{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],"normalize":"auto","sampleRate":48000}',
		NULL, NULL, '{}', 0, 1000, 1000);`); err != nil {
		t.Fatalf("build pre-facebook database: %v", err)
	}
	// Proving the column really is absent before Open ever runs, the same way
	// TestMigrateRenditionsUpgradesAPreRenditionsDatabase proves rendition_id
	// is absent: otherwise a schema drift elsewhere could make this pass for a
	// reason that has nothing to do with the facebook migration.
	if _, err := old.Exec(`SELECT facebook FROM destinations`); err == nil {
		t.Fatal("facebook column already exists on the hand-built table; this test proves nothing")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	// Open, and only Open, is what an existing install actually runs on
	// startup; it must migrate the missing column, because nothing else will.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-facebook database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations after migration: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListDestinations = %d rows, want the pre-existing destination", len(list))
	}
	legacy := list[0]
	if legacy.Name != "Legacy FB" {
		t.Fatalf("pre-existing destination came back as %+v", legacy)
	}
	if len(legacy.Facebook.Crosspost) != 0 || legacy.Facebook.DonateCharityID != "" {
		t.Errorf("Facebook = %+v, want zero -- a pre-existing row must not start "+
			"sending Facebook parameters nobody set", legacy.Facebook)
	}
}

// THE SILENT HALF OF THE MIGRATION. backup_ingest_wanted arrives as
// `INTEGER NOT NULL DEFAULT 0`, and 0 is the correct answer for a new row and
// the wrong one for every install that already had redundancy switched on: the
// intent used to live inside the facebook blob, as `{"backupIngest":true}`.
// A bare ALTER answers "no" for all of them, wantsBackup goes false, and
// nothing anywhere says so -- the operator finds out when a primary connection
// drops and the second feed that was supposed to catch it was never started.
//
// So this builds the row in the shape it was actually written in, with the
// column genuinely absent, and asks whether the intent came back. Same
// technique as TestMigrateDestinationExpertArgsBackfillsFacebookOnAnUpgradedDatabase
// above; the claim is about the UPDATE that follows the ALTER, which does not
// run unless the column is missing when Open() is called.
//
// Mutation, run against a committed tree: in MigrateDestinationExpertArgs,
// `UPDATE destinations SET backup_ingest_wanted = 1` -> `... = 0`, which is
// exactly the bare-ALTER behaviour. Observed FAIL.
func TestMigrateDestinationsCarriesBackupIntentOutOfTheFacebookBlob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-backup-intent.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	// Every column the release before this one had, including facebook and the
	// two backup endpoint columns -- and NOT backup_ingest_wanted, which is the
	// single column an upgrading install is missing.
	if _, err := old.Exec(`CREATE TABLE destinations (
		id                      INTEGER PRIMARY KEY AUTOINCREMENT,
		name                    TEXT    NOT NULL,
		kind                    TEXT    NOT NULL,
		platform                TEXT    NOT NULL DEFAULT '',
		account_id              INTEGER,
		url                     TEXT    NOT NULL DEFAULT '',
		stream_key              TEXT    NOT NULL DEFAULT '',
		enabled                 INTEGER NOT NULL DEFAULT 0,
		audio_bitrate           INTEGER NOT NULL DEFAULT 160,
		profile                 TEXT    NOT NULL,
		rendition_id            INTEGER,
		source_id               INTEGER,
		extra_input_args        TEXT    NOT NULL DEFAULT '',
		extra_output_args       TEXT    NOT NULL DEFAULT '',
		expert_ack_reencode     INTEGER NOT NULL DEFAULT 0,
		tr_no_duration_filesize INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_packets    INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_bytes      INTEGER NOT NULL DEFAULT 0,
		tr_rw_timeout_seconds   INTEGER NOT NULL DEFAULT 0,
		rs_min_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_max_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_give_up_after        INTEGER NOT NULL DEFAULT 0,
		au_codec                TEXT    NOT NULL DEFAULT '',
		au_mono                 INTEGER NOT NULL DEFAULT 0,
		compliance              TEXT    NOT NULL DEFAULT '{}',
		facebook                TEXT    NOT NULL DEFAULT '{}',
		backup_url              TEXT    NOT NULL DEFAULT '',
		backup_stream_key       TEXT    NOT NULL DEFAULT '',
		position                INTEGER NOT NULL DEFAULT 0,
		created_at              INTEGER NOT NULL,
		updated_at              INTEGER NOT NULL
	);
	INSERT INTO destinations
		(name, kind, platform, url, stream_key, enabled, audio_bitrate, profile,
		 rendition_id, source_id, compliance, facebook, backup_url, backup_stream_key,
		 position, created_at, updated_at)
	VALUES ('Redundant FB', 'rtmp', 'facebook', 'rtmps://live-api.facebook.com:443/rtmp/', 'abc-123', 1, 160,
		'{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],"normalize":"auto","sampleRate":48000}',
		NULL, NULL, '{}', '{"backupIngest":true}', 'rtmps://backup.example/rtmp', 'backup-key',
		0, 1000, 1000),
	       ('Ordinary FB', 'rtmp', 'facebook', 'rtmps://live-api.facebook.com:443/rtmp/', 'def-456', 1, 160,
		'{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],"normalize":"auto","sampleRate":48000}',
		NULL, NULL, '{}', '{}', '', '',
		1, 1000, 1000);`); err != nil {
		t.Fatalf("build pre-backup-intent database: %v", err)
	}
	if _, err := old.Exec(`SELECT backup_ingest_wanted FROM destinations`); err == nil {
		t.Fatal("backup_ingest_wanted already exists on the hand-built table; this test proves nothing")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-backup-intent database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations after migration: %v", err)
	}
	byName := map[string]*Destination{}
	for _, row := range list {
		byName[row.Name] = row
	}
	redundant, ok := byName["Redundant FB"]
	if !ok {
		t.Fatalf("the pre-existing rows did not survive the migration: %d rows", len(list))
	}
	if !redundant.BackupIngestWanted {
		t.Error("an install that had redundancy switched on came back with it OFF. " +
			"Nothing reports this: the destination goes live with one feed, and the " +
			"operator learns about it the first time the primary drops.")
	}
	// The other direction, so the backfill cannot pass by setting everything.
	// A row that never asked for redundancy must not start paying for it --
	// double the upload bandwidth, on an operator who did not choose it.
	if ordinary := byName["Ordinary FB"]; ordinary == nil || ordinary.BackupIngestWanted {
		t.Errorf("a destination that never asked for redundancy came back wanting it: %+v", ordinary)
	}
}

// legacyDestinationsDB writes a destinations table in the shape the release
// before backup_ingest_wanted actually had, with one row carrying the intent
// inside the facebook blob. Shared by the two migration guards below so they
// cannot drift apart on what "the previous release" means.
func legacyDestinationsDB(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE destinations (
		id                      INTEGER PRIMARY KEY AUTOINCREMENT,
		name                    TEXT    NOT NULL,
		kind                    TEXT    NOT NULL,
		platform                TEXT    NOT NULL DEFAULT '',
		account_id              INTEGER,
		url                     TEXT    NOT NULL DEFAULT '',
		stream_key              TEXT    NOT NULL DEFAULT '',
		enabled                 INTEGER NOT NULL DEFAULT 0,
		audio_bitrate           INTEGER NOT NULL DEFAULT 160,
		profile                 TEXT    NOT NULL,
		rendition_id            INTEGER,
		source_id               INTEGER,
		extra_input_args        TEXT    NOT NULL DEFAULT '',
		extra_output_args       TEXT    NOT NULL DEFAULT '',
		expert_ack_reencode     INTEGER NOT NULL DEFAULT 0,
		tr_no_duration_filesize INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_packets    INTEGER NOT NULL DEFAULT 0,
		tr_mux_queue_bytes      INTEGER NOT NULL DEFAULT 0,
		tr_rw_timeout_seconds   INTEGER NOT NULL DEFAULT 0,
		rs_min_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_max_backoff_seconds  INTEGER NOT NULL DEFAULT 0,
		rs_give_up_after        INTEGER NOT NULL DEFAULT 0,
		au_codec                TEXT    NOT NULL DEFAULT '',
		au_mono                 INTEGER NOT NULL DEFAULT 0,
		compliance              TEXT    NOT NULL DEFAULT '{}',
		facebook                TEXT    NOT NULL DEFAULT '{}',
		backup_url              TEXT    NOT NULL DEFAULT '',
		backup_stream_key       TEXT    NOT NULL DEFAULT '',
		position                INTEGER NOT NULL DEFAULT 0,
		created_at              INTEGER NOT NULL,
		updated_at              INTEGER NOT NULL
	);
	INSERT INTO destinations
		(name, kind, platform, url, stream_key, enabled, audio_bitrate, profile,
		 rendition_id, source_id, compliance, facebook, backup_url, backup_stream_key,
		 position, created_at, updated_at)
	VALUES ('Redundant FB', 'rtmp', 'facebook', 'rtmps://live-api.facebook.com:443/rtmp/', 'abc-123', 1, 160,
		'{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],"normalize":"auto","sampleRate":48000}',
		NULL, NULL, '{}', '{"backupIngest":true}', 'rtmps://backup.example/rtmp', 'backup-key',
		0, 1000, 1000);`); err != nil {
		t.Fatalf("build legacy database: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	return path
}

// The ALTER and its backfill are one transaction, and this is the case that
// decides it.
//
// The backfill only runs when THIS pass created the column. So a crash -- or
// any error -- between the ALTER committing and the UPDATE running would leave
// a database where the column exists, the guard is false for ever after, and
// every operator who had redundancy on has silently lost it. Nothing reports
// that. They find out when a primary drops and the second feed that was
// supposed to catch it was never started.
//
// Either both arrive or neither does, so the next open sees a state it
// recognises and tries again.
//
// Mutation proving it can fail: in MigrateDestinationExpertArgs, change
// `defer func() { _ = tx.Rollback() }()` to `defer func() { _ = tx.Commit() }()`,
// so the error path commits the ALTER it was supposed to unwind. Measured:
// FAIL, "the column survived a failed migration" -- and the retry guard below
// fails with it.
//
// THE OBVIOUS MUTATION IS THE WRONG ONE, and it is worth writing down why.
// Taking the ALTERs back out of the transaction -- `tx.Exec(c.ddl)` ->
// `d.sql.Exec(c.ddl)` -- does not fail this test. It HANGS: db.go sets
// SetMaxOpenConns(1), so a statement issued on d.sql while tx holds the one
// connection waits for a connection tx will not release until it commits. That
// is the same deadlock the comment above the existence checks warns about, and
// a hanging test proves nothing at all -- it never reaches an assertion.
func TestAFailedBackfillTakesTheColumnWithIt(t *testing.T) {
	path := legacyDestinationsDB(t, "interrupted-migration.db")

	orig := backupIntentBackfill
	backupIntentBackfill = `UPDATE destinations SET backup_ingest_wanted = 1 WHERE no_such_column = 1`
	t.Cleanup(func() { backupIntentBackfill = orig })

	if d, err := Open(path); err == nil {
		d.Close()
		t.Fatal("Open reported success while its data migration failed")
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw sqlite: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`SELECT backup_ingest_wanted FROM destinations`); err == nil {
		t.Error("the column survived a failed migration. The next open will see it, " +
			"skip the backfill for ever, and every install that had redundancy on " +
			"keeps a column that says it does not.")
	}
}

// And the recovery the atomicity is FOR: the operator restarts, and the second
// attempt completes. A rollback that left the database unopenable would be a
// different bug wearing the same fix.
func TestTheMigrationSucceedsOnTheAttemptAfterAFailedOne(t *testing.T) {
	path := legacyDestinationsDB(t, "retried-migration.db")

	orig := backupIntentBackfill
	backupIntentBackfill = `UPDATE destinations SET backup_ingest_wanted = 1 WHERE no_such_column = 1`
	if d, err := Open(path); err == nil {
		d.Close()
		t.Fatal("Open reported success while its data migration failed")
	}
	backupIntentBackfill = orig

	d, err := Open(path)
	if err != nil {
		t.Fatalf("the retry could not open the database the rollback left behind: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	list, err := d.ListDestinations()
	if err != nil {
		t.Fatalf("ListDestinations after the retried migration: %v", err)
	}
	if len(list) != 1 || !list[0].BackupIngestWanted {
		t.Errorf("the retry did not carry the intent across: %+v", list)
	}
}

// The state database holds every destination stream key in plaintext, so it
// must not be readable by other accounts on the machine.
//
// ISSUE #297. SQLite creates the file under the process umask, which is 0644 on
// a default install -- measured, not assumed -- and both shipped deployments put
// it in a world-traversable directory: the unit file's install notes make
// /var/lib/polyemesis with a plain `mkdir -p` under umask 022, and the
// Dockerfile did the same for /data. Neither set UMask=, so nothing narrowed it
// at runtime either. Any local user could read every key.
//
// THE SIDECARS ARE ASSERTED TOO, and they are not a formality. SQLite creates
// -wal and -shm with the permissions of the main database rather than from the
// umask, so this test is what pins that behaviour: a reader who cannot open the
// database can still read committed pages out of the write-ahead log, and a
// future SQLite or driver change that stopped copying the mode would reopen the
// hole silently.
//
// Unix only. Windows has no mode bits and internal/fsperm implements the same
// intent with an ACL there; fsperm_windows_test.go is where that is asserted.
//
// Mutation: delete the fsperm.SecureFile call in Open.
// Observed to fail with "polyemesis.db is mode 0644, want no access for group
// or other".
func TestTheStateDatabaseIsNotReadableByOtherAccounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are a Unix concept; see fsperm_windows_test.go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// A write, so the WAL exists to be checked. Any write does; this one needs
	// no fixture beyond what Open already created.
	if _, err := store.sql.Exec(`CREATE TABLE IF NOT EXISTS perm_probe (x INTEGER)`); err != nil {
		t.Fatalf("write: %v", err)
	}

	checked := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		fi, err := os.Stat(p)
		if err != nil {
			// -shm can be absent depending on journal mode and platform. An
			// absent file is not a leak, but it is also not evidence, so it is
			// skipped rather than counted.
			continue
		}
		checked++
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is mode %#o, want no access for group or other. It carries "+
				"destination stream keys in plaintext, and a stream key is a credential: "+
				"whoever reads one can broadcast to the owner's channel", filepath.Base(p), perm)
		}
	}
	// Without this a future change that stopped creating the database at all --
	// or renamed it -- would leave every assertion above unexecuted and this
	// test green.
	if checked < 2 {
		t.Fatalf("only %d of the database files existed to check; expected at least "+
			"the database and its write-ahead log", checked)
	}
}

// An EXISTING install, where the sidecars were created before this code was.
//
// ISSUE #297, and the case the first version of that fix missed. SQLite gives
// -wal and -shm the mode of the main database WHEN IT CREATES THEM, so a test
// that opens a fresh database sees them inherit and passes. On an install that
// already has them, it does not create them -- and chmodding only the database
// left them at whatever the old umask gave them.
//
// Found by deploying to a real server running an older build:
//
//	-rw-------  polyemesis.db        <- fixed
//	-rw-r--r--  polyemesis.db-wal    <- still world-readable
//	-rw-r--r--  polyemesis.db-shm
//
// and `sudo -u nobody head -c 32 polyemesis.db-wal` succeeded against it.
//
// This test manufactures that state: open, write, close, deliberately widen
// every file, then REOPEN. Only the reopen path can fix it, which is exactly
// what an upgrade does.
//
// Mutation: narrow Open's loop back to `fsperm.SecureFile(path)` alone.
// Observed to fail with "polyemesis.db-wal is mode 0644 after reopening".
func TestReopeningAnOlderInstallSecuresTheSidecarsToo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are a Unix concept; see fsperm_windows_test.go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close()
	if _, err := first.sql.Exec(`CREATE TABLE IF NOT EXISTS perm_probe (x INTEGER)`); err != nil {
		t.Fatalf("write: %v", err)
	}

	// HELD OPEN while the modes are widened, and that is the fixture rather
	// than an accident. A clean Close checkpoints and DELETES -wal and -shm, so
	// a closed database has no sidecars to widen and the state this test is
	// about cannot be built. An older install has them because a server is
	// running.
	widened := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o644); err == nil {
			widened++
		}
	}
	if widened < 2 {
		t.Fatalf("only %d file(s) existed to widen; the fixture did not build the "+
			"state this test is about", widened)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	checked := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(path + suffix)
		if err != nil {
			continue
		}
		checked++
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("polyemesis.db%s is mode %#o after reopening. An upgrade is the "+
				"only moment an existing install gets fixed, and -wal carries committed "+
				"pages that have not been checkpointed -- a reader who cannot open the "+
				"database still sees recent writes there", suffix, perm)
		}
	}
	if checked < 2 {
		t.Fatalf("only %d file(s) survived the reopen to be checked", checked)
	}
}
