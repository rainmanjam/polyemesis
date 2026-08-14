package routing

import (
	"fmt"
	"strings"
)

// SecondaryPrefix is the label namespace of the second mix in a paired graph.
//
// It is spelled for a reader of `ffmpeg -h`, not for brevity: an operator
// staring at a filter_complex in the UI should be able to tell which half is
// which without knowing this package exists. Every label in the second half
// carries it -- vod_a_t0, vod_a_mix, vod_aout -- and no label in the first half
// can, because the first half is compiled in the EMPTY namespace and every label
// it emits begins with "a".
const SecondaryPrefix = "vod_"

// Pair is two finished mixes sharing ONE filter graph, for a destination that
// carries a second audio track.
//
// WHY THIS TYPE EXISTS. ffmpeg.DestSpec.SecondAudioOutLabel has been able to map
// and encode a second mix since #331, and it was measured arriving as two
// distinct tracks through this project's own RTMP ingest. Nothing could ask for
// one, because Compile emitted a single mix whose internal labels were fixed
// constants -- a_t0, a_mix, aout -- so concatenating two compiled graphs
// collided on every one of them. Namespacing those labels is the whole of the
// fix, and this type is how a caller asks for the result.
//
// TWO INPUT TAPS OF THE SAME INGEST TRACK ARE FINE, which is the fact that
// decided the shape of this. Both halves emit [0:a:N] for any track they share,
// and a filter output pad feeds exactly one input pad -- so the obvious reading
// is that a shared tap needs an explicit asplit, the way duckGraph splits a
// trigger that also has to reach the mix. It does not: an INPUT STREAM is not a
// filter pad, and FFmpeg inserts the split itself. Measured on FFmpeg 6.0.1
// (Alpine 3.18 -- the 6.0 floor internal/ffmpeg/detect.go enforces) and on 8.1.2
// (Homebrew), both of which built the two-tap graph and produced two mixes whose
// tone content differed. An asplit here would have been dead weight carried on a
// guess; see TestAPairedGraphReachesFFmpegAsTwoDistinctMixes, which builds the
// real graph with the real binary and reads the tones back rather than counting
// tracks.
type Pair struct {
	// Result is the PRIMARY mix -- its OutLabel, Tracks, Summary, Normalization
	// and VideoDelayMS all describe the first track, exactly as a plain Compile
	// would -- with TWO exceptions: FilterComplex carries BOTH halves, because
	// that is the single string FFmpeg is handed, and SecondOutLabel names the
	// second mix. Callers that already know what to do with a Result therefore
	// need to learn one new field, not a new type -- which is what lets the
	// engine carry a VOD track without changing a single signature.
	Result

	// Second describes the second mix on its own terms -- which tracks reached
	// it, what it was normalized to, what it warns about. Nil when there is no
	// second mix, which includes the case where one was ASKED for and could not
	// be built; see CompilePair for why that is a warning and not an error.
	Second *Result
}

// CompilePair compiles a primary mix and an optional secondary mix into one
// filter graph.
//
// The primary is compiled in the empty namespace, so a destination that gains a
// second track does not have its first track silently rewritten: the primary
// half of the returned FilterComplex is byte for byte what Compile(primary, src)
// returns on its own. That is asserted by
// TestTheEmptyNamespaceIsByteIdenticalToTheSingleMixGraph, because "the live mix
// is unchanged" is the promise an operator is actually relying on when they tick
// a VOD box, and a promise nothing checks is a promise that decays.
//
// A NIL SECONDARY IS THE ORDINARY CASE and returns a Pair with SecondOutLabel ""
// and Second nil -- i.e. a Result, wearing a different hat. Callers do not need
// to branch before calling.
//
// A SECONDARY THAT WILL NOT COMPILE IS A WARNING, NOT AN ERROR. This is the
// owner's standing decision that an optional VOD track must never veto a working
// broadcast, applied at the earliest point it can be: if the secondary profile
// selects nothing this ingest carries, or every track it selects is excluded by
// a role policy, the operator gets their live stream plus a sentence saying why
// there is no VOD track. The alternative -- returning an error -- would take a
// destination that was publishing fine yesterday off the air because an OPTIONAL
// extra could not be built, which is precisely backwards.
//
// A PRIMARY THAT WILL NOT COMPILE IS STILL AN ERROR. There is no stream without
// it, and pretending otherwise would publish silence.
func CompilePair(primary Profile, secondary *Profile, src Source) (Pair, error) {
	first, err := compile(primary, src, false, ns{})
	if err != nil {
		return Pair{}, err
	}

	p := Pair{Result: first}
	if secondary == nil {
		return p, nil
	}

	second, serr := compile(*secondary, src, false, ns{prefix: SecondaryPrefix})
	if serr != nil {
		// Name the destination-level consequence, not the Go error. "no audio"
		// on its own reads as though the whole destination is silent, which is
		// the opposite of what has happened.
		p.Warnings = dedupe(append(p.Warnings, fmt.Sprintf(
			"the second (VOD) audio track could not be built and is not being sent, so this destination is publishing its live mix only: %v", serr)))
		return p, nil
	}

	// Prefix the second mix's own warnings. Unprefixed they are indistinguishable
	// from the live mix's -- "track 4 is not present on the ingest" is a
	// different problem depending on which mix dropped it, and an operator
	// reading a destination card cannot tell them apart otherwise.
	for _, w := range second.Warnings {
		p.Warnings = append(p.Warnings, "second (VOD) audio track: "+w)
	}
	p.Warnings = dedupe(p.Warnings)

	p.FilterComplex = first.FilterComplex + ";" + second.FilterComplex
	p.SecondOutLabel = second.OutLabel
	p.Second = &second
	return p, nil
}

// SecondaryLabels reports every label the secondary namespace claims in g, in
// the order they are defined. It exists for the collision test and for anyone
// debugging a graph by eye; nothing in the compile path calls it.
func SecondaryLabels(g string) []string {
	var out []string
	for _, f := range strings.Split(g, ";") {
		for {
			i := strings.Index(f, "["+SecondaryPrefix)
			if i < 0 {
				break
			}
			j := strings.Index(f[i:], "]")
			if j < 0 {
				break
			}
			out = append(out, f[i+1:i+j])
			f = f[i+j:]
		}
	}
	return dedupe(out)
}
