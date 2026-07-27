// Package transcribe turns a recorded multitrack MKV into per-track
// transcripts and subtitle files using whisper.cpp.
//
// The differentiator is that polyemesis records every microphone on its OWN
// audio track. Transcribing track N in isolation is measurably better than
// transcribing a mix — no music under the voice, no second speaker bleeding
// into the acoustic model — and when two people are on two tracks, the track
// index IS the speaker attribution. No diarization model is involved anywhere
// in this package, and none is needed.
//
// Everything here is optional. whisper.cpp is an external binary that most
// installs will not have; its absence degrades this package to "no transcripts
// offered" and must never take down startup or the recordings page, exactly
// like ffmpeg.Detect's treatment of a build without SRT.
//
// Layout mirrors internal/ffmpeg: argument builders are pure functions from a
// spec struct to a []string so the command lines can be tested exhaustively,
// and nothing but worker.go spawns a process.
package transcribe

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Segment is one utterance with the identity of the track it came from.
//
// Track and Speaker are carried on every segment rather than only on the
// enclosing TrackTranscript because the interesting view — the merged,
// time-ordered conversation — flattens the tracks away, and a segment that has
// lost its track identity has lost the speaker attribution with it.
type Segment struct {
	// Track is the 0-based audio track index within the recording: the N in
	// FFmpeg's `-map 0:a:N`, and the same numbering routing.Source uses.
	Track int `json:"track"`
	// Speaker is the human name for that track, resolved once from the track's
	// role and label. It is denormalised deliberately; see above.
	Speaker string `json:"speaker,omitempty"`

	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`

	// Confidence is the mean token probability, 0..1. Whisper only reports the
	// per-token probabilities under --output-json-full, and older builds do not
	// have that flag at all, so a transcript without confidences is normal and
	// not a failure.
	Confidence float64 `json:"confidence,omitempty"`
	// ConfidenceKnown separates "the model was unsure" from "nobody asked".
	// Without it a missing confidence reads as 0.0, which is the strongest
	// possible claim of garbage about a segment that may be perfect.
	ConfidenceKnown bool `json:"confidenceKnown,omitempty"`
}

// Duration is how long the utterance lasted.
func (s Segment) Duration() time.Duration {
	if s.EndMS <= s.StartMS {
		return 0
	}
	return time.Duration(s.EndMS-s.StartMS) * time.Millisecond
}

// TrackTranscript is everything one audio track said.
type TrackTranscript struct {
	Track   int    `json:"track"`
	Speaker string `json:"speaker,omitempty"`
	// Role is the routing role this track was annotated with, kept as a plain
	// string so a stored transcript does not go stale when the role vocabulary
	// grows.
	Role string `json:"role,omitempty"`
	// Language is what whisper decided, or what the operator forced.
	Language string    `json:"language,omitempty"`
	Model    string    `json:"model,omitempty"`
	Segments []Segment `json:"segments"`
}

// Text is the whole track as running prose, one utterance per line.
func (t TrackTranscript) Text() string {
	var b strings.Builder
	for _, s := range t.Segments {
		if s.Text == "" {
			continue
		}
		b.WriteString(s.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Transcript is the whole job's output: one entry per track transcribed.
type Transcript struct {
	// Recording is the segment filename this describes, e.g. rec-20240115-143000.mkv.
	Recording   string            `json:"recording"`
	RecordingID int64             `json:"recordingId,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	Model       string            `json:"model,omitempty"`
	Backend     Backend           `json:"backend,omitempty"`
	Tracks      []TrackTranscript `json:"tracks"`
}

// Merged is every track's segments in one time-ordered slice.
//
// This is the "free diarization" view: because each speaker was recorded on a
// separate track, interleaving the tracks by timestamp produces an attributed
// conversation with no diarization model in the pipeline at all.
//
// Ties break on track index so the ordering is stable — two people starting to
// talk in the same millisecond must not reorder between runs, or every diff of
// a re-run transcript is noise.
func (t Transcript) Merged() []Segment {
	var out []Segment
	for _, tr := range t.Tracks {
		out = append(out, tr.Segments...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		return out[i].EndMS < out[j].EndMS
	})
	return out
}

// Speakers lists the distinct speakers in track order. It is what a UI puts in
// a filter control.
func (t Transcript) Speakers() []string {
	seen := map[string]bool{}
	var out []string
	for _, tr := range t.Tracks {
		if tr.Speaker == "" || seen[tr.Speaker] {
			continue
		}
		seen[tr.Speaker] = true
		out = append(out, tr.Speaker)
	}
	return out
}

// SegmentCount totals the utterances across every track.
func (t Transcript) SegmentCount() int {
	n := 0
	for _, tr := range t.Tracks {
		n += len(tr.Segments)
	}
	return n
}

// NormalizeSegments fixes up what a decoder handed us: drops empty text, orders
// by start time, clamps negative timestamps to zero and repairs an end that
// precedes its start.
//
// Whisper does emit zero-length and out-of-order segments at the seams between
// its 30-second windows, and a subtitle file with a cue that ends before it
// begins is rejected outright by some players rather than being ignored.
func NormalizeSegments(segs []Segment) []Segment {
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text == "" {
			continue
		}
		if s.StartMS < 0 {
			s.StartMS = 0
		}
		if s.EndMS < s.StartMS {
			s.EndMS = s.StartMS
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMS < out[j].StartMS })
	return out
}

// SpeakerLabel is the human name for one track.
//
// Operator label first, then role, then the track number. This is the reverse
// of recording.stemBase's precedence and deliberately so: a stem is a FILE that
// a post-production script globs for, so its closed-vocabulary role has to win,
// whereas a speaker label is read by a person and "Guest mic (Zoom)" tells them
// more than "mic" ever will.
func SpeakerLabel(label, role string, track int) string {
	if s := strings.TrimSpace(label); s != "" {
		return s
	}
	if s := strings.TrimSpace(role); s != "" {
		return fmt.Sprintf("%s %d", titleWord(s), track+1)
	}
	return fmt.Sprintf("Track %d", track+1)
}

// UniqueSpeakers disambiguates labels that collide.
//
// Two tracks both labelled "Mic" produce a transcript where the whole point of
// the feature — knowing who said what — is silently lost. Suffixing with the
// track number rather than a running counter keeps the label tied to the "Track
// N" the operator sees in the UI.
func UniqueSpeakers(labels map[int]string) map[int]string {
	counts := map[string]int{}
	for _, l := range labels {
		counts[l]++
	}
	out := make(map[int]string, len(labels))
	used := map[string]bool{}
	tracks := make([]int, 0, len(labels))
	for t := range labels {
		tracks = append(tracks, t)
	}
	sort.Ints(tracks)
	for _, t := range tracks {
		l := labels[t]
		if counts[l] > 1 || used[l] {
			l = fmt.Sprintf("%s (track %d)", l, t+1)
		}
		for n := 2; used[l]; n++ {
			l = fmt.Sprintf("%s %d", labels[t], n)
		}
		used[l] = true
		out[t] = l
	}
	return out
}

func titleWord(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
