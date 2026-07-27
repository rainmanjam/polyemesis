package ffmpeg

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ------------------------------------------------------------------ renditions

// Video encoder names, spelled the way FFmpeg spells them, because these
// strings are written straight into the command line and stored in the
// database.
const (
	EncoderX264         = "libx264"
	EncoderNVENC        = "h264_nvenc"
	EncoderQSV          = "h264_qsv"
	EncoderVideoToolbox = "h264_videotoolbox"
	EncoderVAAPI        = "h264_vaapi"
	EncoderAMF          = "h264_amf"
)

// hwEncoders is the hardware-accelerated set, in the order we would offer them.
// Detection intersects this with what the binary actually reports.
var hwEncoders = []string{EncoderNVENC, EncoderQSV, EncoderVideoToolbox, EncoderVAAPI, EncoderAMF}

// defaultVAAPIDevice is the first render node on a typical Linux box. VAAPI is
// the one encoder that cannot work without knowing its device up front.
const defaultVAAPIDevice = "/dev/dri/renderD128"

// assumedSourceFPS is the frame rate used for the GOP arithmetic when neither
// the target nor the probed source rate is known. Guessing low is the safe
// direction: on a 60 fps source it yields 1 s keyframes rather than 4 s, which
// costs a little bitrate but never breaks platform-side segmenting.
const assumedSourceFPS = 30

// RenditionSpec describes one shared video encode.
//
// A rendition re-encodes VIDEO ONLY and copies every audio track through
// untouched. That is the whole reason renditions can exist alongside
// per-destination audio routing: the destinations downstream still do their own
// `-c:v copy` plus their own routing graph, so audio is mixed exactly once, at
// the destination, and never encoded twice.
type RenditionSpec struct {
	// InRelayURL is the hub this rendition reads, normally the ingest's.
	InRelayURL string
	// OutRelayURL is the rendition's OWN hub, which its destinations subscribe
	// to instead of the ingest's.
	OutRelayURL string

	// Width and Height size the output. Either may be 0, meaning "keep the
	// source dimension and derive this one".
	Width  int
	Height int

	// FPS is the output frame rate; 0 keeps the source rate.
	FPS float64
	// SourceFPS is the probed ingest rate. Only used for the GOP arithmetic
	// when FPS is 0, since a keyframe interval is counted in frames.
	SourceFPS float64

	VideoKbps   int
	MaxrateKbps int // 0 => VideoKbps (platforms want a capped stream)
	BufsizeKbps int // 0 => 2x maxrate

	// Encoder is an FFmpeg encoder name; empty means libx264.
	Encoder string
	// Preset is the encoder's speed/quality knob. Empty takes the per-encoder
	// default; encoders that have no such knob ignore it.
	Preset string
	// VAAPIDevice overrides the render node for h264_vaapi.
	VAAPIDevice string

	// GOPSeconds is the forced keyframe interval; 0 means 2 seconds.
	GOPSeconds float64
}

// encoderProfile captures the ways FFmpeg encoders disagree about their own
// flags. Handing an encoder a flag it does not own is not free: FFmpeg either
// warns loudly on every start or, for the hardware wrappers, refuses to open.
type encoderProfile struct {
	// presetFlag is empty for encoders with no speed/quality knob.
	presetFlag    string
	defaultPreset string
	// rateControl are flags this encoder needs before -b:v means what we mean.
	rateControl []string
	// vaapi marks the encoder that needs a device and a hwupload filter tail.
	vaapi bool
}

var encoderProfiles = map[string]encoderProfile{
	// yuv420p is forced because a 10-bit or 4:2:2 ingest would otherwise
	// produce a High10/422 stream that no streaming platform will accept.
	EncoderX264: {
		presetFlag:    "-preset",
		defaultPreset: "veryfast",
		rateControl:   []string{"-profile:v", "high", "-pix_fmt", "yuv420p"},
	},
	// NVENC's p1..p7 presets replaced the named ones; p4 is the middle,
	// "medium" equivalent and the honest default for a live encode.
	EncoderNVENC: {
		presetFlag:    "-preset",
		defaultPreset: "p4",
		rateControl:   []string{"-rc", "cbr", "-profile:v", "high"},
	},
	EncoderQSV: {
		presetFlag:    "-preset",
		defaultPreset: "veryfast",
		rateControl:   []string{"-profile:v", "high"},
	},
	// VideoToolbox has no preset at all; -realtime is its equivalent lever.
	EncoderVideoToolbox: {
		rateControl: []string{"-realtime", "1", "-profile:v", "high"},
	},
	// VAAPI takes neither a preset nor a profile name; everything it needs
	// comes from the device and the filter chain.
	EncoderVAAPI: {vaapi: true},
	// AMF spells its preset "-quality", and "-usage transcoding" is what
	// selects the streaming rate-control behaviour rather than the low-latency
	// screen-sharing one.
	EncoderAMF: {
		presetFlag:    "-quality",
		defaultPreset: "speed",
		rateControl:   []string{"-usage", "transcoding", "-profile:v", "high"},
	},
}

