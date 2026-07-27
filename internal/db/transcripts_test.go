package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// seedRecording indexes a recording and returns its id.
func seedRecording(t *testing.T, d *DB, name string, started time.Time, durationMS int64) int64 {
	t.Helper()
	rec := &Recording{
		Filename:   name,
		StartedAt:  started,
		FinishedAt: started.Add(time.Duration(durationMS) * time.Millisecond),
		Bytes:      1024,
		DurationMS: durationMS,
		Tracks:     2,
	}
	if err := d.UpsertRecording(rec); err != nil {
		t.Fatalf("upsert recording %s: %v", name, err)
	}
	var id int64
	if err := d.sql.QueryRow(`SELECT id FROM recordings WHERE filename = ?`, name).Scan(&id); err != nil {
		t.Fatalf("recording id: %v", err)
	}
	return id
}

// seg is shorthand for a segment at a whole-second offset.
func seg(startSec, endSec int, text string) TranscriptSegment {
	return TranscriptSegment{
		StartMS: int64(startSec) * 1000,
		EndMS:   int64(endSec) * 1000,
		Text:    text,
	}
}

// seedTranscript stores a two-track transcript on recID.
func seedTranscript(t *testing.T, d *DB, recID int64) {
	t.Helper()
	err := d.SaveTranscript(&Transcript{
		RecordingID: recID,
		Tracks: []TranscriptTrack{
			{
				Track: 0, Speaker: "Host", Role: "host", Language: "en", Model: "base.en",
				Segments: []TranscriptSegment{
					seg(0, 3, "Welcome back to the show everyone"),
					seg(4, 7, "Today we are talking about multitrack audio routing"),
					seg(8, 11, "It is the good stream I promised you"),
				},
			},
			{
				Track: 1, Speaker: "Guest", Role: "guest", Language: "en", Model: "base.en",
				Segments: []TranscriptSegment{
					seg(3, 4, "Thanks for having me"),
					seg(12, 15, "Routing per destination is what sold me on it"),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("save transcript: %v", err)
	}
}

func TestSaveTranscriptStoresTracksAndSegments(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-a.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	got, err := d.GetTranscript(rec)
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(got.Tracks))
	}
	if got.Recording != "rec-a.mkv" {
		t.Errorf("recording filename = %q, want rec-a.mkv", got.Recording)
	}
	if n := got.SegmentCount(); n != 5 {
		t.Errorf("segment count = %d, want 5", n)
	}
	if sp := got.Speakers(); len(sp) != 2 || sp[0] != "Host" || sp[1] != "Guest" {
		t.Errorf("speakers = %v, want [Host Guest]", sp)
	}
	if got.Tracks[0].DurationMS != 11_000 {
		t.Errorf("track 0 duration = %d, want 11000", got.Tracks[0].DurationMS)
	}
}

func TestMergedInterleavesTracksIntoOneAttributedConversation(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-merge.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	got, err := d.GetTranscript(rec)
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	merged := got.Merged()
	wantSpeakers := []string{"Host", "Guest", "Host", "Host", "Guest"}
	if len(merged) != len(wantSpeakers) {
		t.Fatalf("merged length = %d, want %d", len(merged), len(wantSpeakers))
	}
	for i, want := range wantSpeakers {
		if merged[i].Speaker != want {
			t.Errorf("merged[%d].Speaker = %q, want %q (%q)", i, merged[i].Speaker, want, merged[i].Text)
		}
		if i > 0 && merged[i].StartMS < merged[i-1].StartMS {
			t.Errorf("merged[%d] starts before its predecessor", i)
		}
	}
}

func TestSaveTranscriptReplacesOnlyTheTracksItCarries(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-b.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	// Re-run track 1 alone with a bigger model. Track 0 must survive untouched.
	err := d.SaveTranscript(&Transcript{
		RecordingID: rec,
		Tracks: []TranscriptTrack{{
			Track: 1, Speaker: "Guest", Model: "large-v3",
			Segments: []TranscriptSegment{seg(3, 5, "Thank you very much for having me on")},
		}},
	})
	if err != nil {
		t.Fatalf("resave: %v", err)
	}

	got, err := d.GetTranscript(rec)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(got.Tracks))
	}
	if got.Tracks[0].Model != "base.en" || len(got.Tracks[0].Segments) != 3 {
		t.Errorf("track 0 was disturbed: model=%q segments=%d", got.Tracks[0].Model, len(got.Tracks[0].Segments))
	}
	if got.Tracks[1].Model != "large-v3" || len(got.Tracks[1].Segments) != 1 {
		t.Errorf("track 1 not replaced: model=%q segments=%d", got.Tracks[1].Model, len(got.Tracks[1].Segments))
	}
	// The superseded track-1 text must be gone from the index, not merely
	// hidden behind the new row.
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "sold me on it"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("stale track text still searchable: %d hits", len(hits))
	}
}

