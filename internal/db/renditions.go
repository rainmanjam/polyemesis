package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// VideoEncoder is the FFmpeg encoder a rendition drives. Software and the
// three hardware families we can reasonably expect to meet: NVIDIA (nvenc),
// Intel Quick Sync (qsv), Apple (videotoolbox), plus VA-API and AMD AMF on
// Linux/Windows. Availability is a property of the FFmpeg build, not of this
// table, so a rendition can name an encoder the running FFmpeg lacks — that is
// a start-time failure with a clear message, not a config-time one.
type VideoEncoder string

const (
	EncoderX264 VideoEncoder = "libx264"
	EncoderX265 VideoEncoder = "libx265"

	EncoderNVENCH264 VideoEncoder = "h264_nvenc"
	EncoderNVENCHEVC VideoEncoder = "hevc_nvenc"

	EncoderQSVH264 VideoEncoder = "h264_qsv"
	EncoderQSVHEVC VideoEncoder = "hevc_qsv"

	EncoderVideoToolboxH264 VideoEncoder = "h264_videotoolbox"
	EncoderVideoToolboxHEVC VideoEncoder = "hevc_videotoolbox"

	EncoderVAAPIH264 VideoEncoder = "h264_vaapi"
	EncoderVAAPIHEVC VideoEncoder = "hevc_vaapi"

	EncoderAMFH264 VideoEncoder = "h264_amf"
	EncoderAMFHEVC VideoEncoder = "hevc_amf"
)

// KnownEncoders is every encoder a rendition may name, in the order the UI
// should offer them: software first, because it is the one that always works.
var KnownEncoders = []VideoEncoder{
	EncoderX264, EncoderX265,
	EncoderNVENCH264, EncoderNVENCHEVC,
	EncoderQSVH264, EncoderQSVHEVC,
	EncoderVideoToolboxH264, EncoderVideoToolboxHEVC,
	EncoderVAAPIH264, EncoderVAAPIHEVC,
	EncoderAMFH264, EncoderAMFHEVC,
}

// Codec returns the bitstream the encoder produces: "h264" or "hevc". RTMP
// ingests are overwhelmingly H.264-only, so callers use this to warn.
func (e VideoEncoder) Codec() string {
	if e == EncoderX265 || strings.HasPrefix(string(e), "hevc_") {
		return "hevc"
	}
	return "h264"
}

// Rendition bounds. Deliberately generous: these exist to catch a typo or a
// unit mix-up (6 instead of 6000), not to express any platform's policy.
const (
	MinRenditionDimension = 128
	MaxRenditionDimension = 7680 // 8K wide, i.e. beyond anything we would encode
	MaxRenditionFPS       = 240
	MinRenditionBitrate   = 100     // kbps
	MaxRenditionBitrate   = 100_000 // kbps
	MinRenditionGOP       = 1.0     // seconds
	MaxRenditionGOP       = 10.0    // seconds
)

