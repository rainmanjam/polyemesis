package api

import (
	"encoding/json"
	"strings"
	"testing"

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
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatSRT)
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
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatVTT)
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
	out, err := renderCaptionExport(captionExportFixture(), transcribe.FormatJSON)
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
