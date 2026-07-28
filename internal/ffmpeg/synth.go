package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- synthesis
//
// Two synthetic sources. Both exist because a stream that is technically
// correct is not always a stream a platform will accept or keep.
//
//   - SILENCE. A video-only ingest is rejected outright by YouTube, Twitch and
//     Facebook. Without synthesis the operator discovers that as a destination
//     which connects and dies seconds later, with a platform-side error nobody
//     can read. Synthesising a silent AAC track makes the destination valid.
//
//   - SLATE. When the ingest drops, every destination's input ends and the
//     platform sees the stream STOP rather than stall. On YouTube that can end
//     the broadcast outright, not merely pause it, and the operator cannot get
//     it back. A standby source keeps the hub fed with a colour or a still
//     image plus silence so destinations stay connected while the encoder
//     reconnects.
//
// Everything here is a pure argument builder. Nothing in this file starts a
// process or decides when a process should exist.
//
// Neither builder tags its synthesised track with a title. The MPEG-TS muxer
// has no descriptor for one and discards it without a word, so telling the
// operator WHY a video-only ingest suddenly has audio has to come from engine
// state rather than from the stream.

const (
	// 48 kHz stereo is what every destination re-samples to anyway, so
	// synthesising anything else just adds a conversion.
	defaultSynthSampleRate = 48000
	defaultSynthChannels   = 2

	// Silence compresses to almost nothing, so the bitrate here is a ceiling
	// that will never be reached rather than a cost.
	defaultSilenceKbps = 128

	// AAC tops out at 7.1. Verified against ffmpeg 8.1: anullsrc happily
	// accepts channel_layout=12c and resolves it to 7.1.4, and the aac encoder
	// then refuses to open with "Unsupported channel layout", which would
	// crash-loop the very process that exists to keep a destination alive.
	maxAACChannels = 8

	defaultSlateColor = "black"
	defaultSlateWidth = 1280
	// A slate is a static frame; 30 fps is plenty and halves the encode of 60.
	defaultSlateHeight = 720
	defaultSlateFPS    = 30
	// A flat colour or a still image needs almost no bitrate, but platforms
	// watch for a bitrate floor before they call a stream unhealthy.
	defaultSlateKbps = 2000
)

// NeedsSilence reports whether a probed ingest requires a synthesised track.
//
// It answers true only when the probe positively reported zero audio tracks. A
// nil probe answers false, and that asymmetry is deliberate: declining to
// synthesise for a stream that really is video-only costs a rejected
// destination and a legible platform error, whereas synthesising for a stream
// that does carry audio would map video only and silently drop every track the
// operator selected — the one failure this product cannot have.
func NeedsSilence(p *ProbeResult) bool {
	return p != nil && len(p.Audio) == 0
}

// SilenceSource renders the anullsrc filtergraph description.
//
// Both options are given explicitly because anullsrc's own defaults are 44100
// Hz stereo, and a 44.1 kHz track fed into a 48 kHz graph costs a resample on
// every destination for no reason.
func SilenceSource(channels, sampleRate int) string {
	if channels <= 0 {
		channels = defaultSynthChannels
	}
	if sampleRate <= 0 {
		sampleRate = defaultSynthSampleRate
	}
	return fmt.Sprintf("anullsrc=channel_layout=%s:sample_rate=%d",
		ChannelLayoutName(channels), sampleRate)
}

// SilenceInputArgs is the input fragment on its own, for callers that splice a
// silent track into a command they build themselves.
func SilenceInputArgs(channels, sampleRate int) []string {
	return []string{"-f", "lavfi", "-i", SilenceSource(channels, sampleRate)}
}

// aacChannels narrows a requested width to something the AAC encoder can
// actually open. Widening silence past 7.1 buys nothing — it is silence — so
// capping keeps the process alive where honouring the request verbatim would
// only produce an encoder that refuses to start.
func aacChannels(n int) int {
	if n <= 0 {
		return defaultSynthChannels
	}
	if n > maxAACChannels {
		return maxAACChannels
	}
	return n
}

// SilenceSpec describes the tier that gives a video-only ingest an audio track.
type SilenceSpec struct {
	// InRelayURL is the hub carrying the video-only ingest.
	InRelayURL string
	// OutRelayURL is this tier's OWN hub. Destinations subscribe to it instead
	// of the ingest's, so from routing's point of view the source simply has
	// one audio track and nothing downstream needs to know it is synthetic.
	OutRelayURL string

	Channels   int // 0 => stereo
	SampleRate int // 0 => 48000
	Kbps       int // 0 => 128
}

