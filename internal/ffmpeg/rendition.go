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

// AspectMode decides what happens to a frame whose shape does not match the
// rendition's Width x Height. It is the whole of dual-format output: a 9:16
// rendition of a 16:9 ingest is one more entry in the ladder, encoded once and
// shared, not a second pipeline.
//
// The zero value is deliberately the historical behaviour. Every rendition
// saved before this existed scaled straight to the target size, and must keep
// compiling to exactly that filter string.
type AspectMode string

const (
	// AspectStretch scales to Width x Height and lets the picture distort if
	// the source disagrees. Anamorphic, and almost never what anyone wants —
	// but it is what this code did before dual-format, so it stays the default.
	AspectStretch AspectMode = ""
	// AspectCrop centre-crops to the target shape and then scales. Subjects
	// keep their on-screen size; the edges of the frame are gone.
	AspectCrop AspectMode = "crop"
	// AspectPad scales the whole frame to fit and fills the remainder with a
	// flat colour. Nothing is lost, but a 16:9 source on a 9:16 canvas is
	// mostly bars.
	AspectPad AspectMode = "pad"
	// AspectBlurredPad fills the remainder with a blurred, cropped-to-fill copy
	// of the frame itself. This is the convention every vertical feed has
	// settled on, and it is the difference between a repurposed landscape
	// stream looking deliberate and looking lazy.
	AspectBlurredPad AspectMode = "blurpad"
)

// AspectModes is every mode a rendition may name, in the order to offer them:
// the no-op first, then increasing amounts of work.
var AspectModes = []AspectMode{AspectStretch, AspectCrop, AspectPad, AspectBlurredPad}

// defaultPadColor is the letterbox fill. Black, because a bar the viewer does
// not notice is the entire goal of a bar.
const defaultPadColor = "black"

