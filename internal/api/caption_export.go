package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// Caption export: one recording's stored transcript as a file a player or an
// editor loads, with the speaker on every cue.
//
// This is the VOD half of #8's export requirement. It reads the transcript the
// offline worker already stored, so it covers the recording after the fact. The
// live cue log (what a viewer saw during the broadcast) is not built here yet.

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

// renderCaptionExport produces the file for one format from the merged,
// time-ordered transcript. SRT and VTT both carry speaker prefixes, because the
// export exists to say who spoke.
func renderCaptionExport(t db.Transcript, f transcribe.SubtitleFormat) (captionExport, error) {
	merged := t.Merged()
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
// /library/recordings/{id}/transcript/export?format=srt|vtt|json. The id and the
// format are the only request inputs, and both are validated before the store is
// read.
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
	t, err := s.store.GetTranscript(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if t == nil || len(t.Tracks) == 0 {
		writeError(w, http.StatusNotFound, "no transcript for this recording")
		return
	}
	out, err := renderCaptionExport(*t, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "caption export failed")
		return
	}
	w.Header().Set("Content-Type", out.contentType)
	w.Header().Set("Content-Disposition", captionExportFileName(id, f))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out.body)
}
