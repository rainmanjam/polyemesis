package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// Caption export: one recording's stored transcript as a file a player or an
// editor loads, with the speaker on every cue.
//
// This is #8's export requirement, from two sources:
//
//   - the stored transcript (the default, source=recording), which the offline
//     worker made from the recording after the fact, with the programme's caption
//     policy applied on the way out;
//   - the live cue log (source=live), the captions viewers saw during the
//     broadcast, lined up with the recording's own timeline. The policy was
//     applied to those when they went out, so it is not applied again.

// captionExportDoc is the JSON export's stable shape. Confidence is a pointer so
// a segment whose confidence was never measured is omitted, not reported as 0.
type captionExportDoc struct {
	RecordingID int64                  `json:"recordingId"`
	Recording   string                 `json:"recording,omitempty"`
	Speakers    []string               `json:"speakers"`
	Segments    []captionExportSegment `json:"segments"`
}

type captionExportSegment struct {
	Track      int      `json:"track"`
	Speaker    string   `json:"speaker,omitempty"`
	StartMS    int64    `json:"startMs"`
	EndMS      int64    `json:"endMs"`
	Text       string   `json:"text"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// captionExport is one rendered file: its body and its media type.
type captionExport struct {
	body        []byte
	contentType string
}

// parseCaptionExportFormat accepts exactly the three caption formats. It is an
// allowlist on the raw string rather than a lookup in transcribe's catalogue,
// because the catalogue also holds plain text, which is not a caption file.
func parseCaptionExportFormat(raw string) (transcribe.SubtitleFormat, bool) {
	switch f := transcribe.SubtitleFormat(raw); f {
	case transcribe.FormatSRT, transcribe.FormatVTT, transcribe.FormatJSON:
		return f, true
	}
	return "", false
}

// captionExportFileName is the Content-Disposition value. It is built from the
// numeric recording id and the validated format, and nothing else.
func captionExportFileName(recordingID int64, f transcribe.SubtitleFormat) string {
	return fmt.Sprintf(`attachment; filename="recording-%d%s"`, recordingID, f.Ext())
}

// captionExportSource is where an export's words come from.
type captionExportSource string

const (
	captionSourceRecording captionExportSource = "recording"
	captionSourceLive      captionExportSource = "live"
)

// parseCaptionExportSource accepts exactly the two sources, with absent meaning
// the stored transcript.
func parseCaptionExportSource(raw string) (captionExportSource, bool) {
	switch captionExportSource(raw) {
	case "", captionSourceRecording:
		return captionSourceRecording, true
	case captionSourceLive:
		return captionSourceLive, true
	}
	return "", false
}

// liveCaptionWindowCap bounds the window read for a recording whose duration is
// not known yet (still being written, or never probed).
const liveCaptionWindowCap = 24 * time.Hour

// liveCaptionTranscript lines logged live cues up with a recording: each cue is
// placed at its offset from the recording's StartedAt, a cue wholly outside the
// recording is left out, and one that straddles either end is clipped to it.
// Each cue keeps its track and speaker, so the export still says who spoke.
func liveCaptionTranscript(rec db.Recording, cues []captions.LoggedCue) db.Transcript {
	start := rec.StartedAt.UnixMilli()
	end := start + rec.DurationMS
	if rec.DurationMS <= 0 {
		end = start + liveCaptionWindowCap.Milliseconds()
	}
	tracks := map[int]*db.TranscriptTrack{}
	for _, c := range cues {
		if c.EndMS <= start || c.StartMS >= end {
			continue
		}
		s, e := max(c.StartMS, start), min(c.EndMS, end)
		tr := tracks[c.Track]
		if tr == nil {
			tr = &db.TranscriptTrack{RecordingID: rec.ID, Track: c.Track, Speaker: c.Speaker}
			tracks[c.Track] = tr
		}
		tr.Segments = append(tr.Segments, db.TranscriptSegment{
			RecordingID: rec.ID, Track: c.Track, Speaker: c.Speaker,
			StartMS: s - start, EndMS: e - start, Text: c.Text,
		})
	}
	t := db.Transcript{RecordingID: rec.ID, Recording: rec.Filename}
	for _, tr := range tracks {
		t.Tracks = append(t.Tracks, *tr)
	}
	sort.Slice(t.Tracks, func(i, j int) bool { return t.Tracks[i].Track < t.Tracks[j].Track })
	return t
}

// renderCaptionExport produces the file for one format from the merged,
// time-ordered transcript, with pol applied to every cue's text. SRT and VTT
// both carry speaker prefixes, because the export exists to say who spoke.
func renderCaptionExport(t db.Transcript, f transcribe.SubtitleFormat, pol captions.TextPolicy) (captionExport, error) {
	merged := t.Merged()
	for i := range merged {
		merged[i].Text = pol.Apply(merged[i].Text)
	}
	switch f {
	case transcribe.FormatSRT:
		body := transcribe.SRT(toTranscribeSegments(merged), transcribe.SubtitleOptions{Speakers: true})
		return captionExport{[]byte(body), "application/x-subrip; charset=utf-8"}, nil
	case transcribe.FormatVTT:
		body := transcribe.VTT(toTranscribeSegments(merged), transcribe.SubtitleOptions{Speakers: true})
		return captionExport{[]byte(body), "text/vtt; charset=utf-8"}, nil
	case transcribe.FormatJSON:
		doc := captionExportDoc{
			RecordingID: t.RecordingID,
			Recording:   t.Recording,
			Speakers:    captionExportSpeakers(t),
			Segments:    make([]captionExportSegment, 0, len(merged)),
		}
		for _, s := range merged {
			seg := captionExportSegment{
				Track:   s.Track,
				Speaker: s.Speaker,
				StartMS: s.StartMS,
				EndMS:   s.EndMS,
				Text:    s.Text,
			}
			if s.ConfidenceKnown {
				c := s.Confidence
				seg.Confidence = &c
			}
			doc.Segments = append(doc.Segments, seg)
		}
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return captionExport{}, err
		}
		return captionExport{append(raw, '\n'), "application/json; charset=utf-8"}, nil
	}
	return captionExport{}, fmt.Errorf("caption export: unsupported format %q", f)
}

// captionExportSpeakers lists each named track's speaker once, in track order.
func captionExportSpeakers(t db.Transcript) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, tr := range t.Tracks {
		if tr.Speaker == "" || seen[tr.Speaker] {
			continue
		}
		seen[tr.Speaker] = true
		out = append(out, tr.Speaker)
	}
	return out
}

// toTranscribeSegments converts the stored segments to the shape transcribe's
// subtitle writers take. Only the fields those writers read are carried over.
func toTranscribeSegments(in []db.TranscriptSegment) []transcribe.Segment {
	out := make([]transcribe.Segment, 0, len(in))
	for _, s := range in {
		out = append(out, transcribe.Segment{
			Track:   s.Track,
			Speaker: s.Speaker,
			StartMS: s.StartMS,
			EndMS:   s.EndMS,
			Text:    s.Text,
		})
	}
	return out
}

// handleExportTranscriptCaptions serves GET
// /library/recordings/{id}/transcript/export?format=srt|vtt|json[&source=recording|live].
// The id, the format and the source are the only request inputs, and all three
// are validated before the store is read. No path is built from the request:
// the cue log's path comes from the recording's numeric programme id.
func (s *Server) handleExportTranscriptCaptions(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	f, ok := parseCaptionExportFormat(r.URL.Query().Get("format"))
	if !ok {
		writeError(w, http.StatusBadRequest, "format must be srt, vtt or json")
		return
	}
	src, ok := parseCaptionExportSource(r.URL.Query().Get("source"))
	if !ok {
		writeError(w, http.StatusBadRequest, "source must be recording or live")
		return
	}
	if src == captionSourceLive {
		s.exportLiveCaptions(w, id, f)
		return
	}
	t, err := s.store.GetTranscript(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if t == nil || len(t.Tracks) == 0 {
		writeError(w, http.StatusNotFound, "no transcript for this recording")
		return
	}
	out, err := renderCaptionExport(*t, f, s.recordingCaptionPolicy(id))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "caption export failed")
		return
	}
	writeCaptionExport(w, out, captionExportFileName(id, f))
}

// exportLiveCaptions serves the live cue log for one recording's window.
func (s *Server) exportLiveCaptions(w http.ResponseWriter, id int64, f transcribe.SubtitleFormat) {
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if rec == nil || rec.SourceID == nil || s.cfg.DataDir == "" {
		writeError(w, http.StatusNotFound, "no live captions for this recording")
		return
	}
	end := rec.StartedAt.Add(time.Duration(rec.DurationMS) * time.Millisecond)
	if rec.DurationMS <= 0 {
		end = rec.StartedAt.Add(liveCaptionWindowCap)
	}
	cues, err := captions.ReadCueLog(captions.CueLogPath(s.cfg.DataDir, *rec.SourceID), rec.StartedAt, end)
	if err != nil {
		s.log.Warn("live caption export: the cue log could not be read", "recording", id, "err", err)
		writeError(w, http.StatusInternalServerError, "the live caption log could not be read")
		return
	}
	t := liveCaptionTranscript(*rec, cues)
	if len(t.Tracks) == 0 {
		writeError(w, http.StatusNotFound, "no live captions were logged during this recording")
		return
	}
	out, err := renderCaptionExport(t, f, captions.TextPolicy{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "caption export failed")
		return
	}
	writeCaptionExport(w, out, fmt.Sprintf(`attachment; filename="recording-%d-live%s"`, id, f.Ext()))
}

// recordingCaptionPolicy is the caption policy of the programme a recording was
// made from, or none when it has no programme (an older recording, or one whose
// programme was deleted). A failed read exports the words unchanged rather than
// refusing the export.
func (s *Server) recordingCaptionPolicy(id int64) captions.TextPolicy {
	rec, err := s.store.GetRecording(id)
	if err != nil || rec == nil || rec.SourceID == nil {
		return captions.TextPolicy{}
	}
	src, err := s.store.GetSource(*rec.SourceID)
	if err != nil || src == nil {
		return captions.TextPolicy{}
	}
	return captions.TextPolicy{Vocabulary: src.CaptionPolicy.Vocabulary, Blocklist: src.CaptionPolicy.Blocklist}
}

func writeCaptionExport(w http.ResponseWriter, out captionExport, disposition string) {
	w.Header().Set("Content-Type", out.contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out.body)
}
