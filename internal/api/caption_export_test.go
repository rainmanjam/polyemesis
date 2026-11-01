package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// captionExportFixture is a two-microphone recording. Ana is on track 0 and Ben
// on track 1, and their utterances interleave, so a merged export that dropped
// the speaker or the time order would read differently from the one a viewer
// expects.
func captionExportFixture() db.Transcript {
	return db.Transcript{
		RecordingID: 7,
		Recording:   "rec-20261031-210000.mkv",
		Tracks: []db.TranscriptTrack{
			{
				Track:   0,
				Speaker: "Ana",
				Segments: []db.TranscriptSegment{
					{Track: 0, Speaker: "Ana", StartMS: 1500, EndMS: 3000, Text: "hello there", Confidence: 0.91, ConfidenceKnown: true},
					{Track: 0, Speaker: "Ana", StartMS: 9000, EndMS: 10250, Text: "back to the topic"},
				},
			},
			{
				Track:   1,
				Speaker: "Ben",
				Segments: []db.TranscriptSegment{
					{Track: 1, Speaker: "Ben", StartMS: 4000, EndMS: 5500, Text: "hi Ana"},
				},
			},
		},
	}
}

// TestCaptionExportSRTLabelsEachCueWithItsSpeakerInTimeOrder. SRT is the format
// the offline editors read, and the speaker prefix is the whole reason this
// route exists instead of the plain text view: a cue without its name is a line
// nobody can attribute.
func TestCaptionExportSRTLabelsEachCueWithItsSpeakerInTimeOrder(t *testing.T) {
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatSRT, captions.TextPolicy{})
	if err != nil {
		t.Fatalf("renderCaptionExport(srt): %v", err)
	}
	body := string(out.body)
	for _, want := range []string{
		"00:00:01,500 --> 00:00:03,000\nAna: hello there",
		"00:00:04,000 --> 00:00:05,500\nBen: hi Ana",
		"00:00:09,000 --> 00:00:10,250\nAna: back to the topic",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SRT export missing %q\n--- got ---\n%s", want, body)
		}
	}
	if strings.Index(body, "hello there") > strings.Index(body, "hi Ana") {
		t.Errorf("SRT export is not in time order:\n%s", body)
	}
	if out.contentType != "application/x-subrip; charset=utf-8" {
		t.Errorf("SRT content type = %q", out.contentType)
	}
}

// TestCaptionExportVTTHasTheHeaderAndPeriodTimestamps. A WebVTT file without its
// WEBVTT line is not parsed at all, and a comma in a timestamp makes every cue
// silently land at zero, so both are pinned here rather than trusted to the
// shared formatter.
func TestCaptionExportVTTHasTheHeaderAndPeriodTimestamps(t *testing.T) {
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatVTT, captions.TextPolicy{})
	if err != nil {
		t.Fatalf("renderCaptionExport(vtt): %v", err)
	}
	body := string(out.body)
	if !strings.HasPrefix(body, "WEBVTT\n") {
		t.Errorf("VTT export does not start with the WEBVTT header:\n%s", body)
	}
	if !strings.Contains(body, "00:00:01.500 --> 00:00:03.000\nAna: hello there") {
		t.Errorf("VTT export missing period-separated cue with speaker:\n%s", body)
	}
	if strings.Contains(body, "00:00:01,500") {
		t.Errorf("VTT export used SRT comma timestamps:\n%s", body)
	}
	if out.contentType != "text/vtt; charset=utf-8" {
		t.Errorf("VTT content type = %q", out.contentType)
	}
}

