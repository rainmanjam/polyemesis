package playout

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// File and directory names the packager writes and the handler serves. They are
// constants because three places have to agree on them: the muxer's command
// line, the master playlist the manager writes, and the sweeper that decides
// which files on disk are disposable.
const (
	// MasterPlaylist is the cross-variant playlist at the playout root. It is
	// written by the manager rather than by FFmpeg, because a variant riding a
	// different rendition is a different process and no single muxer can see
	// them all.
	MasterPlaylist = "master.m3u8"
	// MediaPlaylist and DASHManifest live inside each variant's directory.
	MediaPlaylist = "index.m3u8"
	DASHManifest  = "manifest.mpd"

	hlsSegmentPattern  = "seg_%05d.ts"
	dashInitPattern    = "init-$RepresentationID$.m4s"
	dashSegmentPattern = "chunk-$RepresentationID$-$Number%05d$.m4s"
)

// VariantSpec describes one publicly served rung of the ladder.
//
// The load-bearing line in the arguments below is `-c:v copy`. A variant does
// not encode video; it packages video some rendition already encoded, which is
// why a five-rung ladder costs one encoder per distinct rendition rather than
// five encoders for playout. If this ever gains a video encoder, the rendition
// tier has been duplicated and the shared-encode accounting is wrong.
type VariantSpec struct {
	// Name is the directory and URL path segment the variant is served under.
	Name string
	// RelayURL is the variant's own subscription: the ingest hub for a
	// passthrough variant, its rendition's hub otherwise. Its own, so a muxer
	// that falls over cannot disturb a destination.
	RelayURL string
	// Dir is the variant's directory, normally <playout root>/<Name>.
	Dir string

	SegmentSeconds int
	// PlaylistSegments is the live window. When DVRSegments is larger it wins:
	// see playlistLength.
	PlaylistSegments int
	// DVRSegments is how many segments the rolling seekable window holds, 0 for
	// live only.
	DVRSegments int

	// AudioTrack is the ingest track this variant publishes.
	AudioTrack int
	AudioKbps  int

	// DASH additionally writes a DASH manifest with fMP4 segments, muxed by the
	// same process from the same copied video.
	DASH bool
}

// playoutDefaults fills in anything the caller left at zero, so a spec built by
// hand in a test still produces a runnable command line.
func (s *VariantSpec) applyDefaults() {
	if s.SegmentSeconds <= 0 {
		s.SegmentSeconds = 4
	}
	if s.PlaylistSegments <= 0 {
		s.PlaylistSegments = 6
	}
	if s.AudioKbps <= 0 {
		s.AudioKbps = 128
	}
}

// playlistLength is how many segments the published playlist carries.
//
// DVR beyond the playlist would be pointless: a segment that is on disk but
// absent from the playlist has no URL any player will ever ask for, so the DVR
// window IS the playlist window whenever it is the larger of the two.
func (s VariantSpec) playlistLength() int {
	if s.DVRSegments > s.PlaylistSegments {
		return s.DVRSegments
	}
	return s.PlaylistSegments
}

// commonArgs and progressArgs mirror internal/ffmpeg's unexported helpers of
// the same name. Duplicated rather than reached for because playout must not
// widen that package's API for the sake of four flags; the values are the ones
// every polyemesis child has always been given.
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "warning"}
}

func progressArgs() []string {
	return []string{"-nostats", "-progress", "pipe:1"}
}

// VariantArgs builds the packaging command for one variant.
//
// Audio is encoded, video is not. That asymmetry is deliberate: a viewer's
// player wants exactly one stereo AAC track, while the ingest may carry six
// tracks in a layout only the destinations care about. Encoding one 128 kbps
// stereo track per variant is cheap and, crucially, happens entirely off the
// destination path — playout has its own relay subscription and cannot alter
// what any destination receives.
func VariantArgs(s VariantSpec) []string {
	s.applyDefaults()

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", ffmpeg.RelayInputURL(s.RelayURL),
	)
	args = append(args, hlsOutput(s)...)
	if s.DASH {
		args = append(args, dashOutput(s)...)
	}
	return args
}

// mapArgs selects the streams every output of this variant carries.
//
// The '?' on the audio map is what lets a video-only ingest still play instead
// of failing the whole variant at start — the same fail-open choice the preview
// makes, and the right one: a silent stream is a diagnosable problem, a stream
// that never starts is not.
func mapArgs(s VariantSpec) []string {
	return []string{
		"-map", "0:v:0",
		"-map", fmt.Sprintf("0:a:%d?", s.AudioTrack),
		"-c:v", "copy",
		"-c:a", "aac",
		"-b:a", strconv.Itoa(s.AudioKbps) + "k",
		"-ac", "2",
	}
}

func hlsOutput(s VariantSpec) []string {
	args := mapArgs(s)
	return append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(s.SegmentSeconds),
		"-hls_list_size", strconv.Itoa(s.playlistLength()),
		// delete_segments is the muxer pruning its own window; without it a DVR
		// stream grows without bound. omit_endlist keeps the playlist live so a
		// player does not treat a momentary muxer restart as end-of-stream.
		// program_date_time is what makes a DVR window seekable to a wall-clock
		// time rather than only to an offset.
		"-hls_flags", "delete_segments+independent_segments+omit_endlist+program_date_time",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(s.Dir, hlsSegmentPattern),
		filepath.Join(s.Dir, MediaPlaylist),
	)
}

// dashOutput is a second muxer on the same process, fed by the same copied
// video. It re-encodes the audio a second time, which costs a fraction of a
// core and buys a DASH manifest without a second subscription, a second
// process, or — the thing that would matter — a second video encode.
func dashOutput(s VariantSpec) []string {
	args := mapArgs(s)
	return append(args,
		"-f", "dash",
		"-seg_duration", strconv.Itoa(s.SegmentSeconds),
		"-window_size", strconv.Itoa(s.playlistLength()),
		// Two segments of grace past the window. A player that fetched the
		// manifest a moment before a segment rolled out would otherwise 404 on
		// a segment the manifest it holds still lists.
		"-extra_window_size", "2",
		"-use_template", "1",
		"-use_timeline", "1",
		// Without explicit sets the muxer puts video and audio in one
		// AdaptationSet, which is invalid for a player that wants to switch
		// them independently.
		"-adaptation_sets", "id=0,streams=v id=1,streams=a",
		"-init_seg_name", dashInitPattern,
		"-media_seg_name", dashSegmentPattern,
		filepath.Join(s.Dir, DASHManifest),
	)
}

// segmentsFor converts a DVR window in seconds into a segment count, rounding
// up so the window is never shorter than asked for.
func segmentsFor(windowSeconds, segmentSeconds int) int {
	if windowSeconds <= 0 || segmentSeconds <= 0 {
		return 0
	}
	n := (windowSeconds + segmentSeconds - 1) / segmentSeconds
	if n < 1 {
		n = 1
	}
	return n
}
