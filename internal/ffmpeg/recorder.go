package ffmpeg

import (
	"strconv"
)

// ---------------------------------------------------------------------- stems
//
// Stem recording writes every ingest audio track to its OWN file beside the
// multitrack master, which is what turns polyemesis from a restreamer that
// happens to archive into a multitrack field recorder that happens to stream.
// A podcaster ends the session with mic.flac, music.flac and game.flac ready to
// drop onto three DAW tracks, without a second capture device and without
// asking the remote guest to record locally and remember to send the file.
//
// ONE PROCESS, N OUTPUTS — not N processes. Three reasons, in order of how much
// they matter:
//
//  1. SAMPLE ALIGNMENT. Every stem is demuxed from one input by one process, so
//     they share a decode clock and a segment boundary. Import them into a DAW
//     and they line up to the sample with no nudging. N processes each dial
//     their own relay subscription, each connect at a slightly different
//     instant, and each drop a different number of leading packets — the stems
//     would arrive milliseconds apart, which is a phase-cancelling comb filter
//     the moment anyone sums two of them.
//  2. COST. One decode of the ingest feeds every stem encoder. N processes pay
//     N demuxes, N decodes and N relay subscriptions to produce the same bytes.
//  3. LIFETIME. One process starts, stops and restarts as a unit, so the stems
//     and the master always cover the same span. There is no state in which
//     four of seven stem processes survived a hiccup.
//
// The price of one process is that a fatal output kills the archive too, so
// everything below leans toward dropping a stem rather than risking the exit:
// see the FLAC channel guard and the -vn on every stem output.
//
// This file never edits the master output. StemRecorderArgs builds it by
// calling RecorderArgs, so a recording made with stems on is byte-identical to
// one made with stems off.

// StemCodec selects what each stem is written as.
type StemCodec string

const (
	// StemFLAC is the default: lossless, and roughly half the size of the same
	// audio as WAV. A six-hour six-track session is the difference between
	// filling a disk and not.
	StemFLAC StemCodec = "flac"
	// StemWAV is for the tool that still cannot open a FLAC, and for anyone who
	// needs every segment's header to state its own length — see the STREAMINFO
	// note below. It is lossless too, just larger; nothing about the audio
	// changes.
	StemWAV StemCodec = "wav"
)

// A raw-FLAC segment does not carry its own length. The encoder only learns its
// total sample count at flush, by which time the segment muxer has closed every
// earlier file. Verified against ffmpeg 8.1 over a segmented capture:
// intermediate segments report an unknown duration — legal FLAC, and every
// decoder in the chain handles it — while the final partial segment inherits
// the whole run's sample count and so overstates its own length in the header.
//
// No audio is affected: each file decodes to exactly the samples that belong to
// it. But a tool that trusts the header instead of decoding will mis-report the
// last file, which is the entire reason StemWAV is offered as an alternative
// rather than FLAC being the only choice.

// DefaultStemCodec is what an unset codec means everywhere in this package.
const DefaultStemCodec = StemFLAC

// MaxFLACStemChannels is the FLAC bitstream's own ceiling. Verified against
// ffmpeg 8.1: the encoder refuses to open with "12 channels not supported
// (max 8)", which in a one-process design would take the master recording down
// with the stem. Exported so the caller choosing filenames and the builder
// choosing encoders cannot disagree about where the cliff is.
const MaxFLACStemChannels = 8

// stemFLACBits pins FLAC to 24-bit.
//
// Left to negotiate, FFmpeg picks s32 for a float decode and the encoder then
// clamps to 24 anyway — but logs a warning about it for every stem, every
// start, at the loglevel the whole app runs at. Saying 24 out loud produces the
// identical file and a quiet log.
const stemFLACBits = "24"

// StemCodecs is the catalogue the UI offers, in the order it should show them.
func StemCodecs() []StemCodec { return []StemCodec{StemFLAC, StemWAV} }

// ValidStemCodec reports whether c is a codec this build understands. The empty
// string counts: it means DefaultStemCodec, so a payload with no opinion about
// stems is not an error.
func ValidStemCodec(c StemCodec) bool {
	switch c {
	case "", StemFLAC, StemWAV:
		return true
	}
	return false
}

// Ext is the file extension for a stem in this codec, leading dot included.
func (c StemCodec) Ext() string {
	if c == StemWAV {
		return ".wav"
	}
	return ".flac"
}

// StemSpec is one ingest audio track routed to its own file.
type StemSpec struct {
	// Track is the 0-based ingest audio track, matching routing.Track.Index.
	Track int
	// Path is the absolute strftime output pattern for this stem. The caller
	// owns naming, because the name has to agree with the file extension and
	// only the caller knows which codec it picked.
	Path string
	// Codec overrides the recorder's default for this one stem. Empty means the
	// default. Per-stem rather than per-recorder because a single track wide
	// enough to defeat FLAC must not force every other stem to WAV.
	Codec StemCodec
	// Channels is the track's width as the probe reported it. Zero means
	// unknown, which is treated as "fine" — refusing to record a stem because
	// we failed to measure it would be the restrictive-check mistake this repo
	// has already made three times.
	Channels int
}