// The blurred background is built from a shrunken copy rather than blurred in
// place. A gaussian wide enough to read as "background" costs more per frame at
// 1080p than the H.264 encode it feeds, and this runs on a live stream; on a
// 1/8-scale proxy the same look costs a rounding error, because the upscale
// back to full size does most of the blurring for free.
const (
	blurProxyDivisor = 8
	// blurProxySigma is measured in PROXY pixels, so it lands around 8x this
	// once the proxy is scaled back up.
	blurProxySigma = 4
	// minBlurProxyDimension keeps a small rendition from blurring a handful of
	// pixels into one flat colour.
	minBlurProxyDimension = 32
)

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

	// Aspect reconciles the source's shape with Width x Height. The zero value
	// stretches, which is what every pre-dual-format rendition did.
	//
	// It needs BOTH dimensions to mean anything: an aspect conversion is
	// defined by the target shape, and "1080 tall, width from the source" has
	// no shape to convert to. With one dimension set, or with a mode this
	// binary does not recognise, the rendition falls back to the plain scale
	// rather than refusing to start.
	Aspect AspectMode
	// PadColor is the AspectPad fill; empty means black. Ignored by the other
	// modes.
	PadColor string

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
	if fit := aspectFilter(s); fit != "" {
		// The aspect chain already ends at exactly Width x Height, so the plain
		// scale would be a second, redundant resize.
		chain = append(chain, fit)
	} else if scale := scaleFilter(s.Width, s.Height); scale != "" {
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

// ------------------------------------------------------------- dual format

// aspectFilter renders the aspect-conversion chain, or "" when this rendition
// is a plain scale and the caller should fall back to scaleFilter.
//
// An unrecognised mode degrades to the plain scale on purpose. A rendition row
// written by a newer build, or hand-edited in the database, must still encode:
// this repo has already paid three times for a check that was wrong in the
// restrictive direction, and a stream that does not start is a worse answer
// than a stream in the wrong shape.
func aspectFilter(s RenditionSpec) string {
	if s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	switch s.Aspect {
	case AspectCrop:
		return cropFitFilter(s.Width, s.Height)
	case AspectPad:
		return padFitFilter(s.Width, s.Height, s.PadColor)
	case AspectBlurredPad:
		return blurredPadFilter(s.Width, s.Height)
	default:
		return ""
	}
}

// cropFitFilter centre-crops to the target shape, then scales.
//
// One expression covers both directions, which is why there is no
// landscape/portrait branch anywhere in this file: cropping 16:9 to 9:16 keeps
// the full height and trims the width, cropping 9:16 to 16:9 does the reverse,
// and min() picks whichever of the two the source actually needs.
//
// crop's own x/y default to centred, and vf_crop masks them down to the chroma
// grid itself, so the offsets are deliberately left unstated.
//
// setsar=1 is load-bearing rather than tidy. Rounding the crop down to an even
// number of pixels leaves it up to one pixel off the exact target ratio, and
// scale preserves DISPLAY aspect by pushing that error into the sample aspect
// ratio: without this the file is 1080x1920 carrying SAR 404:405, so a player
// that honours SAR shows a 9:16 rendition very slightly un-square. Verified
// against real FFmpeg — it is not a hypothetical.
func cropFitFilter(w, h int) string {
	cw := evenExpr(fmt.Sprintf("min(iw\\,ih*%d/%d)", w, h))
	ch := evenExpr(fmt.Sprintf("min(ih\\,iw*%d/%d)", h, w))
	return fmt.Sprintf("crop=%s:%s,scale=%d:%d,setsar=1", cw, ch, w, h)
}

// padFitFilter scales the whole frame to fit and letterboxes the remainder.
//
// setsar=1 closes the chain because the padded frame IS the target shape now:
// an anamorphic source that arrived with a non-square SAR would otherwise hand
// the player a display aspect that no longer describes the canvas we built.
func padFitFilter(w, h int, color string) string {
	return fmt.Sprintf("%s,pad=%d:%d:%s:%s:%s,setsar=1",
		fitInsideFilter(w, h), w, h,
		evenExpr("(ow-iw)/2"), evenExpr("(oh-ih)/2"), padColor(color))
}

// blurredPadFilter centres the real frame on a blurred, cropped-to-fill copy of
// itself.
//
// split feeds one decoded frame to both halves, so the background is always the
// picture behind it rather than a still or a colour. The background is built at
// proxy size and blown back up; see blurProxyDivisor for why.
func blurredPadFilter(w, h int) string {
	bw, bh := blurProxySize(w, h)
	var b strings.Builder
	b.WriteString("split=2[bgsrc][fgsrc];")
	fmt.Fprintf(&b, "[bgsrc]scale=%d:%d:force_original_aspect_ratio=increase:force_divisible_by=2,"+
		"crop=%d:%d,gblur=sigma=%d,scale=%d:%d,setsar=1[bg];",
		bw, bh, bw, bh, blurProxySigma, w, h)
	fmt.Fprintf(&b, "[fgsrc]%s[fg];", fitInsideFilter(w, h))
	// W/H are the background's dimensions and w/h the foreground's, which is
	// overlay's own vocabulary rather than ours; the result is the real frame
	// centred on the canvas.
	fmt.Fprintf(&b, "[bg][fg]overlay=%s:%s", evenExpr("(W-w)/2"), evenExpr("(H-h)/2"))
	return b.String()
}

// fitInsideFilter scales to the largest size that fits inside w x h with the
// source's own aspect ratio intact.
//
// force_divisible_by=2 is not decoration: the derived side of a
// force_original_aspect_ratio scale lands wherever the arithmetic puts it, and
// an odd width reaches the encoder as "width not divisible by 2" — a start
// failure, not a warning. It needs FFmpeg 4.4, comfortably below the 6.0 the
// startup check already demands.
func fitInsideFilter(w, h int) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2", w, h)
}

// blurProxySize is the background's working size, kept even so the proxy is
// itself a legal 4:2:0 frame.
func blurProxySize(w, h int) (int, int) {
	return evenDown(max(w/blurProxyDivisor, minBlurProxyDimension)),
		evenDown(max(h/blurProxyDivisor, minBlurProxyDimension))
}

// evenExpr wraps an FFmpeg expression so its value lands on an even number of
// pixels, which 4:2:0 chroma subsampling requires of every dimension and which
// keeps a composited frame on the chroma grid.
func evenExpr(expr string) string { return "2*floor(" + expr + "/2)" }

// evenDown rounds down to an even number, with 2 as the floor because a
// zero-sized filter output is not a frame.
func evenDown(v int) int {
	v -= v % 2
	if v < 2 {
		return 2
	}
	return v
}

// padColor keeps operator text off the filter graph unless it is unmistakably a
// colour.
//
// The value lands inside a filter argument, where a comma or a colon would
// silently re-cut the entire chain into different filters. Anything outside
// FFmpeg's colour vocabulary — a bare name, #rrggbb[aa], or 0xRRGGBB — becomes
// black rather than an error, because a mistyped colour must not be the reason
// a live stream will not start. The alpha suffix ("black@0.5") is not accepted:
// a translucent letterbox has nothing behind it.
func padColor(c string) string {
	c = strings.TrimSpace(c)
	if c == "" || len(c) > 24 {
		return defaultPadColor
	}
	for i, r := range c {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '#' && i == 0:
		default:
			return defaultPadColor
		}
	}
	return c
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