// SilenceArgs builds the silence tier's command.
//
// Video is `-c:v copy`, exactly as everywhere else — this tier must not become
// a second place video is degraded. Audio comes only from input 1: input 0's
// audio is deliberately NOT mapped, because this tier only ever runs against an
// ingest that probed with zero audio tracks, and mapping 0:a here would mean
// re-encoding real operator audio through a path that has no routing graph.
func SilenceArgs(s SilenceSpec) []string {
	s.Channels = aacChannels(s.Channels)
	if s.SampleRate <= 0 {
		s.SampleRate = defaultSynthSampleRate
	}
	if s.Kbps <= 0 {
		s.Kbps = defaultSilenceKbps
	}

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", RelayInputURL(s.InRelayURL),
	)
	args = append(args, SilenceInputArgs(s.Channels, s.SampleRate)...)
	args = append(args,
		// No '?' on the video map. This tier is only started for an ingest that
		// probed as video-with-no-audio, so a missing video track is a real
		// fault and belongs in this process's log, not hidden behind an
		// audio-only hub that breaks every destination further downstream.
		"-map", "0:v:0",
		"-c:v", "copy",
		"-map", "1:a:0",
		"-c:a", "aac",
		"-b:a", strconv.Itoa(s.Kbps)+"k",
		"-ac", strconv.Itoa(s.Channels),
		"-ar", strconv.Itoa(s.SampleRate),
		// Load-bearing. anullsrc never ends, so without -shortest this process
		// would keep publishing silence into the hub long after the ingest went
		// away, and the supervisor would never see it exit and restart it.
		"-shortest",
		"-f", "mpegts",
		"-flush_packets", "1",
		RelayOutputURL(s.OutRelayURL),
	)
	return args
}

// -------------------------------------------------------------------- slate

// SlateSpec describes the standby source shown while the ingest is away.
type SlateSpec struct {
	// OutRelayURL is the hub the slate publishes into — normally the same hub
	// the destinations already subscribe to, so the takeover is invisible to
	// them and no destination process restarts.
	OutRelayURL string

	// ImagePath, when set, loops a still image; otherwise Color paints a flat
	// frame. The colour path is the fallback precisely because it has no file
	// to fail to open, and a standby source that cannot start is worse than an
	// ugly one.
	ImagePath string
	// Color is any spelling FFmpeg's colour parser accepts ("black", "0x101014",
	// "gray@0.9"). Empty means black.
	Color string

	// Width, Height and FPS should match the departed ingest so the platform
	// sees a continuous stream rather than a format change mid-broadcast.
	Width  int
	Height int
	FPS    float64

	VideoKbps   int
	MaxrateKbps int // 0 => VideoKbps
	BufsizeKbps int // 0 => 2x maxrate

	// Encoder defaults to libx264 rather than to whatever hardware this box
	// has. A slate is a static frame that costs a software encoder nothing, and
	// the one job it has is to start when everything else has already failed.
	Encoder     string
	Preset      string
	VAAPIDevice string

	GOPSeconds float64

	AudioChannels int // 0 => stereo
	SampleRate    int // 0 => 48000
	AudioKbps     int // 0 => 128

	// TimestampOffsetSeconds shifts the slate's output timestamps forward. The
	// slate's synthetic inputs start at zero while the destinations have been
	// running on the ingest's clock; without an offset the muxed timeline jumps
	// backwards and platforms drop the connection outright.
	TimestampOffsetSeconds float64
}