func TestSaveTranscriptSkipsEmptySegments(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-empty.mkv", time.Unix(1_700_000_000, 0), 60_000)
	err := d.SaveTranscript(&Transcript{
		RecordingID: rec,
		Tracks: []TranscriptTrack{{
			Track: 0,
			Segments: []TranscriptSegment{
				seg(0, 1, "real words"),
				seg(1, 2, "   "),
				seg(2, 3, ""),
			},
		}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := d.GetTranscript(rec)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if n := len(got.Tracks[0].Segments); n != 1 {
		t.Errorf("segments = %d, want 1 (silence rows dropped)", n)
	}
}

func TestSaveTranscriptRequiresRecordingID(t *testing.T) {
	d := testDB(t)
	if err := d.SaveTranscript(&Transcript{}); err == nil {
		t.Fatal("want error for a transcript with no recording")
	}
	if err := d.SaveTranscript(nil); err == nil {
		t.Fatal("want error for a nil transcript")
	}
}

func TestSearchFindsTermsPhrasesAndPrefixes(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-search.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	tests := []struct {
		name   string
		query  TranscriptQuery
		want   int
		expect string // substring the first hit must contain
	}{
		{
			name:   "single term",
			query:  TranscriptQuery{Text: "multitrack"},
			want:   1,
			expect: "multitrack",
		},
		{
			name:   "case insensitive",
			query:  TranscriptQuery{Text: "MULTITRACK"},
			want:   1,
			expect: "multitrack",
		},
		{
			name:   "phrase matches only in order",
			query:  TranscriptQuery{Text: `"the good stream"`},
			want:   1,
			expect: "good stream",
		},
		{
			name:  "phrase does not match scattered words",
			query: TranscriptQuery{Text: `"stream good the"`},
			want:  0,
		},
		{
			name:   "prefix search",
			query:  TranscriptQuery{Text: "rout", Prefix: true},
			want:   2,
			expect: "outing",
		},
		{
			name:  "same term without prefix does not match",
			query: TranscriptQuery{Text: "rout"},
			want:  0,
		},
		{
			name:   "explicit trailing star is a prefix",
			query:  TranscriptQuery{Text: "promis*"},
			want:   1,
			expect: "promised",
		},
		{
			name:  "two terms are an implicit AND",
			query: TranscriptQuery{Text: "routing destination"},
			want:  1,
		},
		{
			name:  "OR widens",
			query: TranscriptQuery{Text: "welcome OR thanks"},
			want:  2,
		},
		{
			name:  "NOT narrows",
			query: TranscriptQuery{Text: "routing NOT destination"},
			want:  1,
		},
		{
			name:  "no match",
			query: TranscriptQuery{Text: "helicopter"},
			want:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := d.SearchTranscripts(tc.query)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(hits) != tc.want {
				texts := make([]string, len(hits))
				for i, h := range hits {
					texts[i] = h.Text
				}
				t.Fatalf("hits = %d, want %d: %v", len(hits), tc.want, texts)
			}
			if tc.expect != "" && !strings.Contains(strings.ToLower(hits[0].Text), strings.ToLower(tc.expect)) {
				t.Errorf("first hit %q does not contain %q", hits[0].Text, tc.expect)
			}
		})
	}
}

func TestSearchSurvivesPunctuationThatIsFTS5Syntax(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-punct.mkv", time.Unix(1_700_000_000, 0), 60_000)
	if err := d.SaveTranscript(&Transcript{
		RecordingID: rec,
		Tracks: []TranscriptTrack{{Track: 0, Speaker: "Host", Segments: []TranscriptSegment{
			seg(0, 2, "It isn't ready -- the colon: is the problem"),
		}}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Every one of these is a syntax error if handed to FTS5 as typed. A
	// database error message instead of an answer is the bug being pinned.
	tests := []struct {
		name  string
		text  string
		want  int
		empty bool
	}{
		{name: "apostrophe", text: "isn't", want: 1},
		{name: "double dash", text: "ready -- colon", want: 1},
		{name: "colon", text: "colon:", want: 1},
		{name: "parens", text: "(ready)", want: 1},
		{name: "caret", text: "^ready", want: 1},
		{name: "only punctuation", text: "-- : ()", empty: true},
		{name: "blank", text: "   ", empty: true},
		{name: "dangling operator", text: "ready AND", want: 1},
		{name: "leading operator", text: "OR ready", want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := d.SearchTranscripts(TranscriptQuery{Text: tc.text})
			if tc.empty {
				if !errors.Is(err, ErrEmptyQuery) {
					t.Fatalf("err = %v, want ErrEmptyQuery", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("search %q: %v", tc.text, err)
			}
			if len(hits) != tc.want {
				t.Errorf("hits = %d, want %d", len(hits), tc.want)
			}
		})
	}
}

func TestMatchQueryBuildsSafeFTS5Expressions(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		prefix  bool
		want    string
		wantErr error
	}{
		{name: "single term", in: "hello", want: `"hello"`},
		{name: "two terms", in: "hello world", want: `"hello" "world"`},
		{name: "phrase preserved", in: `"hello world"`, want: `"hello world"`},
		{name: "phrase plus term", in: `"hello world" again`, want: `"hello world" "again"`},
		{name: "operators kept", in: "cats OR dogs", want: `"cats" OR "dogs"`},
		{name: "lowercase or is a term", in: "cats or dogs", want: `"cats" "or" "dogs"`},
		{name: "prefix on last term", in: "good str", prefix: true, want: `"good" "str"*`},
		{name: "explicit star", in: "str*", want: `"str"*`},
		{name: "punctuation stripped", in: "isn't -- it?", want: `"isn't" "it"`},
		{name: "unbalanced quotes still produce a valid expression", in: `say "he said`, want: `"say" "he said"`},
		{name: "nested quoting degrades into separate phrases", in: `say "he said ""hi"""`, want: `"say" "he said " "hi"`},
		{name: "dangling operators dropped", in: "AND cats AND", want: `"cats"`},
		{name: "double operator collapsed", in: "cats AND OR dogs", want: `"cats" OR "dogs"`},
		{name: "empty", in: "   ", wantErr: ErrEmptyQuery},
		{name: "punctuation only", in: "-- :: ()", wantErr: ErrEmptyQuery},
		{name: "operators only", in: "AND OR NOT", wantErr: ErrEmptyQuery},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchQuery(tc.in, tc.prefix)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MatchQuery(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("MatchQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSearchScopesToSpeakerAndTrack(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-scope.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	track1 := 1
	tests := []struct {
		name  string
		query TranscriptQuery
		want  int
	}{
		{name: "unscoped", query: TranscriptQuery{Text: "routing"}, want: 2},
		{name: "by speaker", query: TranscriptQuery{Text: "routing", Speaker: "Guest"}, want: 1},
		{name: "speaker is case insensitive", query: TranscriptQuery{Text: "routing", Speaker: "guest"}, want: 1},
		{name: "by track", query: TranscriptQuery{Text: "routing", Track: &track1}, want: 1},
		{name: "by recording", query: TranscriptQuery{Text: "routing", RecordingID: rec}, want: 2},
		{name: "wrong recording", query: TranscriptQuery{Text: "routing", RecordingID: rec + 999}, want: 0},
		{name: "unknown speaker", query: TranscriptQuery{Text: "routing", Speaker: "Nobody"}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := d.SearchTranscripts(tc.query)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(hits) != tc.want {
				t.Errorf("hits = %d, want %d", len(hits), tc.want)
			}
		})
	}
}

func TestSearchHitCarriesWallClockTimeAndSnippet(t *testing.T) {
	d := testDB(t)
	start := time.Unix(1_700_000_000, 0)
	rec := seedRecording(t, d, "rec-when.mkv", start, 60_000)
	seedTranscript(t, d, rec)

	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	h := hits[0]
	if h.Recording != "rec-when.mkv" {
		t.Errorf("recording = %q", h.Recording)
	}
	if h.StartMS != 4000 {
		t.Errorf("startMs = %d, want 4000", h.StartMS)
	}
	if want := start.Add(4 * time.Second); !h.At.Equal(want) {
		t.Errorf("At = %v, want %v", h.At, want)
	}
	if h.Speaker != "Host" || h.Track != 0 {
		t.Errorf("attribution = track %d %q, want track 0 Host", h.Track, h.Speaker)
	}
	if !strings.Contains(h.Snippet, HighlightOpen+"multitrack"+HighlightClose) {
		t.Errorf("snippet %q does not highlight the match", h.Snippet)
	}
	if h.Score <= 0 {
		t.Errorf("score = %v, want a positive relevance", h.Score)
	}
}

func TestSearchContextIncludesNeighboursFromTheSameTrackOnly(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-ctx.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack", Context: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	ctx := hits[0].Context
	for _, want := range []string{"Welcome back", "multitrack audio routing", "the good stream"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context %q missing %q", ctx, want)
		}
	}
	// "Thanks for having me" is track 1 and sits between the two track-0
	// utterances in time. Splicing it in would invent a sentence nobody said.
	if strings.Contains(ctx, "Thanks for having me") {
		t.Errorf("context %q crossed tracks", ctx)
	}
}

func TestSearchContextCanBeDisabled(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-noctx.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack", Context: -1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits[0].Context != "" {
		t.Errorf("context = %q, want empty", hits[0].Context)
	}
}

func TestSearchOrderingAndPaging(t *testing.T) {
	d := testDB(t)
	older := seedRecording(t, d, "rec-older.mkv", time.Unix(1_700_000_000, 0), 60_000)
	newer := seedRecording(t, d, "rec-newer.mkv", time.Unix(1_700_100_000, 0), 60_000)
	for _, rec := range []int64{older, newer} {
		if err := d.SaveTranscript(&Transcript{
			RecordingID: rec,
			Tracks: []TranscriptTrack{{Track: 0, Speaker: "Host", Segments: []TranscriptSegment{
				seg(0, 2, "routing one"),
				seg(3, 5, "routing two"),
			}}},
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	byTime, err := d.SearchTranscripts(TranscriptQuery{Text: "routing", Order: OrderTime})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(byTime) != 4 || byTime[0].RecordingID != older || byTime[3].RecordingID != newer {
		t.Fatalf("time order wrong: %d hits, first=%d last=%d", len(byTime), byTime[0].RecordingID, byTime[len(byTime)-1].RecordingID)
	}
	byRecent, err := d.SearchTranscripts(TranscriptQuery{Text: "routing", Order: OrderRecent})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if byRecent[0].RecordingID != newer {
		t.Errorf("recent order should lead with the newest recording")
	}

	page, err := d.SearchTranscripts(TranscriptQuery{Text: "routing", Order: OrderTime, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page) != 2 || page[0].SegmentID != byTime[2].SegmentID {
		t.Errorf("paging did not line up with the unpaged result")
	}

	n, err := d.CountTranscriptMatches(TranscriptQuery{Text: "routing"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 4 {
		t.Errorf("count = %d, want 4", n)
	}
}

func TestSearchBoundsLimitAndSnippetWidth(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-bounds.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	// An absurd limit and snippet width must be clamped, not passed through:
	// FTS5 errors on a snippet token count above 64.
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack", Limit: 100000, SnippetTokens: 100000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("hits = %d, want 1", len(hits))
	}
	if _, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack", Offset: -5}); err != nil {
		t.Errorf("negative offset: %v", err)
	}
}

func TestSearchTimeWindow(t *testing.T) {
	d := testDB(t)
	early := time.Unix(1_700_000_000, 0)
	late := time.Unix(1_700_100_000, 0)
	a := seedRecording(t, d, "rec-early.mkv", early, 60_000)
	b := seedRecording(t, d, "rec-late.mkv", late, 60_000)
	for _, rec := range []int64{a, b} {
		if err := d.SaveTranscript(&Transcript{RecordingID: rec, Tracks: []TranscriptTrack{{
			Track: 0, Segments: []TranscriptSegment{seg(0, 2, "routing")},
		}}}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "routing", Since: late.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].RecordingID != b {
		t.Errorf("Since did not narrow to the later recording")
	}
	hits, err = d.SearchTranscripts(TranscriptQuery{Text: "routing", Until: early.Add(time.Minute)})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].RecordingID != a {
		t.Errorf("Until did not narrow to the earlier recording")
	}
}

func TestRawQueryReportsSyntaxErrorsAsBadQuery(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-raw.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	if _, err := d.SearchTranscripts(TranscriptQuery{Text: `multitrack OR`, Raw: true}); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("err = %v, want ErrBadQuery", err)
	}
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: `multitrack OR welcome`, Raw: true})
	if err != nil {
		t.Fatalf("valid raw query: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("hits = %d, want 2", len(hits))
	}
}

func TestDeletingARecordingRemovesItsTranscriptFromTheIndex(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-del.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	if hits, _ := d.SearchTranscripts(TranscriptQuery{Text: "multitrack"}); len(hits) != 1 {
		t.Fatalf("precondition: hits = %d", len(hits))
	}
	if err := d.DeleteRecording(rec); err != nil {
		t.Fatalf("delete recording: %v", err)
	}
	// The cascade never passes through Go, so this is what proves the FTS
	// triggers are the thing keeping the index honest.
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("deleted recording still searchable: %d hits", len(hits))
	}
	if _, err := d.sql.Exec(`INSERT INTO transcript_fts(transcript_fts) VALUES('integrity-check')`); err != nil {
		t.Errorf("fts index corrupt after cascade: %v", err)
	}
}

func TestDeleteTranscriptAndTrack(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-dt.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	if err := d.DeleteTranscriptTrack(rec, 1); err != nil {
		t.Fatalf("delete track: %v", err)
	}
	if err := d.DeleteTranscriptTrack(rec, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
	tracks, err := d.ListTranscriptTracks(rec)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Track != 0 {
		t.Fatalf("tracks = %v", tracks)
	}
	if err := d.DeleteTranscript(rec); err != nil {
		t.Fatalf("delete transcript: %v", err)
	}
	has, err := d.HasTranscript(rec)
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if has {
		t.Error("transcript survived deletion")
	}
	// Deleting a transcript that is not there is not an error: the recording
	// sweeper calls it blind.
	if err := d.DeleteTranscript(rec); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestSetTranscriptSpeakerRewritesSegmentsSoSearchStaysScoped(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-relabel.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	if err := d.SetTranscriptSpeaker(rec, 1, "Dana"); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "routing", Speaker: "Dana"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Speaker != "Dana" {
		t.Errorf("hit speaker = %q, want Dana", hits[0].Speaker)
	}
	if old, _ := d.SearchTranscripts(TranscriptQuery{Text: "routing", Speaker: "Guest"}); len(old) != 0 {
		t.Errorf("old speaker label still matches")
	}
	if err := d.SetTranscriptSpeaker(rec, 7, "Nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("relabel of a missing track = %v, want ErrNotFound", err)
	}
	// The relabel is an UPDATE, which the update trigger must handle without
	// corrupting the index.
	if _, err := d.sql.Exec(`INSERT INTO transcript_fts(transcript_fts) VALUES('integrity-check')`); err != nil {
		t.Errorf("fts index corrupt after update: %v", err)
	}
}

func TestTranscriptSpeakersAndTranscribedRecordings(t *testing.T) {
	d := testDB(t)
	a := seedRecording(t, d, "rec-s1.mkv", time.Unix(1_700_000_000, 0), 60_000)
	b := seedRecording(t, d, "rec-s2.mkv", time.Unix(1_700_100_000, 0), 60_000)
	seedTranscript(t, d, a)

	speakers, err := d.TranscriptSpeakers()
	if err != nil {
		t.Fatalf("speakers: %v", err)
	}
	if len(speakers) != 2 || speakers[0] != "Guest" || speakers[1] != "Host" {
		t.Errorf("speakers = %v, want [Guest Host]", speakers)
	}
	got, err := d.TranscribedRecordings([]int64{a, b})
	if err != nil {
		t.Fatalf("transcribed: %v", err)
	}
	if !got[a] || got[b] {
		t.Errorf("transcribed map = %v, want only %d", got, a)
	}
	if m, err := d.TranscribedRecordings(nil); err != nil || len(m) != 0 {
		t.Errorf("empty input = %v, %v", m, err)
	}
}

func TestSearchScopesToSession(t *testing.T) {
	d := testDB(t)
	start := time.Unix(1_700_000_000, 0)
	a := seedRecording(t, d, "rec-ses-a.mkv", start, 3_600_000)
	b := seedRecording(t, d, "rec-ses-b.mkv", start.Add(2*time.Hour), 3_600_000)
	seedTranscript(t, d, a)
	seedTranscript(t, d, b)

	if _, err := d.BackfillSessions(DefaultSessionRules()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	sesA, err := d.SessionForRecording(a)
	if err != nil {
		t.Fatalf("session for a: %v", err)
	}
	hits, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack", SessionID: sesA.ID})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].RecordingID != a {
		t.Fatalf("session scope returned %d hits", len(hits))
	}
	if hits[0].SessionID != sesA.ID {
		t.Errorf("hit sessionId = %d, want %d", hits[0].SessionID, sesA.ID)
	}
	all, err := d.SearchTranscripts(TranscriptQuery{Text: "multitrack"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unscoped hits = %d, want 2", len(all))
	}
}

func TestListTranscriptSegmentsOrdersByTime(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-order.mkv", time.Unix(1_700_000_000, 0), 60_000)
	seedTranscript(t, d, rec)

	segs, err := d.ListTranscriptSegments(rec, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(segs) != 5 {
		t.Fatalf("segments = %d, want 5", len(segs))
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].StartMS < segs[i-1].StartMS {
			t.Fatalf("segment %d out of order", i)
		}
	}
	track := 1
	only, err := d.ListTranscriptSegments(rec, &track)
	if err != nil {
		t.Fatalf("list track: %v", err)
	}
	if len(only) != 2 {
		t.Errorf("track 1 segments = %d, want 2", len(only))
	}
}

func TestConfidenceKnownRoundTrips(t *testing.T) {
	d := testDB(t)
	rec := seedRecording(t, d, "rec-conf.mkv", time.Unix(1_700_000_000, 0), 60_000)
	if err := d.SaveTranscript(&Transcript{RecordingID: rec, Tracks: []TranscriptTrack{{
		Track: 0,
		Segments: []TranscriptSegment{
			{StartMS: 0, EndMS: 1000, Text: "measured", Confidence: 0.42, ConfidenceKnown: true},
			{StartMS: 1000, EndMS: 2000, Text: "unmeasured"},
		},
	}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	segs, err := d.ListTranscriptSegments(rec, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !segs[0].ConfidenceKnown || segs[0].Confidence != 0.42 {
		t.Errorf("measured segment = %+v", segs[0])
	}
	// Zero confidence with ConfidenceKnown false is "nobody asked", not "the
	// model thought this was garbage".
	if segs[1].ConfidenceKnown {
		t.Errorf("unmeasured segment claims a known confidence")
	}
}