// StemRecorderSpec is the recorder plus its per-track stem outputs.
type StemRecorderSpec struct {
	// RecorderSpec is the master archive, unchanged and unchangeable: enabling
	// stems must not alter a single byte of the MKV anyone already relies on.
	RecorderSpec
	// Codec is the default for stems that do not name their own.
	Codec StemCodec
	// Stems is the per-track outputs, written in the order given.
	Stems []StemSpec
}

// StemRecorderArgs builds the recorder command with per-track stem outputs.
//
// With no usable stems it returns exactly RecorderArgs(s.RecorderSpec), so
// turning the feature on for a video-only ingest is a no-op rather than a
// different recording.
//
// Every stem carries the master's -segment_time, so stem segment k and master
// segment k cover the same window of the source. The boundaries are not
// identical: the master is video and can only cut on a keyframe at or after the
// boundary, while an audio-only output cuts on the boundary itself, so a stem
// hands off to its successor up to one GOP before the master does — which can
// also leave the stems with one more trailing segment than the master at the
// end of a run. Nothing is lost or duplicated at a seam; the samples are simply
// in the adjacent file. What is exact is stem-to-stem: every stem cuts at the
// same instant as every other stem, which is the alignment that matters when
// six files are dragged into a DAW together.
func StemRecorderArgs(s StemRecorderSpec) []string {
	// Mirrors RecorderArgs' own default. The two have to agree: a master
	// segmented at 3600s beside stems segmented at 0 would not line up at all.
	if s.SegmentSeconds == 0 {
		s.SegmentSeconds = 3600
	}
	args := RecorderArgs(s.RecorderSpec)

	seen := make(map[string]bool, len(s.Stems))
	for _, st := range s.Stems {
		if st.Path == "" || st.Track < 0 {
			continue
		}
		// Two outputs writing one path interleave into a file that is neither.
		if seen[st.Path] {
			continue
		}
		codec := st.Codec
		if codec == "" {
			codec = s.Codec
		}
		if codec == "" {
			codec = DefaultStemCodec
		}
		// A FLAC encoder that cannot open is a fatal output, and a fatal output
		// in a shared process ends the master recording too. Losing one stem
		// beats losing the archive. Only a positively reported width triggers
		// this; Channels == 0 means "not measured" and is left alone.
		if codec == StemFLAC && st.Channels > MaxFLACStemChannels {
			continue
		}
		seen[st.Path] = true
		args = append(args, stemOutputArgs(st, codec, s.SegmentSeconds)...)
	}
	return args
}

// stemOutputArgs is one stem's output block.
//
// No -ac and no -ar anywhere: a stem is the track exactly as it arrived, at its
// own width and rate. Down-mixing or resampling here would make the archive
// worse than the live destinations it exists to outlive.
func stemOutputArgs(st StemSpec, codec StemCodec, segmentSeconds int) []string {
	args := []string{
		// Non-optional on purpose. A trailing '?' would make a missing track
		// survivable, but verified against ffmpeg 8.1 an output whose maps all
		// match nothing then falls back to DEFAULT stream selection and happily
		// picks the video stream — a "stem" that is silently a video file, which
		// is worse than a recorder that refuses to start and says why.
		"-map", "0:a:" + strconv.Itoa(st.Track),
		// And belt-and-braces even so, because a stem is never a video file no
		// matter how FFmpeg's stream selection evolves.
		"-vn",
	}
	switch codec {
	case StemWAV:
		// 24-bit PCM is the field-recorder convention and what every DAW
		// expects to be handed.
		args = append(args, "-c:a", "pcm_s24le")
	default:
		args = append(args, "-c:a", "flac", "-bits_per_raw_sample", stemFLACBits)
	}
	args = append(args,
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentSeconds),
	)
	switch codec {
	case StemWAV:
		// WAV's 32-bit size field wraps at 4 GiB, which a long segment of
		// 24-bit multichannel audio reaches. rf64=auto keeps the file a plain
		// WAV until that point and promotes it to RF64 instead of writing a
		// header that lies about its own length.
		args = append(args, "-segment_format", "wav", "-segment_format_options", "rf64=auto")
	default:
		args = append(args, "-segment_format", "flac")
	}
	args = append(args,
		// Same two flags as the master, for the same two reasons: each segment
		// plays standalone, and its filename carries its own wall-clock start.
		"-reset_timestamps", "1",
		"-strftime", "1",
		st.Path,
	)
	return args
}
