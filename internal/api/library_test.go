package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The search is the headline of this whole workstream, and it is reached by a
// GET so a result can be bookmarked and shared. That makes the query string a
// public interface, and these pin it.

func TestSearchQueryReadsTheQueryString(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr error
		check   func(*testing.T, db.TranscriptQuery)
	}{
		{
			name:  "the text is trimmed and prefix matching is opt-in",
			query: "q=%20the%20mic%20",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if q.Text != "the mic" {
					t.Errorf("text = %q, want %q", q.Text, "the mic")
				}
				if q.Prefix {
					t.Error("prefix must not default on")
				}
			},
		},
		{
			name:  "prefix accepts the spellings a client actually sends",
			query: "q=mic&prefix=true",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if !q.Prefix {
					t.Error("prefix=true was not read")
				}
			},
		},
		{
			name:  "a bare date is accepted, because a date picker sends one",
			query: "q=mic&since=2026-07-01",
			check: func(t *testing.T, q db.TranscriptQuery) {
				want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
				if !q.Since.Equal(want) {
					t.Errorf("since = %v, want %v", q.Since, want)
				}
			},
		},
		{
			name:  "an RFC3339 instant is accepted, because a permalink carries one",
			query: "q=mic&until=2026-07-27T14%3A30%3A00Z",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if q.Until.IsZero() {
					t.Error("until was not parsed")
				}
			},
		},
		{
			name:  "track zero is distinguishable from no track at all",
			query: "q=mic&track=0",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if q.Track == nil || *q.Track != 0 {
					t.Errorf("track = %v, want a pointer to 0", q.Track)
				}
			},
		},
		{
			name:  "an absent track stays absent",
			query: "q=mic",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if q.Track != nil {
					t.Errorf("track = %v, want nil", q.Track)
				}
			},
		},
		{
			name:  "context may be negative, which is how a caller asks for none",
			query: "q=mic&context=-1",
			check: func(t *testing.T, q db.TranscriptQuery) {
				if q.Context != -1 {
					t.Errorf("context = %d, want -1", q.Context)
				}
			},
		},
		{
			// Both of these map to 400 rather than 500: they are the user's
			// query, and a 500 would have an operator restarting a healthy box.
			name:    "an empty query is refused with the store's own error",
			query:   "q=%20%20",
			wantErr: db.ErrEmptyQuery,
		},
		{
			name:    "an unknown order is refused",
			query:   "q=mic&order=alphabetical",
			wantErr: errAny,
		},
		{
			name:    "a negative offset is refused rather than handed to SQL",
			query:   "q=mic&offset=-5",
			wantErr: errAny,
		},
		{
			name:    "a non-numeric limit is refused",
			query:   "q=mic&limit=lots",
			wantErr: errAny,
		},
		{
			name:    "an unparseable date is refused",
			query:   "q=mic&since=last%20tuesday",
			wantErr: errAny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/library/search?"+tt.query, nil)
			q, err := searchQuery(r)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected an error, got %+v", q)
				}
				if !errors.Is(tt.wantErr, errAny) && !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, q)
		})
	}
}

// errAny is the sentinel for "any error will do"; the message is the handler's
// to word, not this test's to pin.
var errAny = errors.New("any error")