// Rendition is one shared video encode.
//
// The load-bearing rule of the whole feature: a rendition re-encodes VIDEO
// ONLY and passes every audio track through with -c:a copy. Destinations still
// do "-c:v copy" plus their own routing graph, so per-destination audio routing
// keeps working on top of a shared video encode and audio is never encoded
// twice. There is deliberately no audio field here, and there must never be
// one.
//
// A destination with no rendition (rendition_id NULL) is "passthrough": no
// process, no CPU, subscribed straight to the ingest relay. That is the
// default and the behaviour every pre-renditions install already has.
type Rendition struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Width/Height are the output size; 0 on either axis means "keep the
	// source's", so setting only Height rescales while preserving aspect.
	Width  int `json:"width"`
	Height int `json:"height"`
	// FPS is the output frame rate, 0 meaning "keep the source's". Integer
	// because every platform tier is expressed in whole frames; a 59.94 source
	// that wants its exact rate left alone uses 0.
	FPS int `json:"fps"`
	// VideoBitrate is the target in kbps. Always set — a rendition that does
	// not change size, rate or bitrate has no reason to exist.
	VideoBitrate int `json:"videoBitrate"`
	// MaxrateKbps and BufsizeKbps are the rest of the rate-control triple, and
	// 0 on either means "derive it": maxrate falls back to the bitrate and
	// bufsize to twice the maxrate, which is CBR and is what every rendition
	// did before these existed.
	//
	// WHY THEY ARE SETTABLE AT ALL, given CBR is right for live. The services
	// registry carries a MaxVideoKbps per platform, read out of OBS's own
	// service list, and a ceiling is precisely what a maxrate is for: a
	// destination can average below what the platform recommends and still be
	// allowed the burst it permits. Before this, the fields existed on
	// ffmpeg.RenditionSpec and the argv builder used them, and nothing in the
	// database or the API could reach them -- so the code described a
	// capability the product did not have. See #341.
	//
	// A maxrate BELOW the bitrate is refused rather than clamped: it is not a
	// preference the encoder can honour, it is a contradiction, and silently
	// rewriting one of the two numbers is how a stream ends up at a bitrate
	// nobody chose.
	MaxrateKbps int          `json:"maxrateKbps"`
	BufsizeKbps int          `json:"bufsizeKbps"`
	Encoder     VideoEncoder `json:"encoder"`
	// Preset is the encoder's own speed/quality knob ("veryfast" for x264,
	// "p4" for nvenc, "quality" for amf...). Free text because the vocabulary
	// is per-encoder and changes between FFmpeg releases; validated only for
	// shape, since it lands on a command line.
	Preset string `json:"preset"`
	// GOPSeconds is the keyframe interval in seconds rather than frames, so it
	// stays correct when FPS changes. Live platforms want 1-4s.
	GOPSeconds float64 `json:"gopSeconds"`
	// AspectMode decides what happens when the target shape does not match the
	// source's — the vertical-plus-horizontal case. Empty is the historical
	// behaviour, a plain scale that stretches, so every stored rendition keeps
	// producing the frame it always did.
	//
	// It only takes effect when BOTH Width and Height are set: with one axis
	// free there is no mismatch to resolve.
	AspectMode string `json:"aspectMode,omitempty"`
	// Deinterlace strips field combing before scaling. '' (off), 'auto' or
	// 'all'. Empty is off, so every stored rendition keeps producing exactly
	// the frame it always did.
	Deinterlace string `json:"deinterlace,omitempty"`
	// PadColor is the bar colour for the padding modes, in any syntax FFmpeg's
	// colour parser takes. Empty means black.
	PadColor string `json:"padColor,omitempty"`
	// Overlay is an optional image watermark burned into this tier.
	//
	// Stored as columns rather than as its own table, which is a deliberate
	// narrowing of the roadmap design. That design wants a table because the
	// full feature has several overlays per rendition and reuses one row across
	// renditions; v0.5 has neither -- one image, one rendition -- and a join
	// table for a strictly 1:1 relationship is structure with nothing in it.
	// Growing to the table later is a data migration of six columns, which is
	// cheaper than carrying the join now. See docs/roadmap/OVERLAYS.md.
	//
	// Empty OverlayImage is no overlay, so every rendition that predates this
	// keeps producing exactly the frame it always did.
	Overlay RenditionOverlay `json:"overlay"`
	// Text is an optional line of text burned into this tier, drawn on top of
	// Overlay. Columns rather than a table for the reason Overlay gives.
	//
	// Empty Content is no text, so every rendition that predates this keeps
	// producing exactly the frame it always did.
	Text RenditionText `json:"text"`
	// Note is the "what is this tier for" line. Preset-derived renditions
	// arrive with one already filled in; the user can rewrite it.
	Note string `json:"note"`
	// SourceID is the programme this rendition re-encodes. A rendition reads
	// exactly one ingest, so it belongs to exactly one source; nil means the
	// source was deleted, which CASCADE makes unreachable in practice.
	// CreateRendition fills it with the default source when omitted.
	SourceID  *int64    `json:"sourceId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Overlay bounds. Percentages are stored 0-1 rather than 0-100 because that is
// what the filter arithmetic wants, and converting at one boundary is fewer
// places to get it wrong than converting at every use.
const (
	// MaxOverlayImagePath matches the slate's cap, and for the same reason.
	MaxOverlayImagePath = 512
	// MinOverlayWidthPct stops a watermark being scaled to something invisible
	// that an operator then hunts for in the encoder logs.
	MinOverlayWidthPct  = 0.01
	MaxOverlayWidthPct  = 1.0
	MaxOverlayMarginPct = 0.45
)

// RenditionOverlay is an image watermark burned into a rendition.
//
// Geometry is percentage-based, and that is the whole point rather than a
// detail: the same overlay has to be correct on a 1920x1080 tier and a
// 1080x1920 one, and pixel geometry that looks right on the first lands
// off-canvas on the second with nothing to report it.
type RenditionOverlay struct {
	// Image is a path relative to the data directory, confined the same way a
	// slate image and a file:// pull source are. Empty means no overlay.
	Image string `json:"image,omitempty"`
	// Anchor is which corner, edge or centre the image is pinned to.
	Anchor string `json:"anchor,omitempty"`
	// WidthPct is the image's width as a fraction of the output width.
	WidthPct float64 `json:"widthPct,omitempty"`
	// MarginXPct and MarginYPct are the gap from the anchored edges, as
	// fractions of the output width and height. Ignored on a centred axis.
	MarginXPct float64 `json:"marginXPct,omitempty"`
	MarginYPct float64 `json:"marginYPct,omitempty"`
	// Opacity is 0-1; 0 is treated as 1 so a row saved before this field
	// existed does not render an invisible watermark.
	Opacity float64 `json:"opacity,omitempty"`
}

// Active reports whether this rendition actually carries a watermark.
func (o RenditionOverlay) Active() bool { return strings.TrimSpace(o.Image) != "" }

// problems reports everything wrong with the overlay, given the rendition's
// output size. Nothing is checked when no image is set: half-filled geometry on
// a rendition with no watermark cannot misbehave.
func (o RenditionOverlay) problems(width, height int) []string {
	if !o.Active() {
		return nil
	}
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	// The rule that would otherwise be discovered as a watermark that silently
	// is not there.
	//
	// The image is scaled to a percentage of the OUTPUT width, so that width has
	// to resolve to a number when the arguments are built. With one axis free
	// it does not, and the filter builder degrades to no overlay at all --
	// which reaches the operator as a stream that starts, looks fine, and has
	// no logo, with nothing anywhere saying why. Exactly the failure the
	// deinterlace validation was added for.
	if width <= 0 || height <= 0 {
		add("an overlay needs the rendition to have an explicit width AND height, " +
			"because the image is sized as a percentage of the output")
	}

	if err := overlayPathProblem(o.Image); err != nil {
		add("%v", err)
	}

	if o.Anchor != "" {
		known := false
		for _, a := range ffmpeg.OverlayAnchors {
			if string(a) == o.Anchor {
				known = true
				break
			}
		}
		// Refused rather than accepted, for the same reason an unknown
		// deinterlace mode is: the filter builder degrades an unrecognised
		// anchor to top-left, so the operator would get a logo in a corner they
		// did not choose with nothing to tell them which one is running.
		if !known {
			add("unknown overlay anchor %q", o.Anchor)
		}
	}

	if o.WidthPct < MinOverlayWidthPct || o.WidthPct > MaxOverlayWidthPct {
		add("overlay width %.3f out of range (%.2f-%.2f of the output width)",
			o.WidthPct, MinOverlayWidthPct, MaxOverlayWidthPct)
	}
	for _, m := range []struct {
		name string
		v    float64
	}{{"horizontal", o.MarginXPct}, {"vertical", o.MarginYPct}} {
		// Capped well below 0.5 because a margin at half the canvas pushes an
		// edge-anchored logo past the opposite edge, and a watermark rendered
		// off-frame is indistinguishable from one that never rendered.
		if m.v < 0 || m.v > MaxOverlayMarginPct {
			add("overlay %s margin %.3f out of range (0-%.2f)", m.name, m.v, MaxOverlayMarginPct)
		}
	}
	if o.Opacity < 0 || o.Opacity > 1 {
		add("overlay opacity %.3f out of range (0-1)", o.Opacity)
	}
	return probs
}

// Text bounds. Percentages are 0-1 for the reason the overlay's are.
const (
	// MaxTextLen is generous for a station ident or a lower third and well
	// short of anything that would push a filtergraph past a command-line
	// limit.
	MaxTextLen = 200
	// MaxTextFontName matches the fonts directory's own naming: a bare
	// filename, not a path.
	MaxTextFontName = 128
	// MinTextSizePct is where type stops surviving encoding. 1% of a 360p
	// rendition is under four pixels tall.
	MinTextSizePct = 0.01
	// MaxTextSizePct at 0.5 means half the frame height, which is already a
	// full-screen caption; above it nothing legible fits.
	MaxTextSizePct   = 0.5
	MaxTextMarginPct = 0.45
	MaxTextColorLen  = 32
)

// RenditionText is a line of text burned into a rendition.
//
// Separate from RenditionOverlay rather than folded into it: either, both or
// neither may be active, and a rendition with a logo and no caption must not
// have to carry empty text fields that validation then has to special-case.
type RenditionText struct {
	// Content is the literal string drawn. It is never interpreted -- see
	// ffmpeg.drawtextFilter, which sets expansion=none precisely so that a
	// percent sign in a station name is a glyph rather than a directive.
	Content string `json:"content,omitempty"`
	// Font is a BARE FILENAME in the fonts directory, never a path. Empty
	// means the built-in default. The engine resolves it through
	// ffmpeg.FontPath, which is where the confinement lives.
	Font string `json:"font,omitempty"`
	// Anchor is which corner, edge or centre the text is pinned to.
	Anchor string `json:"anchor,omitempty"`
	// SizePct is the type size as a fraction of the output HEIGHT.
	SizePct float64 `json:"sizePct,omitempty"`
	// Color is an FFmpeg colour: a name, 0xRRGGBB, or either with @alpha.
	Color string `json:"color,omitempty"`
	// MarginXPct and MarginYPct are the gap from the anchored edges. Ignored
	// on a centred axis.
	MarginXPct float64 `json:"marginXPct,omitempty"`
	MarginYPct float64 `json:"marginYPct,omitempty"`
	// Box draws a filled rectangle behind the text. It is what makes white
	// text readable over a white shirt.
	Box        bool    `json:"box,omitempty"`
	BoxColor   string  `json:"boxColor,omitempty"`
	BoxOpacity float64 `json:"boxOpacity,omitempty"`
}

// Active reports whether this rendition actually draws text.
func (t RenditionText) Active() bool { return strings.TrimSpace(t.Content) != "" }

// problems reports everything wrong with the text, given the output size.
func (t RenditionText) problems(width, height int) []string {
	if !t.Active() {
		return nil
	}
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	// Same rule as the overlay, and the same failure it prevents: the type is
	// sized as a percentage of the OUTPUT height, so that height has to resolve
	// to a number when the arguments are built. With one axis free it does not,
	// and the operator gets a stream that starts, looks fine and has no
	// caption, with nothing anywhere saying why.
	if width <= 0 || height <= 0 {
		add("text needs the rendition to have an explicit width AND height, " +
			"because the type is sized as a percentage of the output")
	}

	if len(t.Content) > MaxTextLen {
		add("text is %d characters, longer than the %d allowed", len(t.Content), MaxTextLen)
	}
	// A newline would end the filter argument, and a NUL truncates the C
	// string FFmpeg receives -- so both are rejected rather than escaped.
	// drawtext can draw multiple lines, but that is a feature with its own
	// line-spacing question and is not this one.
	if strings.ContainsAny(t.Content, "\x00\n\r") {
		add("text contains a line break or control character; it must be a single line")
	}

	if p := textFontProblem(t.Font); p != "" {
		add("%s", p)
	}
	if t.Anchor != "" && !knownAnchor(t.Anchor) {
		// Refused rather than accepted, for the reason an unknown overlay
		// anchor is: the filter builder degrades an unrecognised anchor to
		// top-left, so the operator gets a caption in a corner they did not
		// choose with nothing to tell them which is running.
		add("unknown text anchor %q", t.Anchor)
	}
	if t.SizePct < MinTextSizePct || t.SizePct > MaxTextSizePct {
		add("text size %.3f out of range (%.2f-%.2f of the output height)",
			t.SizePct, MinTextSizePct, MaxTextSizePct)
	}
	for _, m := range []struct {
		name string
		v    float64
	}{{"horizontal", t.MarginXPct}, {"vertical", t.MarginYPct}} {
		if m.v < 0 || m.v > MaxTextMarginPct {
			add("text %s margin %.3f out of range (0-%.2f)", m.name, m.v, MaxTextMarginPct)
		}
	}
	for _, c := range []struct {
		name string
		v    string
	}{{"text colour", t.Color}, {"box colour", t.BoxColor}} {
		if p := colorProblem(c.name, c.v); p != "" {
			add("%s", p)
		}
	}
	if t.BoxOpacity < 0 || t.BoxOpacity > 1 {
		add("box opacity %.3f out of range (0-1)", t.BoxOpacity)
	}
	return probs
}

func knownAnchor(a string) bool {
	for _, k := range ffmpeg.OverlayAnchors {
		if string(k) == a {
			return true
		}
	}
	return false
}

// textFontProblem checks the font NAME. It cannot check that the file exists --
// this package does not know the data directory -- so it checks the shape, and
// ffmpeg.FontPath does the existence and confinement check at the point of use.
//
// Both separators, spelled literally. `strings.ContainsRune(name,
// os.PathSeparator)` is a check whose meaning changes with GOOS, and this
// codebase has shipped that mistake twice; see internal/recording.Resolve.
func textFontProblem(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "" // the built-in default
	}
	if len(name) > MaxTextFontName {
		return fmt.Sprintf("font name is longer than %d characters", MaxTextFontName)
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Sprintf("font %q must be a bare filename in the fonts directory", name)
	}
	if strings.ContainsAny(name, "\x00\n\r") {
		return "font name contains control characters"
	}
	return ""
}