// RenditionArgs builds one shared video encode.
//
// The load-bearing line is `-map 0:a -c:a copy`: every audio track the ingest
// carries arrives at the destinations bit-identical, so per-destination routing
// downstream still sees the full multitrack ingest. If this ever becomes an
// audio encode or a mixdown, the product's differentiator is gone.
func RenditionArgs(s RenditionSpec) []string {
	if s.VideoKbps <= 0 {
		s.VideoKbps = 4500
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
	if s.Encoder == "" {
		s.Encoder = EncoderX264
	}
	// An encoder we have no profile for is still usable: pass only the flags
	// every encoder understands, plus a preset if the user explicitly asked
	// for one. Refusing to run would make a custom encoder unusable, and
	// assuming a default preset would break the ones that have no such option.
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
		// Must precede -i: the device has to exist before the filter graph
		// that uploads into it is configured.
		args = append(args, "-vaapi_device", dev)
	}

	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", RelayInputURL(s.InRelayURL),
		// Explicit maps only. Default stream selection would take one audio
		// track and drop the rest, which is the exact failure this feature
		// exists to avoid.
		"-map", "0:v:0",
		"-map", "0:a",
		"-c:v", s.Encoder,
	)
	args = append(args, presetArgs(prof, s.Preset)...)
	args = append(args, prof.rateControl...)
	args = append(args,
		"-b:v", strconv.Itoa(s.VideoKbps)+"k",
		"-maxrate", strconv.Itoa(s.MaxrateKbps)+"k",
		"-bufsize", strconv.Itoa(s.BufsizeKbps)+"k",
	)

	if vf := videoFilter(s, prof); vf != "" {
		args = append(args, "-vf", vf)
	}
	if s.FPS > 0 {
		args = append(args, "-r", formatFPS(s.FPS))
	}

	// Forcing the GOP is a real benefit of renditions, not a detail. With
	// -c:v copy the user inherits whatever keyframe interval OBS was set to,
	// and a 10 s interval breaks HLS/DASH packaging on the platform side.
	// keyint_min pins the lower bound and sc_threshold 0 stops scene cuts
	// from inserting extra keyframes that would desync the segmenting.
	gop := gopFrames(s)
	args = append(args,
		"-g", strconv.Itoa(gop),
		"-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0",
		"-c:a", "copy",
		"-f", "mpegts",
		"-flush_packets", "1",
		RelayOutputURL(s.OutRelayURL),
	)
	return args
}

func presetArgs(prof encoderProfile, want string) []string {
	if prof.presetFlag == "" {
		// VideoToolbox and VAAPI have no preset option; passing one makes
		// FFmpeg complain about an unused AVOption on every restart.
		return nil
	}
	if want == "" {
		want = prof.defaultPreset
	}
	if want == "" {
		return nil
	}
	return []string{prof.presetFlag, want}
}

// videoFilter renders the scale chain, or "" when there is nothing to do.
func videoFilter(s RenditionSpec, prof encoderProfile) string {
	var chain []string
	if scale := scaleFilter(s.Width, s.Height); scale != "" {
		chain = append(chain, scale)
	}
	if prof.vaapi {
		// VAAPI encodes from GPU surfaces, so even an unscaled rendition needs
		// the frames converted and uploaded.
		chain = append(chain, "format=nv12", "hwupload")
	}
	return strings.Join(chain, ",")
}

// scaleFilter sizes the output, or returns "" when neither dimension is set —
// a no-op scale filter still costs a full colour-space round trip, so it is
// worth omitting.
//
// -2 (not -1) derives the missing dimension rounded to an even number, which
// H.264's 4:2:0 chroma subsampling requires; -1 can land on an odd height and
// the encoder then refuses to open.
func scaleFilter(w, h int) string {
	switch {
	case w > 0 && h > 0:
		return fmt.Sprintf("scale=%d:%d", w, h)
	case w > 0:
		return fmt.Sprintf("scale=%d:-2", w)
	case h > 0:
		return fmt.Sprintf("scale=-2:%d", h)
	default:
		return ""
	}
}

// gopFrames converts the configured keyframe interval in seconds into the
// frame count FFmpeg wants.
func gopFrames(s RenditionSpec) int {
	fps := s.FPS
	if fps <= 0 {
		fps = s.SourceFPS
	}
	if fps <= 0 {
		fps = assumedSourceFPS
	}
	g := int(math.Round(fps * s.GOPSeconds))
	if g < 1 {
		g = 1
	}
	return g
}

// formatFPS renders a rate without trailing zeros, so 30 stays "30" and NTSC
// rates survive as "29.97" rather than becoming an integer.
func formatFPS(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
