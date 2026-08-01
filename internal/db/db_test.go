package db

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
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
