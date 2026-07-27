package transcribe

import (
	"sort"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Track selection — the part of this package that makes the transcripts good.
//
// Transcribing the full mix is the obvious thing to do and it is the wrong
// thing to do. Music under a voice, a second speaker talking over the first,
// and game audio all degrade whisper measurably, and the mix throws away the
// one piece of information the multitrack archive has and nothing else does:
// which microphone said it. So the default is per-track, on the tracks that
// carry speech, and the mix is never chosen automatically.

// TrackChoice is one track selected for transcription.
type TrackChoice struct {
	// Track is the 0-based audio track index within the recording.
	Track int `json:"track"`
	// Speaker is the label every segment from this track will carry.
	Speaker string `json:"speaker"`
	// Role is the routing role it was annotated with, empty when unannotated.
	Role routing.TrackRole `json:"role,omitempty"`
	// Language is the operator's language for this track, which becomes
	// whisper's -l. Empty means detect.
	Language string `json:"language,omitempty"`
	// Denoise carries the source's own noise-suppression flag through to the
	// extraction, because a noisy room is noisy for the transcript too.
	Denoise bool `json:"denoise,omitempty"`
}

// DefaultTracks picks the tracks to transcribe when the operator has not said.
//
// The order of preference:
//
//  1. Tracks roled "mic". This is the case the feature was built for and the
//     one that gives speaker attribution for free.
//  2. Tracks roled "commentary", which is speech by another name.
//  3. Every track that is NOT roled music or game. A room that annotated its
//     music but not its mics has still told us where the speech is not.
//  4. Every track, when there are no annotations at all. With no information,
//     transcribing each track separately is never worse than transcribing the
//     mix and is usually much better — and it is the only option that still
//     produces per-speaker output. The cost lands on a queued, niced,
//     yields-to-the-stream job, which is precisely what that machinery is for.
//
// It never returns the "clean" track, which is by definition the mix.
func DefaultTracks(src routing.Source) []int {
	if mics := src.TracksWithRole(routing.RoleMic); len(mics) > 0 {
		return mics
	}
	if com := src.TracksWithRole(routing.RoleCommentary); len(com) > 0 {
		return com
	}

	all := trackIndices(src)
	if len(src.Annotations) == 0 {
		return all
	}
	var out []int
	for _, i := range all {
		switch src.RoleOf(i) {
		case routing.RoleMusic, routing.RoleGame, routing.RoleClean:
			continue
		}
		out = append(out, i)
	}
	if len(out) == 0 {
		// Every track was annotated as something we would rather not transcribe.
		// Falling back to all of them beats returning nothing: the operator asked
		// for a transcript, and a mediocre transcript of the music track is a
		// visible result they can correct, whereas an empty job with no
		// explanation is not.
		return all
	}
	return out
}

// PlanTracks resolves the requested tracks into a concrete plan.
//
// want may be empty, in which case DefaultTracks decides. Requested tracks that
// the recording does not contain are dropped rather than failing the plan: an
// operator re-running a saved selection against a session that came back with
// one fewer microphone should get the tracks that are there, not an error.
func PlanTracks(src routing.Source, want []int) []TrackChoice {
	present := map[int]bool{}
	for _, t := range src.Tracks {
		present[t.Index] = true
	}
	// No probe result at all means we cannot contradict the request. Honour it
	// verbatim; FFmpeg will say so if a track really is missing.
	unknown := len(src.Tracks) == 0

	if len(want) == 0 {
		want = DefaultTracks(src)
	}

	seen := map[int]bool{}
	var chosen []int
	for _, t := range want {
		if t < 0 || seen[t] || (!unknown && !present[t]) {
			continue
		}
		seen[t] = true
		chosen = append(chosen, t)
	}
	sort.Ints(chosen)

	labels := make(map[int]string, len(chosen))
	for _, t := range chosen {
		labels[t] = SpeakerLabel(src.LabelOf(t), string(src.RoleOf(t)), t)
	}
	labels = UniqueSpeakers(labels)

	out := make([]TrackChoice, 0, len(chosen))
	for _, t := range chosen {
		out = append(out, TrackChoice{
			Track:    t,
			Speaker:  labels[t],
			Role:     src.RoleOf(t),
			Language: src.LanguageOf(t),
			Denoise:  src.DenoiseTrack(t),
		})
	}
	return out
}

// SourceFromProbe builds a routing.Source from an ffprobe of a recording, so a
// recording on disk can be planned against the same way a live ingest is.
//
// The annotations are the operator's and do not come from the file, so they are
// layered on by the caller with routing.Source.WithAnnotations — exactly as the
// engine does for a live ingest.
func SourceFromProbe(p *ffmpeg.ProbeResult) routing.Source {
	var src routing.Source
	if p == nil {
		return src
	}
	for _, a := range p.Audio {
		src.Tracks = append(src.Tracks, routing.Track{
			Index:    a.Index,
			Codec:    a.Codec,
			Channels: a.Channels,
			Layout:   a.Layout,
			Language: a.Language,
			Title:    a.Title,
		})
	}
	return src
}

func trackIndices(src routing.Source) []int {
	var out []int
	for _, t := range src.Tracks {
		if t.Index < 0 {
			continue
		}
		out = append(out, t.Index)
	}
	sort.Ints(out)
	return out
}
