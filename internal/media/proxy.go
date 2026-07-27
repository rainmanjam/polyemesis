package media

import (
	"strconv"
)

// ------------------------------------------------------------------- proxies
//
// Browsers cannot reliably play multitrack Matroska. Chrome and Safari will not
// open the container at all, and the ones that do pick a single audio track by
// rules nobody controls. So the recording that is the entire point of this
// product is the one file a user cannot scrub in the timeline they are standing
// in front of. The proxy fixes that: one small H.264/AAC MP4 beside the master
// whose only job is to make the scrub bar work.
//
// It is NOT a viewing copy and must not become one. Every knob below leans
// toward small, because the proxy is generated for every segment of every
// recording and a library that doubles its own disk usage to draw a timeline
// has made the wrong trade.

// Proxy defaults.
const (
	// DefaultProxyHeight is 360p. Big enough to recognise a face and read a
	// slide title, which is what "find the bit where..." actually needs.
	DefaultProxyHeight = 360
	// DefaultProxyCRF is well past what anyone would accept for viewing, and
	// exactly right for navigation. Roughly 250-500 kbit/s at 360p.
	DefaultProxyCRF = 28
	MinProxyCRF     = 0
	MaxProxyCRF     = 51
	// DefaultProxyPreset trades ratio for speed. The proxy competes with the
	// live stream for CPU even under the governor's nice level, so finishing
	// sooner is worth a few percent of size.
	DefaultProxyPreset = "veryfast"
	// DefaultProxyAudioKbps is speech-grade stereo AAC. The proxy's audio
	// exists so an editor can hear where a sentence starts.
	DefaultProxyAudioKbps = 96
	// DefaultProxyKbps is the fallback for encoders that have no CRF.
	DefaultProxyKbps = 700
	// DefaultProxyGOPSeconds is the keyframe interval, and it is the single
	// most important number in this file. A player can only seek to a keyframe
	// without decoding forward, so this IS the scrub granularity: 2 s feels
	// instant, and the 10 s interval a live encoder typically ships makes the
	// timeline feel broken.
	DefaultProxyGOPSeconds = 2
	// ProxyEncoder is the default video encoder. See ProxyArgs for why this is
	// not chosen from the hardware probe.
	ProxyEncoder = "libx264"
)

// ProxySpec describes one recording's scrubbing proxy.
type ProxySpec struct {
	// Input is the master recording. Output is the .mp4 to write.
	Input  string
	Output string

	// Width and Height size the proxy. Either may be 0; with both 0 the height
	// falls back to DefaultProxyHeight, because a proxy that inherited a 4K
	// source's dimensions would defeat the entire feature.
	Width  int
	Height int

	// Encoder is an FFmpeg encoder name; empty means libx264.
	Encoder string
	// Preset is the encoder's speed/quality knob.
	Preset string
	// CRF is the quality target. Ignored when VideoKbps is set, and when the
	// encoder has no CRF.
	CRF int
	// VideoKbps forces average-bitrate rate control instead of CRF. Mostly for
	// an operator who needs a predictable proxy size per hour.
	VideoKbps int

	// FPS caps the proxy's frame rate; 0 keeps the source rate. Capping is the
	// cheapest size lever there is, but it also coarsens frame stepping, so it
	// is offered rather than assumed.
	FPS float64

	// AudioTrack is the 0-based ingest audio track to carry. The proxy is for
	// navigation, so it carries ONE track by default rather than all of them —
	// the master keeps every track, and a browser would not play them anyway.
	// Negative means a silent proxy.
	AudioTrack int
	// AudioKbps is the AAC bitrate; 0 means DefaultProxyAudioKbps.
	AudioKbps int

	// GOPSeconds is the keyframe interval; 0 means DefaultProxyGOPSeconds.
	GOPSeconds float64
}

// crfEncoders are the encoders whose -crf means what we mean by it.
//
// Hardware encoders spell their quality knob differently and, worse, disagree
// about its direction — videotoolbox's -q:v counts up where x264's -crf counts
// down. Rather than maintain a translation table for a job this cheap, an
// encoder that is not on this list gets average-bitrate rate control, which
// every encoder understands identically.
var crfEncoders = map[string]bool{
	"libx264":    true,
	"libx264rgb": true,
	"libx265":    true,
	"libvpx-vp9": true,
	"libsvtav1":  true,
	"libaom-av1": true,
	"librav1e":   true,
}

