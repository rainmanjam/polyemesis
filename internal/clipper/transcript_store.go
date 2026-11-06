package clipper

import (
	"errors"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// StoreTranscript reads a recording's transcript from the store as caption
// lines. The editor plans with it and the clip and highlight workers read the
// words through it at run time, so the stored shape is translated once. A
// recording with no transcript yields none; a store that cannot be read is an
// error.
func StoreTranscript(store *db.DB) TranscriptSource {
	return func(recordingID int64) ([]TranscriptLine, error) {
		tr, err := store.GetTranscript(recordingID)
		if errors.Is(err, db.ErrNotFound) || (err == nil && tr == nil) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		merged := tr.Merged()
		out := make([]TranscriptLine, 0, len(merged))
		for _, seg := range merged {
			out = append(out, TranscriptLine{Track: seg.Track, StartMS: seg.StartMS, EndMS: seg.EndMS, Text: seg.Text})
		}
		return out, nil
	}
}