// TestCaptionExportJSONNamesSpeakerTrackAndTimesForEverySegment. The JSON is the
// only format that carries the track index and the confidence, so the export
// must keep both, and must say a confidence is absent rather than report 0.
func TestCaptionExportJSONNamesSpeakerTrackAndTimesForEverySegment(t *testing.T) {
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatJSON, captions.TextPolicy{})
	if err != nil {
		t.Fatalf("renderCaptionExport(json): %v", err)
	}
	var doc captionExportDoc
	if err := json.Unmarshal(out.body, &doc); err != nil {
		t.Fatalf("JSON export does not parse: %v\n%s", err, out.body)
	}
	if doc.RecordingID != 7 || doc.Recording != "rec-20261031-210000.mkv" {
		t.Errorf("JSON header = id %d recording %q", doc.RecordingID, doc.Recording)
	}
	if got := strings.Join(doc.Speakers, ","); got != "Ana,Ben" {
		t.Errorf("speakers = %q, want Ana,Ben", got)
	}
	if len(doc.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(doc.Segments))
	}
	first := doc.Segments[0]
	if first.Speaker != "Ana" || first.Track != 0 || first.StartMS != 1500 || first.EndMS != 3000 || first.Text != "hello there" {
		t.Errorf("first segment = %+v", first)
	}
	if first.Confidence == nil || *first.Confidence != 0.91 {
		t.Errorf("first confidence = %v, want 0.91", first.Confidence)
	}
	if doc.Segments[1].Confidence != nil {
		t.Errorf("segment without a known confidence reported %v; it must be absent, not 0", *doc.Segments[1].Confidence)
	}
}

// TestCaptionExportRefusesAnyFormatOutsideTheCaptionSet. The format is the only
// request input that reaches the output, so anything that is not one of the
// three caption formats is refused before the store is read. Plain text is
// refused on purpose: running prose is not a file a player loads. A traversal
// string is refused the same way, so no path is ever built from the request.
func TestCaptionExportRefusesAnyFormatOutsideTheCaptionSet(t *testing.T) {
	for _, raw := range []string{"", "txt", "SRT", "srt ", "../../etc/passwd", "srt/../vtt", "json\x00"} {
		if f, ok := parseCaptionExportFormat(raw); ok {
			t.Errorf("parseCaptionExportFormat(%q) accepted as %q", raw, f)
		}
	}
	for raw, want := range map[string]transcribe.SubtitleFormat{
		"srt":  transcribe.FormatSRT,
		"vtt":  transcribe.FormatVTT,
		"json": transcribe.FormatJSON,
	} {
		if f, ok := parseCaptionExportFormat(raw); !ok || f != want {
			t.Errorf("parseCaptionExportFormat(%q) = %q, %v; want %q", raw, f, ok, want)
		}
	}
}

// TestCaptionExportFileNameIsBuiltFromTheRecordingIDAndFormatOnly. The download
// name goes into a Content-Disposition header, so it must not carry anything
// the caller typed beyond a validated format. It is built from the numeric id
// and the fixed extension.
func TestCaptionExportFileNameIsBuiltFromTheRecordingIDAndFormatOnly(t *testing.T) {
	if got := captionExportFileName(42, transcribe.FormatVTT); got != `attachment; filename="recording-42.vtt"` {
		t.Errorf("captionExportFileName = %q", got)
	}
}

// TestCaptionExportAppliesTheProgrammesCaptionPolicy keeps the promise the
// policy makes: every output shows the same words. The stored transcript is the
// recogniser's raw text, so the export applies the programme's vocabulary and
// masked words on the way out, in every format.
//
// MUTATION: renderCaptionExport ignores the policy. Observed: FAIL, "hello" was
// exported unmasked.
func TestCaptionExportAppliesTheProgrammesCaptionPolicy(t *testing.T) {
	pol := captions.TextPolicy{Vocabulary: []string{"ANA"}, Blocklist: []string{"hello"}}
	for _, f := range []transcribe.SubtitleFormat{transcribe.FormatSRT, transcribe.FormatVTT, transcribe.FormatJSON} {
		out, err := renderCaptionExport(captionExportFixture(), f, pol)
		if err != nil {
			t.Fatalf("renderCaptionExport(%s): %v", f, err)
		}
		body := string(out.body)
		if strings.Contains(body, "hello there") || !strings.Contains(body, "h**** there") {
			t.Errorf("%s export did not mask the blocked word:\n%s", f, body)
		}
		if !strings.Contains(body, "hi ANA") {
			t.Errorf("%s export did not apply the vocabulary spelling:\n%s", f, body)
		}
	}
}