// ProxyArgs builds the proxy transcode.
//
// libx264 is the default and the hardware probe is deliberately NOT consulted.
// A 360p encode is a rounding error on any CPU that can run this product at
// all, while the GPU is the one resource the live stream cannot share — the
// governor already knows to hold GPU work back while an ingest is up, and the
// cheapest way to never be that work is to not want the GPU. An operator who
// wants hardware can name an encoder.
func ProxyArgs(s ProxySpec) []string {
	if s.Width <= 0 && s.Height <= 0 {
		s.Height = DefaultProxyHeight
	}
	if s.Encoder == "" {
		s.Encoder = ProxyEncoder
	}
	if s.GOPSeconds <= 0 {
		s.GOPSeconds = DefaultProxyGOPSeconds
	}
	if s.AudioKbps <= 0 {
		s.AudioKbps = DefaultProxyAudioKbps
	}

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args, "-i", s.Input)

	// Explicit maps. Default stream selection on a multitrack Matroska picks
	// one audio track by its own rules, and "whichever track FFmpeg liked" is
	// not something a UI can label.
	args = append(args, "-map", "0:v:0")
	if s.AudioTrack >= 0 {
		// The trailing '?' makes a missing track survivable: a recording with
		// fewer tracks than the caller believed yields a silent proxy instead
		// of a failed job. Safe here in a way it is not in the stem recorder,
		// because -map 0:v:0 above is unconditional — FFmpeg only falls back to
		// default stream selection for an output with NO matching maps at all.
		args = append(args, "-map", "0:a:"+strconv.Itoa(s.AudioTrack)+"?")
	}
	// Subtitles, chapters and attachments in the master have no business in a
	// navigation copy, and an attachment stream is a common reason an MP4 mux
	// refuses to start.
	args = append(args, "-sn", "-dn", "-map_chapters", "-1")

	args = append(args, "-c:v", s.Encoder)
	if p := s.Preset; p != "" {
		args = append(args, "-preset", p)
	} else if s.Encoder == ProxyEncoder {
		args = append(args, "-preset", DefaultProxyPreset)
	}
	args = append(args, proxyRateArgs(s)...)
	// yuv420p is not optional for a browser: a 10-bit or 4:2:2 master would
	// otherwise produce a High10/422 proxy that no browser decodes, which is
	// the exact problem the proxy exists to solve.
	args = append(args, "-pix_fmt", "yuv420p")

	if vf := scaleFilter(s.Width, s.Height); vf != "" {
		args = append(args, "-vf", vf)
	}
	if s.FPS > 0 {
		args = append(args, "-r", strconv.FormatFloat(s.FPS, 'g', -1, 64))
	}

	// Time-based rather than -g N, because the frame rate of the master is not
	// known here and a keyframe interval counted in frames would be 10 s on a
	// 60 fps source and 1 s on a 6 fps screen capture.
	args = append(args, "-force_key_frames",
		"expr:gte(t,n_forced*"+formatSeconds(s.GOPSeconds)+")")

	if s.AudioTrack >= 0 {
		// Downmixed to stereo on purpose: a 5.1 or 8-channel track in an AAC
		// proxy is larger, and no scrub bar has ever needed a surround field.
		args = append(args,
			"-c:a", "aac",
			"-b:a", strconv.Itoa(s.AudioKbps)+"k",
			"-ac", "2",
		)
	} else {
		args = append(args, "-an")
	}

	// -movflags +faststart is the whole reason this file is playable.
	//
	// Without it the moov atom — the index that says where every frame lives —
	// is written last, so a browser must download the ENTIRE file before it can
	// display frame one or seek anywhere. On an hour-long segment that is the
	// difference between a timeline that scrubs instantly and one that appears
	// to hang. It costs a second pass over the output at the end of the encode,
	// which is why FFmpeg does not do it by default.
	args = append(args, "-movflags", "+faststart", "-f", "mp4", s.Output)
	return args
}

// proxyRateArgs picks CRF or average bitrate. See crfEncoders for why an
// unknown encoder gets bitrate rather than a guess at its quality flag.
func proxyRateArgs(s ProxySpec) []string {
	if s.VideoKbps <= 0 && crfEncoders[s.Encoder] {
		crf := s.CRF
		if crf <= 0 {
			crf = DefaultProxyCRF
		}
		return []string{"-crf", strconv.Itoa(clampInt(crf, MinProxyCRF, MaxProxyCRF))}
	}
	kbps := s.VideoKbps
	if kbps <= 0 {
		kbps = DefaultProxyKbps
	}
	return []string{
		"-b:v", strconv.Itoa(kbps) + "k",
		"-maxrate", strconv.Itoa(kbps) + "k",
		"-bufsize", strconv.Itoa(kbps*2) + "k",
	}
}