func TestWriteSearchErrorMapsUserQueriesTo400(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"an empty query is the caller's fault", db.ErrEmptyQuery, 400},
		{"a malformed raw query is the caller's fault", db.ErrBadQuery, 400},
		{"a missing row is a 404", db.ErrNotFound, 404},
		{"anything else is ours", errors.New("disk on fire"), 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeSearchError(w, tt.err)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestBoolParamAcceptsWhatAClientActuallySends(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"maybe", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := boolParam(tt.in); got != tt.want {
				t.Errorf("boolParam(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The session's face is the contact sheet, because it shows the shape of a whole
// broadcast rather than one arbitrary frame. Its absence must be reported as
// absence, not as a path to a file that is not there.
func TestRepresentativeFramePrefersTheContactSheet(t *testing.T) {
	tests := []struct {
		name  string
		write []string
		want  string
	}{
		{
			name:  "the contact sheet wins when both exist",
			write: []string{media.ContactSheetName, media.PosterName},
			want:  media.ContactSheetName,
		},
		{
			name:  "the poster frame stands in when there is no sheet",
			write: []string{media.PosterName},
			want:  media.PosterName,
		},
		{
			name:  "nothing generated means nothing to show",
			write: nil,
			want:  "",
		},
		{
			// An encoder killed mid-write leaves a zero-byte file behind, and
			// a thumbnail that renders as a broken image is worse than none.
			name:  "an empty file does not count as a generated one",
			write: []string{media.ContactSheetName + ":empty"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			const name = "rec-20260727-140000.mkv"
			layout := media.LayoutFor(dir, name)
			mkdirAll(t, layout.Dir)

			for _, f := range tt.write {
				body := []byte("jpeg")
				if name, ok := strings.CutSuffix(f, ":empty"); ok {
					f, body = name, nil
				}
				writeFile(t, filepath.Join(layout.Dir, f), body)
			}

			if got := representativeFrame(dir, name); got != tt.want {
				t.Errorf("representativeFrame = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAssetsForReportsWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	const name = "rec-20260727-140000.mkv"
	layout := media.LayoutFor(dir, name)

	if got := assetsFor(dir, name); got != (recordingAssets{}) {
		t.Fatalf("an empty derived directory must report nothing, got %+v", got)
	}

	mkdirAll(t, layout.Dir)
	writeFile(t, layout.Proxy, []byte("mp4"))
	// The sheets are numbered, so the VTT is the one file whose presence means
	// the whole sprite set was written.
	writeFile(t, layout.SpriteVTT, []byte("WEBVTT"))

	got := assetsFor(dir, name)
	if !got.Proxy {
		t.Error("the proxy was not detected")
	}
	if !got.Sprites {
		t.Error("the sprite set was not detected from its VTT")
	}
	if got.Poster || got.ContactSheet || got.Archive {
		t.Errorf("files that were never written were reported present: %+v", got)
	}
}

// mkdirAll and writeFile keep the table above readable; a t.Fatalf on every
// os call would be three lines of plumbing per fixture file.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A transcript is a record of a session, so the speaker labels it is submitted
// with are the operator's own words for the tracks: they come from the SOURCE's
// ingest annotations, and they are copied at submission rather than read when
// the job runs, so re-running after the roles were rearranged does not relabel
// an old session.
//
// Two things are asserted here and each has cost the product something before.
//
// WHICH ROW. Annotations used to live in the settings singleton and moved to
// the source's ingest block; the engine follows them there, the settings copy
// is a mirror a client reads back. Submitting from the mirror looks identical
// on any install where the mirror is fresh, and produces track1/track2/track3
// on every install where it is not -- which is exactly the regression
// apitail_annotations_test.go was written after.
//
// WITHOUT AN ENGINE. testServer has no manager at all, which is what makes
// this test the seam: the labels used to be read off s.eng().Settings(), so
// this call panicked -- an archived session could not be transcribed on an
// install whose pipeline was not up, which is the one thing post-production is
// supposed to survive.
func TestATranscribeJobCarriesTheSourcesAnnotationsWithNoEngine(t *testing.T) {
	s, _, store := testServer(t, config.Config{DataDir: t.TempDir()})

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Annotations = []routing.TrackAnnotation{
		{Track: 1, Role: routing.RoleMic, Label: "Host mic"},
	}
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("annotate the source: %v", err)
	}
	// Planted DIFFERENTLY in the settings mirror, so a job built from the wrong
	// row is a visibly wrong job rather than a coincidence.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Ingest.Annotations = []routing.TrackAnnotation{
		{Track: 1, Role: routing.RoleMusic, Label: "Stale mirror"},
	}
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("plant the mirror: %v", err)
	}

	job, err := s.transcribeJob(db.Recording{ID: 7, Filename: "rec-20240115-120000.mkv"}, submitRequest{})
	if err != nil {
		t.Fatalf("transcribeJob: %v", err)
	}
	var params transcribe.Params
	if err := json.Unmarshal(job.Params, &params); err != nil {
		t.Fatalf("decode job params: %v", err)
	}
	if len(params.Annotations) != 1 {
		t.Fatalf("the job carries %d annotations, want the source's one: %+v",
			len(params.Annotations), params.Annotations)
	}
	if got := params.Annotations[0].Label; got != "Host mic" {
		t.Errorf("the job was built with the %q annotation, so it came from the settings "+
			"mirror rather than the source row the engine reads", got)
	}
}