// TestLiveCaptionsAreLinedUpWithTheRecordingTheyWereSpokenDuring is the VOD
// alignment. The cue log keeps the wall-clock moment each caption was spoken;
// a recording's timeline starts at its StartedAt. A cue spoken 90 s after the
// recording began is at 1:30 in the file, a cue before the recording or after
// its end is left out, and one that straddles the start is clipped to it.
//
// MUTATION: the cues are not shifted by StartedAt. Observed: FAIL, the cue was
// placed at the Unix epoch's offset.
func TestLiveCaptionsAreLinedUpWithTheRecordingTheyWereSpokenDuring(t *testing.T) {
	start := time.UnixMilli(1_800_000_000_000)
	rec := db.Recording{ID: 9, Filename: "rec.mkv", StartedAt: start, DurationMS: 120_000}
	ms := start.UnixMilli()
	tr := liveCaptionTranscript(rec, []captions.LoggedCue{
		{StartMS: ms - 5000, EndMS: ms - 1000, Text: "before"},
		{StartMS: ms - 1000, EndMS: ms + 1500, Text: "straddles", Speaker: "Ana"},
		{StartMS: ms + 90_000, EndMS: ms + 92_000, Text: "ninety", Track: 1, Speaker: "Ben"},
		{StartMS: ms + 200_000, EndMS: ms + 201_000, Text: "after"},
	})
	merged := tr.Merged()
	if len(merged) != 2 {
		t.Fatalf("aligned cues = %+v, want the straddling and the ninety-second one", merged)
	}
	if merged[0].Text != "straddles" || merged[0].StartMS != 0 || merged[0].EndMS != 1500 {
		t.Errorf("straddling cue = %+v, want 0..1500", merged[0])
	}
	if merged[1].Text != "ninety" || merged[1].StartMS != 90_000 || merged[1].EndMS != 92_000 || merged[1].Speaker != "Ben" {
		t.Errorf("cue at 90 s = %+v, want 90000..92000 by Ben", merged[1])
	}
	out, err := renderCaptionExport(tr, transcribe.FormatVTT, captions.TextPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.body), "00:01:30.000 --> 00:01:32.000\nBen: ninety") {
		t.Errorf("VTT of the live captions:\n%s", out.body)
	}
}

// TestTheLiveCaptionExportReadsTheProgrammesCueLogForTheRecording drives the
// route with source=live: the recording's programme's cue log is read for the
// recording's window and served as a caption file named for the recording and
// the live source. Any other source value is refused before anything is read.
//
// MUTATION: the handler ignores source=live and serves the stored transcript.
// Observed: FAIL, 404 (no transcript).
func TestTheLiveCaptionExportReadsTheProgrammesCueLogForTheRecording(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	srv.cfg.DataDir = t.TempDir()
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	srcID := int64(1)
	plantRecordingOnDisk(t, store, srv.eng().Recordings().Dir(), db.Recording{
		Filename: "live-caption-export.mkv", StartedAt: started, FinishedAt: time.Now(),
		Bytes: 4096, DurationMS: 600_000, Tracks: 1, SourceID: &srcID,
	}, 1)
	recs, err := store.ListRecordings()
	if err != nil || len(recs) != 1 {
		t.Fatalf("recordings = %d (%v), want 1", len(recs), err)
	}
	if recs[0].SourceID == nil {
		recs[0].SourceID = &srcID
		if err := store.UpsertRecording(&recs[0]); err != nil {
			t.Fatal(err)
		}
	}
	id := recs[0].ID
	cl, err := captions.OpenCueLog(captions.CueLogPath(srv.cfg.DataDir, srcID), 0)
	if err != nil {
		t.Fatal(err)
	}
	spoken := started.Add(30 * time.Second)
	if err := cl.Append(transcribe.LiveCaption{Segment: transcribe.Segment{EndMS: 2000, Text: "what viewers saw"},
		At: spoken.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	_ = cl.Close()

	base := "/api/v1/library/recordings/" + itoa(id) + "/transcript/export?format=vtt"
	r := jsonRequest(t, http.MethodGet, base+"&source=live", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "00:00:30.000 --> 00:00:32.000\nwhat viewers saw") {
		t.Fatalf("live export = %d:\n%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="recording-`+itoa(id)+`-live.vtt"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	for _, bad := range []string{"&source=LIVE", "&source=../x", "&source=both"} {
		r := jsonRequest(t, http.MethodGet, base+bad, nil)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("source %q answered %d, want 400", bad, w.Code)
		}
	}
}