// colorProblem refuses anything that is not plainly a colour.
//
// This is stricter than FFmpeg's own parser ON PURPOSE. The value becomes a
// filter argument, and although escapeLavfiArg escapes it, a validator that
// accepts arbitrary punctuation here is one escaping bug away from letting a
// database row rewrite the filtergraph. Names, hex and an optional @alpha
// cover every colour anyone actually sets.
func colorProblem(field, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "" // the default
	}
	if len(v) > MaxTextColorLen {
		return fmt.Sprintf("%s is longer than %d characters", field, MaxTextColorLen)
	}
	name, alpha, hasAlpha := strings.Cut(v, "@")
	if name == "" {
		return fmt.Sprintf("%s %q has no colour before the @", field, v)
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '#'
		if !ok {
			return fmt.Sprintf("%s %q must be a colour name or 0xRRGGBB, optionally @alpha", field, v)
		}
	}
	if hasAlpha {
		f, err := strconv.ParseFloat(alpha, 64)
		if err != nil || f < 0 || f > 1 {
			return fmt.Sprintf("%s alpha %q must be a number from 0 to 1", field, alpha)
		}
	}
	return ""
}

// overlayPathProblem confines the image to the data directory.
//
// The same confinement as a slate image and a file:// pull source, and for the
// same reason: the path is operator input that becomes an FFmpeg argument, and
// an absolute path here would be a read primitive for whoever reaches the
// renditions API.
func overlayPathProblem(p string) error {
	p = strings.TrimSpace(p)
	if len(p) > MaxOverlayImagePath {
		return fmt.Errorf("overlay image path is longer than %d characters", MaxOverlayImagePath)
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return errors.New("overlay image path contains control characters")
	}
	// Backslashes are separators on Windows, so normalise before the traversal
	// check or "..\..\secret.key" walks straight past it.
	rel := strings.ReplaceAll(p, `\`, "/")
	switch {
	case strings.HasPrefix(rel, "/"), strings.Contains(rel, ".."),
		len(rel) > 1 && rel[1] == ':':
		return errors.New("overlay image must be a relative path inside the data directory")
	}
	return nil
}

// Codec reports the bitstream this rendition produces.
func (r Rendition) Codec() string { return r.Encoder.Codec() }

// ScalesVideo reports whether the rendition changes the picture size.
func (r Rendition) ScalesVideo() bool { return r.Width > 0 || r.Height > 0 }

// presetTokenOK reports whether s is safe to hand to FFmpeg as a bare
// argument. Preset is user text that becomes an argv entry, so anything with
// whitespace or shell/filter punctuation is rejected outright rather than
// quoted: no legitimate preset name has ever needed it.
func presetTokenOK(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// Validate checks a rendition is encodable, reporting every problem at once so
// the UI can mark up the whole form instead of one field per round trip.
func (r Rendition) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if strings.TrimSpace(r.Name) == "" {
		add("name is required")
	}

	// 0 is the "keep source" sentinel on both axes and must stay legal.
	for _, d := range []struct {
		axis string
		v    int
	}{{"width", r.Width}, {"height", r.Height}} {
		if d.v == 0 {
			continue
		}
		if d.v < MinRenditionDimension || d.v > MaxRenditionDimension {
			add("%s %d out of range (%d-%d, or 0 to keep the source's)",
				d.axis, d.v, MinRenditionDimension, MaxRenditionDimension)
			continue
		}
		// H.264 and HEVC both encode in 16x16 macroblocks with 4:2:0 chroma
		// subsampling, so an odd dimension has no representable chroma plane.
		// FFmpeg fails at start with "width not divisible by 2".
		if d.v%2 != 0 {
			add("%s %d must be an even number of pixels", d.axis, d.v)
		}
	}

	if r.FPS < 0 || r.FPS > MaxRenditionFPS {
		add("fps %d out of range (1-%d, or 0 to keep the source's)", r.FPS, MaxRenditionFPS)
	}

	if r.VideoBitrate < MinRenditionBitrate || r.VideoBitrate > MaxRenditionBitrate {
		add("video bitrate %d kbps out of range (%d-%d)",
			r.VideoBitrate, MinRenditionBitrate, MaxRenditionBitrate)
	}

	known := false
	for _, e := range KnownEncoders {
		if r.Encoder == e {
			known = true
			break
		}
	}
	if !known {
		add("unknown encoder %q", r.Encoder)
	}

	if !presetTokenOK(r.Preset) {
		add("preset %q must be a single word of letters, digits, '-', '_' or '.'", r.Preset)
	}

	if r.GOPSeconds < MinRenditionGOP || r.GOPSeconds > MaxRenditionGOP {
		add("gop %.4gs out of range (%g-%g seconds)", r.GOPSeconds, MinRenditionGOP, MaxRenditionGOP)
	}

	// RATE CONTROL. Zero on either is "derive it" and always legal; the checks
	// below only apply to a value somebody actually set.
	//
	// REFUSED RATHER THAN CLAMPED. A maxrate below the target bitrate is not a
	// preference an encoder can honour, it is a contradiction between two
	// numbers, and there is no way to resolve it without overriding one of them.
	// Picking either silently produces a stream at a bitrate nobody chose, and
	// the operator's evidence would be an output that disagrees with the form
	// they filled in.
	if r.MaxrateKbps != 0 {
		if r.MaxrateKbps < MinRenditionBitrate || r.MaxrateKbps > MaxRenditionBitrate {
			add("maxrate %d kbps out of range (%d-%d)", r.MaxrateKbps, MinRenditionBitrate, MaxRenditionBitrate)
		} else if r.MaxrateKbps < r.VideoBitrate {
			add("maxrate %d kbps is below the target bitrate %d kbps: the encoder cannot average above its own ceiling",
				r.MaxrateKbps, r.VideoBitrate)
		}
	}
	// A bufsize smaller than one second at the ceiling makes the rate controller
	// correct over a window too short to hold a GOP, which shows up as visible
	// pumping on a scene cut rather than as an error. Half a second is already
	// well outside anything deliberate.
	if r.BufsizeKbps != 0 {
		ceiling := r.MaxrateKbps
		if ceiling == 0 {
			ceiling = r.VideoBitrate
		}
		if r.BufsizeKbps < ceiling/2 {
			add("bufsize %d kbps is less than half the %d kbps ceiling: the rate controller would correct over a window shorter than one keyframe interval",
				r.BufsizeKbps, ceiling)
		}
		if r.BufsizeKbps > MaxRenditionBitrate*4 {
			add("bufsize %d kbps out of range (max %d)", r.BufsizeKbps, MaxRenditionBitrate*4)
		}
	}

	// An unknown mode is refused here rather than at start time, because the
	// filter builder degrades it to a plain scale — which is a silently
	// different picture, and the operator would have no way to tell that the
	// mode they chose is not the one running.
	if r.AspectMode != "" {
		known := false
		for _, m := range ffmpeg.AspectModes {
			if string(m) == r.AspectMode {
				known = true
				break
			}
		}
		if !known {
			add("unknown aspect mode %q", r.AspectMode)
		}
	}
	// Aspect conversion resolves a mismatch between two known shapes. With one
	// axis free the scale already preserves aspect and the mode would do
	// nothing, so saying so beats saving a control that is quietly inert.
	if r.AspectMode != "" && (r.Width == 0 || r.Height == 0) {
		add("aspect mode %q needs both a width and a height; with one axis free there is no shape to convert", r.AspectMode)
	}
	if r.PadColor != "" && !presetTokenOK(r.PadColor) {
		// It lands on a filter graph, where a comma or a colon would end the
		// argument and start something else.
		add("pad colour %q must be a single word of letters, digits, '-', '_' or '.' (e.g. black, 0x101010)", r.PadColor)
	}

	// Refused here for exactly the reason an unknown aspect mode is: the filter
	// builder degrades an unrecognised mode to OFF, so the operator would get an
	// interlaced picture from a rendition whose stored setting says otherwise,
	// with nothing anywhere to tell them which one is running.
	if r.Deinterlace != "" {
		known := false
		for _, m := range ffmpeg.DeinterlaceModes {
			if string(m) == r.Deinterlace {
				known = true
				break
			}
		}
		if !known {
			add("unknown deinterlace mode %q", r.Deinterlace)
		}
	}

	for _, p := range r.Overlay.problems(r.Width, r.Height) {
		add("%s", p)
	}
	for _, p := range r.Text.problems(r.Width, r.Height) {
		add("%s", p)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid rendition: %s", strings.Join(probs, "; "))
	}
	return nil
}

// PresetDisclaimer is the exact wording the UI and the docs must show beside
// any preset. Platform ceilings move, and they differ by partner status; being
// confidently wrong about one breaks a live stream, so the presets are offered
// as editable starting points and say so.
const PresetDisclaimer = "Starting point — verify current limits with the platform."

// RenditionPreset is an editable starting point offered when creating a
// rendition. Passthrough is the odd one out: it is not a row at all, it is the
// absence of one.
type RenditionPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Passthrough marks the zero-cost default: no encode and no process. A
	// destination on it stores rendition_id NULL.
	Passthrough bool `json:"passthrough"`
	// Rendition is the template to seed the create form with, nil for
	// passthrough. ID and timestamps are zero.
	Rendition *Rendition `json:"rendition,omitempty"`
}

// RenditionPresets returns the starting points, most-capable first after
// passthrough. Values are conservative on purpose: where we were unsure we
// picked the lower number, because an under-spec stream is watchable and an
// over-spec one is rejected at the ingest.
func RenditionPresets() []RenditionPreset {
	tier := func(key, label string, w, h, fps, kbps int, note string) RenditionPreset {
		return RenditionPreset{
			Key:   key,
			Label: label,
			Rendition: &Rendition{
				Name:         label,
				Width:        w,
				Height:       h,
				FPS:          fps,
				VideoBitrate: kbps,
				Encoder:      EncoderX264,
				// veryfast is the live-encoding default everywhere, including
				// our own recorder: slower presets lose the race with realtime
				// on a machine that is already running several encodes.
				Preset:     "veryfast",
				GOPSeconds: 2,
				Note:       note + " " + PresetDisclaimer,
			},
		}
	}

	return []RenditionPreset{
		{
			Key:         "passthrough",
			Label:       "Source passthrough",
			Passthrough: true,
		},
		tier("1080p60", "1080p60 6000 kbps", 1920, 1080, 60, 6000,
			"Sends 1080p60 to destinations that will not take a 4K or high-bitrate source."),
		tier("1080p30", "1080p30 4500 kbps", 1920, 1080, 30, 4500,
			"Half the frame rate of the 1080p60 tier, for destinations or uplinks with less headroom."),
		tier("720p60", "720p60 4500 kbps", 1280, 720, 60, 4500,
			"Keeps motion smooth where bandwidth is the constraint; the usual choice for a constrained uplink."),
		tier("720p30", "720p30 3000 kbps", 1280, 720, 30, 3000,
			"The most conservative tier: use it when a destination keeps dropping frames on everything else."),
	}
}

func scanRendition(s interface{ Scan(...any) error }) (*Rendition, error) {
	var (
		r       Rendition
		source  sql.NullInt64
		created int64
		updated int64
	)
	err := s.Scan(&r.ID, &r.Name, &r.Width, &r.Height, &r.FPS, &r.VideoBitrate,
		&r.MaxrateKbps, &r.BufsizeKbps,
		&r.Encoder, &r.Preset, &r.GOPSeconds, &r.AspectMode, &r.PadColor,
		&r.Deinterlace, &r.Overlay.Image, &r.Overlay.Anchor, &r.Overlay.WidthPct,
		&r.Overlay.MarginXPct, &r.Overlay.MarginYPct, &r.Overlay.Opacity,
		&r.Text.Content, &r.Text.Font, &r.Text.Anchor, &r.Text.SizePct, &r.Text.Color,
		&r.Text.MarginXPct, &r.Text.MarginYPct, &r.Text.Box, &r.Text.BoxColor,
		&r.Text.BoxOpacity,
		&r.Note, &source, &created, &updated)
	if err != nil {
		return nil, err
	}
	if source.Valid {
		v := source.Int64
		r.SourceID = &v
	} else {
		// Same reasoning as destinations: a rendition with no source is
		// re-encoding nothing, and no reconciler will start it.
		return nil, fmt.Errorf("rendition %d has no source: it belongs to no "+
			"programme and would never be started", r.ID)
	}
	r.CreatedAt = time.Unix(created, 0)
	r.UpdatedAt = time.Unix(updated, 0)
	return &r, nil
}

const renditionColumns = `id, name, width, height, fps, video_bitrate,
	maxrate_kbps, bufsize_kbps,
	encoder, preset, gop_seconds, aspect_mode, pad_color, deinterlace,
	overlay_image, overlay_anchor, overlay_width_pct, overlay_margin_x_pct,
	overlay_margin_y_pct, overlay_opacity,
	text_content, text_font, text_anchor, text_size_pct, text_color,
	text_margin_x_pct, text_margin_y_pct, text_box, text_box_color,
	text_box_opacity,
	note, source_id, created_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	renditionBySourceQuery = `SELECT ` + renditionColumns + ` FROM renditions WHERE source_id = ? ORDER BY id`
	renditionListQuery     = `SELECT ` + renditionColumns + ` FROM renditions ORDER BY id`
	renditionByIDQuery     = `SELECT ` + renditionColumns + ` FROM renditions WHERE id = ?`
)

// applyRenditionDefaults fills in the fields an API payload is allowed to
// omit, so a create request can be as short as {"name","height","videoBitrate"}.
func (r *Rendition) applyDefaults() {
	if r.Encoder == "" {
		r.Encoder = EncoderX264
	}
	if r.Preset == "" {
		r.Preset = "veryfast"
	}
	if r.GOPSeconds == 0 {
		r.GOPSeconds = 2
	}
}

// ListRenditions returns every rendition, newest last.
// ListRenditionsBySource returns the renditions belonging to one source, which
// is what a per-source engine reconciles against.
func (d *DB) ListRenditionsBySource(sourceID int64) ([]*Rendition, error) {
	rows, err := d.sql.Query(
		renditionBySourceQuery, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Rendition{}
	for rows.Next() {
		r, err := scanRendition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) ListRenditions() ([]*Rendition, error) {
	rows, err := d.sql.Query(renditionListQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Rendition{}
	for rows.Next() {
		r, err := scanRendition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRendition loads one rendition.
func (d *DB) GetRendition(id int64) (*Rendition, error) {
	row := d.sql.QueryRow(renditionByIDQuery, id)
	r, err := scanRendition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// CreateRendition inserts a rendition, defaulting anything unset.
func (d *DB) CreateRendition(r *Rendition) (*Rendition, error) {
	r.applyDefaults()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	// Same reasoning as CreateDestination, including why it survives: no client
	// reaches this any more -- handleCreateRendition refuses a body with no
	// sourceId -- and it stays for our own test fixtures. A NULL here would
	// produce a rendition no reconciler ever starts, which looks to an operator
	// like a rendition that does nothing.
	if r.SourceID == nil {
		id, err := d.DefaultSourceID()
		if err != nil {
			return nil, fmt.Errorf("resolve default source: %w", err)
		}
		r.SourceID = &id
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO renditions
		(name, width, height, fps, video_bitrate, maxrate_kbps, bufsize_kbps, encoder, preset, gop_seconds,
		 aspect_mode, pad_color, deinterlace,
		 overlay_image, overlay_anchor, overlay_width_pct, overlay_margin_x_pct,
		 overlay_margin_y_pct, overlay_opacity,
		 text_content, text_font, text_anchor, text_size_pct, text_color,
		 text_margin_x_pct, text_margin_y_pct, text_box, text_box_color,
		 text_box_opacity,
		 note, source_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Width, r.Height, r.FPS, r.VideoBitrate,
		r.MaxrateKbps, r.BufsizeKbps,
		r.Encoder, r.Preset, r.GOPSeconds, r.AspectMode, r.PadColor, r.Deinterlace,
		r.Overlay.Image, r.Overlay.Anchor, r.Overlay.WidthPct,
		r.Overlay.MarginXPct, r.Overlay.MarginYPct, r.Overlay.Opacity,
		r.Text.Content, r.Text.Font, r.Text.Anchor, r.Text.SizePct, r.Text.Color,
		r.Text.MarginXPct, r.Text.MarginYPct, r.Text.Box, r.Text.BoxColor,
		r.Text.BoxOpacity,
		r.Note, r.SourceID, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetRendition(id)
}

// UpdateRendition replaces a rendition's mutable fields. The engine notices
// the changed row and restarts the encode; its destinations keep their own
// audio routing untouched.
func (d *DB) UpdateRendition(r *Rendition) (*Rendition, error) {
	r.applyDefaults()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE renditions SET
		name=?, width=?, height=?, fps=?, video_bitrate=?,
		maxrate_kbps=?, bufsize_kbps=?,
		encoder=?, preset=?, gop_seconds=?, aspect_mode=?, pad_color=?,
		deinterlace=?, overlay_image=?, overlay_anchor=?, overlay_width_pct=?,
		overlay_margin_x_pct=?, overlay_margin_y_pct=?, overlay_opacity=?,
		text_content=?, text_font=?, text_anchor=?, text_size_pct=?, text_color=?,
		text_margin_x_pct=?, text_margin_y_pct=?, text_box=?, text_box_color=?,
		text_box_opacity=?,
		note=?, source_id=?, updated_at=? WHERE id=?`,
		r.Name, r.Width, r.Height, r.FPS, r.VideoBitrate,
		r.MaxrateKbps, r.BufsizeKbps,
		r.Encoder, r.Preset, r.GOPSeconds, r.AspectMode, r.PadColor,
		r.Deinterlace, r.Overlay.Image, r.Overlay.Anchor, r.Overlay.WidthPct,
		r.Overlay.MarginXPct, r.Overlay.MarginYPct, r.Overlay.Opacity,
		r.Text.Content, r.Text.Font, r.Text.Anchor, r.Text.SizePct, r.Text.Color,
		r.Text.MarginXPct, r.Text.MarginYPct, r.Text.Box, r.Text.BoxColor,
		r.Text.BoxOpacity,
		r.Note, r.SourceID, time.Now().Unix(), r.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetRendition(r.ID)
}

// DeleteRendition removes a rendition. Its destinations are NOT deleted: the
// ON DELETE SET NULL on destinations.rendition_id drops them back to
// passthrough, so the user loses an encode tier and never an endpoint.
func (d *DB) DeleteRendition(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM renditions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountEnabledDestinationsByRendition returns, per rendition id, how many
// ENABLED destinations select it. This is the engine's ref count: a rendition
// reaching 1 starts an encode, a rendition reaching 0 stops one, and a
// rendition absent from the map must not be burning CPU.
//
// Passthrough destinations (rendition_id NULL) are deliberately not counted —
// they have no process to ref-count.
func (d *DB) CountEnabledDestinationsByRendition() (map[int64]int, error) {
	rows, err := d.sql.Query(`SELECT rendition_id, COUNT(*) FROM destinations
		WHERE enabled = 1 AND rendition_id IS NOT NULL GROUP BY rendition_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var (
			id int64
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// MigrateRenditions brings a database created before renditions existed up to
// date. It is idempotent and safe to call on every open.
//
// schema.sql only ever runs CREATE TABLE IF NOT EXISTS, which is a no-op
// against an existing destinations table, so the new rendition_id column has
// to be added here. SQLite has no ADD COLUMN IF NOT EXISTS, hence the
// table_info probe first; the ALTER itself is legal only because the column
// defaults to NULL, which is exactly what passthrough means. Existing rows
// therefore become passthrough destinations and keep behaving as they did.
func (d *DB) MigrateRenditions() error {
	has, err := columnExists(d.sql, "destinations", "rendition_id")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if !has {
		if _, err := d.sql.Exec(`ALTER TABLE destinations
			ADD COLUMN rendition_id INTEGER REFERENCES renditions(id) ON DELETE SET NULL`); err != nil {
			return fmt.Errorf("add destinations.rendition_id: %w", err)
		}
	}
	// Lives here rather than in schema.sql because schema.sql runs before the
	// column is guaranteed to exist, and a failed CREATE INDEX would abort the
	// whole script and stop the server from starting.
	if _, err := d.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_destinations_rendition
		ON destinations(rendition_id)`); err != nil {
		return fmt.Errorf("index destinations.rendition_id: %w", err)
	}
	return nil
}

// MigrateRenditionAspect adds the aspect-conversion columns to a database
// created before dual-format renditions existed.
//
// Both default to the empty string, which is the historical plain scale, so an
// upgraded install re-encodes exactly the frame it did yesterday until somebody
// picks a mode.
func (d *DB) MigrateRenditionAspect() error {
	for _, col := range []struct{ name, ddl string }{
		// RATE CONTROL. Both default to 0, which RenditionArgs already reads as
		// "derive the CBR relationship" -- maxrate = bitrate, bufsize = 2x
		// maxrate. So an upgraded install emits byte-for-byte the command line
		// it emitted yesterday, and these only do anything once somebody sets
		// them. See #341 and docs/ENCODING.md.
		{"maxrate_kbps", `ALTER TABLE renditions ADD COLUMN maxrate_kbps INTEGER NOT NULL DEFAULT 0`},
		{"bufsize_kbps", `ALTER TABLE renditions ADD COLUMN bufsize_kbps INTEGER NOT NULL DEFAULT 0`},
		{"aspect_mode", `ALTER TABLE renditions ADD COLUMN aspect_mode TEXT NOT NULL DEFAULT ''`},
		{"pad_color", `ALTER TABLE renditions ADD COLUMN pad_color TEXT NOT NULL DEFAULT ''`},
		{"deinterlace", `ALTER TABLE renditions ADD COLUMN deinterlace TEXT NOT NULL DEFAULT ''`},
		// The overlay columns. All default to the no-overlay value, so an
		// upgraded install re-encodes exactly the frame it did yesterday until
		// somebody picks an image.
		{"overlay_image", `ALTER TABLE renditions ADD COLUMN overlay_image TEXT NOT NULL DEFAULT ''`},
		{"overlay_anchor", `ALTER TABLE renditions ADD COLUMN overlay_anchor TEXT NOT NULL DEFAULT ''`},
		{"overlay_width_pct", `ALTER TABLE renditions ADD COLUMN overlay_width_pct REAL NOT NULL DEFAULT 0`},
		{"overlay_margin_x_pct", `ALTER TABLE renditions ADD COLUMN overlay_margin_x_pct REAL NOT NULL DEFAULT 0`},
		{"overlay_margin_y_pct", `ALTER TABLE renditions ADD COLUMN overlay_margin_y_pct REAL NOT NULL DEFAULT 0`},
		{"overlay_opacity", `ALTER TABLE renditions ADD COLUMN overlay_opacity REAL NOT NULL DEFAULT 0`},

		// The text columns. All default to the no-text value, so every
		// rendition that predates them keeps producing exactly the frame it
		// always did -- the same guarantee the overlay columns above carry.
		{"text_content", `ALTER TABLE renditions ADD COLUMN text_content TEXT NOT NULL DEFAULT ''`},
		{"text_font", `ALTER TABLE renditions ADD COLUMN text_font TEXT NOT NULL DEFAULT ''`},
		{"text_anchor", `ALTER TABLE renditions ADD COLUMN text_anchor TEXT NOT NULL DEFAULT ''`},
		{"text_size_pct", `ALTER TABLE renditions ADD COLUMN text_size_pct REAL NOT NULL DEFAULT 0`},
		{"text_color", `ALTER TABLE renditions ADD COLUMN text_color TEXT NOT NULL DEFAULT ''`},
		{"text_margin_x_pct", `ALTER TABLE renditions ADD COLUMN text_margin_x_pct REAL NOT NULL DEFAULT 0`},
		{"text_margin_y_pct", `ALTER TABLE renditions ADD COLUMN text_margin_y_pct REAL NOT NULL DEFAULT 0`},
		{"text_box", `ALTER TABLE renditions ADD COLUMN text_box INTEGER NOT NULL DEFAULT 0`},
		{"text_box_color", `ALTER TABLE renditions ADD COLUMN text_box_color TEXT NOT NULL DEFAULT ''`},
		{"text_box_opacity", `ALTER TABLE renditions ADD COLUMN text_box_opacity REAL NOT NULL DEFAULT 0`},
	} {
		has, err := columnExists(d.sql, "renditions", col.name)
		if err != nil {
			return fmt.Errorf("inspect renditions columns: %w", err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(col.ddl); err != nil {
			return fmt.Errorf("add renditions.%s: %w", col.name, err)
		}
	}
	return nil
}

func columnExists(sqldb *sql.DB, table, column string) (bool, error) {
	rows, err := sqldb.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}