// SlateArgs builds the standby source's command.
//
// The output is a normal MPEG-TS hub feed with one video track and one silent
// audio track, so a destination cannot tell a slate from an ingest.
func SlateArgs(s SlateSpec) []string {
	if s.Color == "" {
		s.Color = defaultSlateColor
	}
	if s.Width <= 0 {
		s.Width = defaultSlateWidth
	}
	if s.Height <= 0 {
		s.Height = defaultSlateHeight
	}
	if s.FPS <= 0 {
		s.FPS = defaultSlateFPS
	}
	if s.VideoKbps <= 0 {
		s.VideoKbps = defaultSlateKbps
	}
	if s.MaxrateKbps <= 0 {
		s.MaxrateKbps = s.VideoKbps
	}
	if s.BufsizeKbps <= 0 {
		s.BufsizeKbps = s.MaxrateKbps * 2
	}
	if s.GOPSeconds <= 0 {
		s.GOPSeconds = 2
	}
	s.AudioChannels = aacChannels(s.AudioChannels)
	if s.SampleRate <= 0 {
		s.SampleRate = defaultSynthSampleRate
	}
	if s.AudioKbps <= 0 {
		s.AudioKbps = defaultSilenceKbps
	}
	if s.Encoder == "" {
		s.Encoder = EncoderX264
	}
	// Same rule as renditions: an encoder we hold no profile for still runs,
	// with only the flags every encoder understands. Refusing to build a
	// command because the encoder is unfamiliar would leave the operator with
	// no standby source at all, which is the failure this feature exists to
	// prevent.
	prof, known := encoderProfiles[s.Encoder]
	if !known {
		prof.presetFlag = "-preset"
	}

	args := commonArgs()
	args = append(args, progressArgs()...)

	if prof.vaapi {
		dev := s.VAAPIDevice
		if dev == "" {
			dev = defaultVAAPIDevice
		}
		// Must precede the inputs: the device has to exist before the filter
		// graph that uploads into it is configured.
		args = append(args, "-vaapi_device", dev)
	}

	args = append(args, slateVideoInput(s)...)
	// -re on the audio too. Both lavfi sources generate as fast as they are
	// read, and with threaded demuxing an unpaced one races ahead of the paced
	// one and fills the interleaving queue with hours of silence.
	args = append(args, "-re")
	args = append(args, SilenceInputArgs(s.AudioChannels, s.SampleRate)...)

	args = append(args,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-c:v", s.Encoder,
	)
	args = append(args, presetArgs(prof, s.Preset)...)
	args = append(args, prof.rateControl...)
	args = append(args,
		"-b:v", strconv.Itoa(s.VideoKbps)+"k",
		"-maxrate", strconv.Itoa(s.MaxrateKbps)+"k",
		"-bufsize", strconv.Itoa(s.BufsizeKbps)+"k",
	)

	if vf := slateVideoFilter(s, prof); vf != "" {
		args = append(args, "-vf", vf)
	}
	// Pinned on the output as well as the input, so the muxed rate matches the
	// ingest the platform was already receiving regardless of which of the two
	// source paths produced the frames.
	args = append(args, "-r", formatFPS(s.FPS))

	// A static frame produces no scene cuts at all, so without a forced
	// interval x264 would emit one keyframe every 250 frames and a viewer
	// joining mid-slate would wait seconds for a picture.
	gop := gopFrames(RenditionSpec{FPS: s.FPS, GOPSeconds: s.GOPSeconds})
	args = append(args,
		"-g", strconv.Itoa(gop),
		"-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", strconv.Itoa(s.AudioKbps)+"k",
		"-ac", strconv.Itoa(s.AudioChannels),
		"-ar", strconv.Itoa(s.SampleRate),
	)

	if s.TimestampOffsetSeconds != 0 {
		args = append(args, "-output_ts_offset", formatSeconds(s.TimestampOffsetSeconds))
	}

	args = append(args,
		"-f", "mpegts",
		"-flush_packets", "1",
		RelayOutputURL(s.OutRelayURL),
	)
	return args
}

// slateVideoInput renders input 0: either a looped still or a flat colour.
func slateVideoInput(s SlateSpec) []string {
	if s.ImagePath != "" {
		return []string{
			// Without -loop the image demuxer yields exactly one frame and the
			// standby source ends the moment it starts.
			"-loop", "1",
			// The demuxer's own rate. -re paces against the input rate, so
			// leaving this at the image2 default of 25 would pace the slate at
			// 25 fps no matter what -r says on the output.
			"-framerate", formatFPS(s.FPS),
			"-re",
			// A plain path, never a movie= filter argument: paths routinely
			// contain the characters a filtergraph would treat as separators.
			"-i", s.ImagePath,
		}
	}
	return []string{
		// lavfi sources generate frames as fast as the encoder will take them.
		// Without -re the slate would run at hundreds of times realtime and
		// bury the hub within seconds.
		"-re",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=%s",
			escapeLavfiValue(s.Color), s.Width, s.Height, formatFPS(s.FPS)),
	}
}

// slateVideoFilter fits the source to the target frame, or returns "" when
// there is nothing to do.
func slateVideoFilter(s SlateSpec, prof encoderProfile) string {
	var chain []string
	if s.ImagePath != "" {
		// A branded slate is whatever size the operator's designer made it.
		// force_original_aspect_ratio=decrease then pad letterboxes it into the
		// ingest's frame rather than stretching it, and setsar=1 stops a
		// non-square source SAR from reaching the encoder as a stretched frame.
		chain = append(chain,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", s.Width, s.Height),
			fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s",
				s.Width, s.Height, escapeLavfiValue(s.Color)),
			"setsar=1",
		)
	}
	// The colour source already emits exactly the requested size and rate, so
	// it needs no chain at all — unless VAAPI, which encodes from GPU surfaces
	// and must have its frames converted and uploaded first.
	if prof.vaapi {
		chain = append(chain, "format=nv12", "hwupload")
	}
	return strings.Join(chain, ",")
}

// escapeLavfiValue protects an option value inside a filtergraph description.
// A colour like "0x101014" is harmless, but any user-supplied string with a
// colon in it would otherwise be read as the start of the next option and
// produce a parse error the operator cannot connect to what they typed.
func escapeLavfiValue(v string) string {
	return lavfiEscaper.Replace(v)
}

var lavfiEscaper = strings.NewReplacer(
	`\`, `\\`,
	`:`, `\:`,
	`,`, `\,`,
	`'`, `\'`,
	`[`, `\[`,
	`]`, `\]`,
	`;`, `\;`,
)

// formatSeconds renders a time without an exponent or trailing zeros, because
// FFmpeg's duration parser rejects Go's default "1e+06" formatting.
func formatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
