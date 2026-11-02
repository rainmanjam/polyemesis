package clipper

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A vertical export is a 9:16 short cut from a 16:9 recording: the same plan,
// the same job kind, the same exports directory and the same download route as
// every other clip -- only the last FFmpeg invocation differs. There is no
// second pipeline, and nothing here runs on its own.
//
// It is the one cut in this package that re-encodes EVERY frame, because a
// crop, a stack and a burned caption cannot be stream-copied. That changes the
// in-point arithmetic in a way worth stating: there is no keyframe to snap to,
// so the clip starts exactly where it was asked to and a caption timed against
// the recording lands on the frame it was spoken over. A fast-mode snap here
// would move the picture and leave the captions where they were.
//
// NO FACE TRACKING. The face-cam box is a fixed rectangle the operator placed on
// a preview frame. A box that follows a face is a model in the loop on every
// frame, and a short is fifteen seconds of a person sitting still in front of a
// camera that did not move either.

// Layout is how a 16:9 picture becomes a 9:16 one.
type Layout string

const (
	// LayoutCenter crops a 9:16 window out of the frame. Focus pans it.
	LayoutCenter Layout = "center"
	// LayoutStacked puts a face-cam box above (or below) a gameplay crop.
	LayoutStacked Layout = "stacked"
	// LayoutBlur fits the whole frame across the width over a blurred,
	// zoomed copy of itself.
	LayoutBlur Layout = "blur"
)

// Layouts is the catalogue, in the order to offer it.
func Layouts() []Layout { return []Layout{LayoutCenter, LayoutStacked, LayoutBlur} }

// The output every short platform takes: 1080x1920, H.264 in MP4 with AAC.
const (
	VerticalWidth  = 1080
	VerticalHeight = 1920
)

// ShortMaxDuration is the length YouTube Shorts, Reels and TikTok's own short
// format are all built around. It is a WARNING, not a ceiling: TikTok takes
// longer uploads and Shorts has moved its own limit before, so refusing a
// 75-second export would be this product deciding a platform rule for the
// operator. Plan.Warnings says it instead.
const ShortMaxDuration = 60 * time.Second

// DefaultFaceShare is the fraction of the output height the face-cam panel
// takes in LayoutStacked.
const DefaultFaceShare = 0.4

// Rect is a box on the SOURCE frame, every field a fraction of the frame's
// width or height (0..1). Fractions rather than pixels because the operator
// placed it on a preview that is not the master's resolution, and the master's
// resolution is not something the request is in a position to know.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// IsZero reports an unset box.
func (r Rect) IsZero() bool { return r == Rect{} }

// minRectSide is the smallest box worth cropping: 2% of a 1080p frame is 21
// pixels, below which the scale up to a 1080-wide panel is pure mush.
const minRectSide = 0.02

// rectSlack absorbs the float error a browser's pixel-to-fraction conversion
// leaves on a box dragged hard against the frame's edge.
const rectSlack = 1e-6

func (r Rect) validate(name string) error {
	for _, v := range []float64{r.X, r.Y, r.W, r.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: the %s box is not a number", ErrInvalidRequest, name)
		}
	}
	if r.X < 0 || r.Y < 0 {
		return fmt.Errorf("%w: the %s box starts outside the frame", ErrInvalidRequest, name)
	}
	if r.W < minRectSide || r.H < minRectSide {
		return fmt.Errorf("%w: the %s box is too small to crop", ErrInvalidRequest, name)
	}
	if r.X+r.W > 1+rectSlack || r.Y+r.H > 1+rectSlack {
		return fmt.Errorf("%w: the %s box runs off the frame", ErrInvalidRequest, name)
	}
	return nil
}

// Vertical is a 9:16 export of the clip a Request describes.
type Vertical struct {
	Layout Layout `json:"layout"`

	// Focus pans LayoutCenter's crop across the frame: 0 is the left edge, 1
	// the right, 0.5 the middle. Required rather than defaulted, because zero
	// is a legitimate answer ("keep the left of the frame") and a default would
	// make it impossible to ask for.
	Focus float64 `json:"focus"`

	// Face and Game are LayoutStacked's two boxes. A zero Game means the whole
	// frame, cropped to fit.
	Face Rect `json:"face,omitzero"`
	Game Rect `json:"game,omitzero"`
	// FaceShare is the face panel's fraction of the output height. Zero means
	// DefaultFaceShare.
	FaceShare float64 `json:"faceShare,omitempty"`
	// FaceBottom puts the face panel under the gameplay instead of over it.
	FaceBottom bool `json:"faceBottom,omitempty"`

	// Captions, when set, are burned in.
	Captions *Captions `json:"captions,omitempty"`
}

// Captions are the lines to burn into a vertical export, and how.
type Captions struct {
	// Cues are in TIMELINE milliseconds -- the same coordinates as Request.In
	// -- so a cue and the clip are rebased by one subtraction, in one place.
	Cues  []Cue        `json:"cues,omitempty"`
	Style CaptionStyle `json:"style"`
	// Tracks limits the captions to these audio tracks' transcripts; empty
	// means every track. It is what a JOB carries instead of Cues: a job's
	// params are readable by any token that may list jobs, and the words of
	// a transcript are not, so the worker reads them from the store when it
	// runs. See CaptionCues and WithTranscript.
	Tracks []int `json:"tracks,omitempty"`

	// FontFile is the absolute path of the font to draw with. The SERVER sets
	// it, from its own fonts directory; it is never taken from a request body.
	FontFile string `json:"fontFile,omitempty"`
	// DrawText reports whether this FFmpeg can draw text at all. A Homebrew
	// build has no drawtext filter; captions are then skipped with a warning
	// rather than failing the export, which still produces a usable short.
	DrawText bool `json:"drawText,omitempty"`
}

// Cue is one caption: a transcript segment, and its words when the transcriber
// recorded them.
type Cue struct {
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
	// Words, when present, time the caption word by word: chunks of
	// MaxWords are shown from their first word's start to their last word's
	// end. Absent, the cue's own span is used, which is what every transcript
	// this product stores today carries.
	Words []Word `json:"words,omitempty"`
}

// Word is one timed word inside a Cue, in timeline milliseconds.
type Word struct {
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
}

// CaptionStyle is how the captions look. Every field has a usable zero, filled
// by Normalized, so a template saved by an older build still renders.
type CaptionStyle struct {
	// Font is a font NAME from the fonts directory, not a path.
	Font string `json:"font,omitempty"`
	// SizePct is the glyph height as a percentage of the output height.
	SizePct float64 `json:"sizePct,omitempty"`
	// Color, OutlineColor and BoxColor are #rrggbb.
	Color        string `json:"color,omitempty"`
	OutlineColor string `json:"outlineColor,omitempty"`
	// Outline is the stroke width in pixels at 1080x1920. Zero means none.
	Outline int `json:"outline,omitempty"`
	// Box draws a translucent panel behind the text.
	Box        bool    `json:"box,omitempty"`
	BoxColor   string  `json:"boxColor,omitempty"`
	BoxOpacity float64 `json:"boxOpacity,omitempty"`
	// PositionPct is where the middle of the caption block sits, as a
	// percentage of the output height from the top.
	PositionPct float64 `json:"positionPct,omitempty"`
	// Uppercase shouts. Shorts captions often do.
	Uppercase bool `json:"uppercase,omitempty"`
	// MaxLines is how many wrapped lines one caption may take before the
	// rest moves to a second screen.
	MaxLines int `json:"maxLines,omitempty"`
	// MaxWords is how many timed words share the screen, when there are
	// timed words at all.
	MaxWords int `json:"maxWords,omitempty"`
}

// DefaultCaptionFont is the bold cut of the embedded Inter, because a caption
// is read on a phone over moving video.
const DefaultCaptionFont = "Inter-Bold.ttf"

// DefaultCaptionStyle is what an empty style renders as.
func DefaultCaptionStyle() CaptionStyle {
	return CaptionStyle{
		Font:         DefaultCaptionFont,
		SizePct:      4.2,
		Color:        "#ffffff",
		OutlineColor: "#000000",
		Outline:      6,
		BoxColor:     "#000000",
		BoxOpacity:   0.55,
		PositionPct:  72,
		MaxLines:     2,
		MaxWords:     4,
	}
}

// Normalized fills every unset field from DefaultCaptionStyle.
func (s CaptionStyle) Normalized() CaptionStyle {
	d := DefaultCaptionStyle()
	if strings.TrimSpace(s.Font) == "" {
		s.Font = d.Font
	}
	if s.SizePct == 0 {
		s.SizePct = d.SizePct
	}
	if s.Color == "" {
		s.Color = d.Color
	}
	if s.OutlineColor == "" {
		s.OutlineColor = d.OutlineColor
	}
	if s.BoxColor == "" {
		s.BoxColor = d.BoxColor
	}
	if s.BoxOpacity == 0 {
		s.BoxOpacity = d.BoxOpacity
	}
	if s.PositionPct == 0 {
		s.PositionPct = d.PositionPct
	}
	if s.MaxLines == 0 {
		s.MaxLines = d.MaxLines
	}
	if s.Outline == 0 {
		// Zero is "unset", like every other field here; -1 asks for none.
		s.Outline = d.Outline
	}
	if s.MaxWords == 0 {
		s.MaxWords = d.MaxWords
	}
	return s
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate checks a style on its own terms. Exported because a saved template
// is validated by the same rule before it is stored.
func (s CaptionStyle) Validate() error {
	n := s.Normalized()
	if strings.ContainsAny(n.Font, `/\`) || n.Font == "." || n.Font == ".." {
		return fmt.Errorf("%w: caption font %q is not a font name", ErrInvalidRequest, n.Font)
	}
	if n.SizePct < 1.5 || n.SizePct > 12 {
		return fmt.Errorf("%w: caption size %.1f%% is outside 1.5..12", ErrInvalidRequest, n.SizePct)
	}
	for name, c := range map[string]string{"color": n.Color, "outline color": n.OutlineColor, "box color": n.BoxColor} {
		if !hexColor.MatchString(c) {
			return fmt.Errorf("%w: caption %s %q is not #rrggbb", ErrInvalidRequest, name, c)
		}
	}
	if n.Outline < -1 || n.Outline > 20 {
		return fmt.Errorf("%w: caption outline %d is outside -1..20", ErrInvalidRequest, n.Outline)
	}
	if n.BoxOpacity < 0 || n.BoxOpacity > 1 {
		return fmt.Errorf("%w: caption box opacity is outside 0..1", ErrInvalidRequest)
	}
	if n.PositionPct < 5 || n.PositionPct > 95 {
		return fmt.Errorf("%w: caption position %.0f%% is outside 5..95", ErrInvalidRequest, n.PositionPct)
	}
	if n.MaxLines < 1 || n.MaxLines > 4 {
		return fmt.Errorf("%w: caption lines %d is outside 1..4", ErrInvalidRequest, n.MaxLines)
	}
	if n.MaxWords < 1 || n.MaxWords > 12 {
		return fmt.Errorf("%w: caption words %d is outside 1..12", ErrInvalidRequest, n.MaxWords)
	}
	return nil
}

// ValidateLayout checks everything about a Vertical except its captions' cues,
// which belong to one clip and are not part of a saved template.
func (v Vertical) ValidateLayout() error {
	switch v.Layout {
	case LayoutCenter, LayoutBlur:
	case LayoutStacked:
		if err := v.Face.validate("face-cam"); err != nil {
			return err
		}
		if !v.Game.IsZero() {
			if err := v.Game.validate("gameplay"); err != nil {
				return err
			}
		}
		if v.FaceShare != 0 && (v.FaceShare < 0.2 || v.FaceShare > 0.6) {
			return fmt.Errorf("%w: the face panel's share %.2f is outside 0.2..0.6", ErrInvalidRequest, v.FaceShare)
		}
	default:
		return fmt.Errorf("%w: unknown vertical layout %q", ErrInvalidRequest, v.Layout)
	}
	if math.IsNaN(v.Focus) || v.Focus < 0 || v.Focus > 1 {
		return fmt.Errorf("%w: the crop focus is outside 0..1", ErrInvalidRequest)
	}
	if v.Captions != nil {
		return v.Captions.Style.Validate()
	}
	return nil
}

// maxCues bounds a request's caption list. A minute of speech is a few dozen
// segments; thousands means somebody sent the whole recording's transcript.
const maxCues = 2000

func (v Vertical) validate(r Request) error {
	if err := v.ValidateLayout(); err != nil {
		return err
	}
	if containerFor(r.OutPath) != ContainerMP4 {
		return fmt.Errorf("%w: a vertical export is an MP4", ErrInvalidRequest)
	}
	if r.Audio.Mode == AudioMix {
		// The mix graph and the picture graph would both want -filter_complex,
		// and a short carries one stereo track anyway.
		return fmt.Errorf("%w: a vertical export takes tracks, not a routing mix", ErrInvalidRequest)
	}
	if v.Captions != nil && len(v.Captions.Cues) > maxCues {
		return fmt.Errorf("%w: %d caption cues is more than one clip can carry", ErrInvalidRequest, len(v.Captions.Cues))
	}
	return nil
}

// faceHeight is the face panel's height in output pixels, always even so the
// 4:2:0 encode never has to round a panel boundary.
func (v Vertical) faceHeight() int {
	share := v.FaceShare
	if share == 0 {
		share = DefaultFaceShare
	}
	h := int(math.Round(float64(VerticalHeight) * share))
	return h &^ 1
}

// ---------------------------------------------------------------- the graph

// num renders a geometry fraction for a filter expression. Six decimals is
// sub-pixel at 8K, and a fixed width keeps the graph diffable.
func num(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// cropTo is the crop that cuts box r out of the input, in the input's own
// pixels: iw and ih are the source's width and height when the filter runs, so
// a box placed on a 640-wide preview lands in the same place on a 3840-wide
// master.
//
// Width and height are floored to an EVEN number of at least two pixels. A box
// on a tiny or odd-sized master would otherwise round to an odd size, which a
// 4:2:0 crop cannot cut, or to zero, which crop refuses outright -- measured on
// a 35x19 source, where a 2% box is 0.7 x 0.38 pixels. The expressions are
// quoted because their commas would otherwise split the graph.
func cropTo(r Rect) string {
	return fmt.Sprintf("crop=w='max(2,trunc(iw*%s/2)*2)':h='max(2,trunc(ih*%s/2)*2)':x=iw*%s:y=ih*%s",
		num(r.W), num(r.H), num(r.X), num(r.Y))
}

// cover scales to fill w x h and trims the overhang, centred. It is the
// fallback when a box's aspect is not the panel's: the middle of the box is
// kept, and nothing is ever letterboxed inside a panel.
func cover(w, h int) string {
	return fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=increase,crop=w=%d:h=%d", w, h, w, h)
}

// pictureGraph is the layout half of the filtergraph: [0:v:0] in, [base] out.
func (v Vertical) pictureGraph() string {
	W, H := VerticalWidth, VerticalHeight
	switch v.Layout {
	case LayoutStacked:
		fh := v.faceHeight()
		gh := H - fh
		game := ""
		if !v.Game.IsZero() {
			game = cropTo(v.Game) + ","
		}
		top, bottom := "[face]", "[game]"
		if v.FaceBottom {
			top, bottom = bottom, top
		}
		return "[0:v:0]split=2[fa][ga];" +
			"[fa]" + cropTo(v.Face) + "," + cover(W, fh) + "[face];" +
			"[ga]" + game + cover(W, gh) + "[game];" +
			top + bottom + "vstack=inputs=2[base]"
	case LayoutBlur:
		// The background is blurred at half size and scaled back up: a box
		// blur's cost is per pixel, and at a quarter of the pixels it is still
		// unreadable, which is all a background has to be.
		return fmt.Sprintf("[0:v:0]split=2[bg][fg];"+
			"[bg]%s,boxblur=luma_radius=20:luma_power=2,scale=w=%d:h=%d[bgb];"+
			"[fg]scale=w=%d:h=%d:force_original_aspect_ratio=decrease[fgs];"+
			"[bgb][fgs]overlay=x=(W-w)/2:y=(H-h)/2[base]",
			cover(W/2, H/2), W, H, W, H)
	default:
		return fmt.Sprintf("[0:v:0]scale=w=%d:h=%d:force_original_aspect_ratio=increase,"+
			"crop=w=%d:h=%d:x=(iw-ow)*%s:y=(ih-oh)/2[base]", W, H, W, H, num(v.Focus))
	}
}

// ---------------------------------------------------------------- captions

// captionScreen is one caption as it will be drawn: wrapped lines, shown over
// [From, To) in OUTPUT seconds -- the clock the drawtext filter reads.
type captionScreen struct {
	From, To time.Duration
	Lines    []string
}

// maxCaptionLines bounds how many drawtext filters one short's graph carries.
const maxCaptionLines = 600

// minScreen drops a caption that would be on screen for less than a few
// frames: it is a flicker, not a caption.
const minScreen = 100 * time.Millisecond

// screens turns the cues into what is drawn, on the FILTERS' clock.
//
// clock maps a timeline position to that clock (see Plan.playback), and
// [winFrom, winTo) is the short's own span on it: a caption is clamped to the
// picture the short actually contains. A cue spoken while the recorder was
// stopped maps to a single instant and is dropped -- there is no picture under
// it to draw on.
func screens(c Captions, style CaptionStyle, clock func(time.Duration) time.Duration, winFrom, winTo time.Duration) []captionScreen {
	perLine := charsPerLine(style)
	var out []captionScreen
	add := func(from, to time.Duration, text string) {
		from, to = clock(from), clock(to)
		if from < winFrom {
			from = winFrom
		}
		if to > winTo {
			to = winTo
		}
		if to-from < minScreen {
			return
		}
		lines := wrap(text, perLine)
		if len(lines) == 0 {
			return
		}
		out = append(out, pages(from, to, lines, style.MaxLines)...)
	}
	for _, cue := range c.Cues {
		if len(cue.Words) > 0 {
			for _, ch := range wordChunks(cue.Words, style.MaxWords) {
				add(ch.from, ch.to, styleText(ch.text, style))
			}
			continue
		}
		add(ms(cue.StartMS), ms(cue.EndMS), styleText(cue.Text, style))
	}
	return out
}

func ms(v int64) time.Duration { return time.Duration(v) * time.Millisecond }

type chunk struct {
	from, to time.Duration
	text     string
}

// wordChunks groups timed words max at a time. Each chunk is shown from its
// first word's start to its last word's end: the timing is the transcriber's,
// not an estimate.
func wordChunks(words []Word, max int) []chunk {
	var out []chunk
	for i := 0; i < len(words); i += max {
		end := min(i+max, len(words))
		var parts []string
		for _, w := range words[i:end] {
			if t := strings.TrimSpace(w.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, chunk{from: ms(words[i].StartMS), to: ms(words[end-1].EndMS), text: strings.Join(parts, " ")})
	}
	return out
}

// pages splits a cue that wraps to more lines than fit into consecutive
// screens, dividing the cue's span between them by how much text each holds.
//
// This is the one place a time is not the transcriber's, and it is bounded by
// the cue it came from: no page starts before its cue or ends after it. The
// alternative -- a six-line caption over the picture -- is not a caption.
func pages(from, to time.Duration, lines []string, maxLines int) []captionScreen {
	if len(lines) <= maxLines {
		return []captionScreen{{From: from, To: to, Lines: lines}}
	}
	var groups [][]string
	total := 0
	for i := 0; i < len(lines); i += maxLines {
		g := lines[i:min(i+maxLines, len(lines))]
		groups = append(groups, g)
		total += textLen(g)
	}
	out := make([]captionScreen, 0, len(groups))
	span := to - from
	at := from
	acc := 0
	for i, g := range groups {
		acc += textLen(g)
		end := from + time.Duration(float64(span)*float64(acc)/float64(total))
		if i == len(groups)-1 {
			end = to
		}
		if end-at >= minScreen {
			out = append(out, captionScreen{From: at, To: end, Lines: g})
		}
		at = end
	}
	return out
}

func textLen(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len([]rune(l))
	}
	if n == 0 {
		return 1
	}
	return n
}

// styleText cleans a transcript line for drawing: control characters and the
// search-highlight markers the transcript store uses are dropped, runs of
// space collapse, and Uppercase is applied.
func styleText(s string, style CaptionStyle) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\ue000', r == '\ue001':
			continue
		case unicode.IsControl(r), unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if style.Uppercase {
		out = strings.ToUpper(out)
	}
	return out
}

// avgGlyphWidth is Inter Bold's mean advance as a fraction of the font size,
// measured over English text. Wrapping on it rather than measuring the font
// keeps this pure; the margin below absorbs the difference for wide words.
const avgGlyphWidth = 0.56

// fontPx is the glyph height in output pixels.
func fontPx(style CaptionStyle) int {
	return max(12, int(math.Round(float64(VerticalHeight)*style.SizePct/100)))
}

// charsPerLine is how many average characters fit across 88% of the width.
func charsPerLine(style CaptionStyle) int {
	return max(8, int(0.88*float64(VerticalWidth)/(avgGlyphWidth*float64(fontPx(style)))))
}

// wrap breaks text into lines of at most width characters, on spaces, and
// between characters inside a word longer than a line.
func wrap(text string, width int) []string {
	var lines []string
	var cur []rune
	for _, word := range strings.Fields(text) {
		w := []rune(word)
		// A word longer than a line -- which is every line of a script
		// written without spaces -- is broken between characters. Kept
		// whole, a Chinese caption was one line thousands of pixels wide.
		for len(w) > width {
			if len(cur) > 0 {
				lines = append(lines, string(cur))
				cur = nil
			}
			lines = append(lines, string(w[:width]))
			w = w[width:]
		}
		if len(w) == 0 {
			continue
		}
		switch {
		case len(cur) == 0:
			cur = append(cur, w...)
		case len(cur)+1+len(w) <= width:
			cur = append(append(cur, ' '), w...)
		default:
			lines = append(lines, string(cur))
			cur = append([]rune(nil), w...)
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

// lineHeight is the spacing between wrapped lines, in output pixels.
func lineHeight(style CaptionStyle) int { return int(math.Round(float64(fontPx(style)) * 1.25)) }

// secsExpr renders an output time for an enable expression.
func secsExpr(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

// ffColor turns #rrggbb into the 0xrrggbb FFmpeg's colour parser takes, which
// needs no escaping in a filter argument.
func ffColor(hex string) string { return "0x" + strings.ToLower(strings.TrimPrefix(hex, "#")) }

// captionFilters renders one drawtext per wrapped line of every screen, each
// reading its line from a sidecar file. A textfile rather than text= because a
// transcript is free text, and the only escaping that cannot be got wrong is
// none at all; expansion=none for the reason internal/ffmpeg's drawtext gives
// -- a '%' in what somebody said must not be read as a template.
//
// Captions on screen at the same time -- two tracks talking over each other --
// are stacked: each takes the lowest LANE free for its whole span, and every
// lane sits one full caption block above the one below it. No more than
// maxCaptionLines lines are drawn; see captionLineCount for the warning.
func captionFilters(scr []captionScreen, style CaptionStyle, font, workDir string) (filters []string, files []SidecarFile) {
	size := fontPx(style)
	lh := lineHeight(style)
	centre := int(math.Round(float64(VerticalHeight) * style.PositionPct / 100))
	laneStep := lh*style.MaxLines + lh/2
	var laneEnds []time.Duration
	for i, s := range scr {
		lane := 0
		for lane < len(laneEnds) && laneEnds[lane] > s.From {
			lane++
		}
		if lane == len(laneEnds) {
			laneEnds = append(laneEnds, 0)
		}
		laneEnds[lane] = s.To
		top := centre - (lh*len(s.Lines))/2 - lane*laneStep
		for j, line := range s.Lines {
			if len(filters) >= maxCaptionLines {
				return filters, files
			}
			path := filepath.Join(workDir, fmt.Sprintf("caption-%04d-%d.txt", i, j))
			files = append(files, SidecarFile{Path: path, Content: line})
			var b strings.Builder
			b.WriteString("drawtext=fontfile=")
			b.WriteString(ffmpeg.GraphArgPath(font))
			b.WriteString(":textfile=")
			b.WriteString(ffmpeg.GraphArgPath(path))
			fmt.Fprintf(&b, ":fontsize=%d:fontcolor=%s:x=(w-text_w)/2:y=%d", size, ffColor(style.Color), top+j*lh)
			if style.Outline > 0 { // -1 is "none"; Normalized turns 0 into the default
				fmt.Fprintf(&b, ":borderw=%d:bordercolor=%s", style.Outline, ffColor(style.OutlineColor))
			}
			if style.Box {
				fmt.Fprintf(&b, ":box=1:boxcolor=%s@%s:boxborderw=%d",
					ffColor(style.BoxColor), strconv.FormatFloat(style.BoxOpacity, 'f', 2, 64), max(4, size/4))
			}
			fmt.Fprintf(&b, ":expansion=none:enable='gte(t,%s)*lt(t,%s)'", secsExpr(s.From), secsExpr(s.To))
			filters = append(filters, b.String())
		}
	}
	return filters, files
}

// ------------------------------------------------------------ the command

// ErrNoCaptionFont is a captioned export the server did not resolve a font
// for. It is the server's fault, not the request's, so it does not wrap
// ErrInvalidRequest.
var ErrNoCaptionFont = errors.New("clipper: captions need a font file and none was resolved")

// verticalEncoder is what encodes a short. Every short platform wants H.264,
// so an encoder of another family is replaced rather than obeyed.
func verticalEncoder(name string) string {
	if strings.Contains(name, "264") {
		return name
	}
	return FallbackHeadEncoder
}

// verticalAudio maps the one stereo track a short carries: the first track for
// "all", the chosen track, or a mix of the chosen tracks.
func (p Plan) verticalAudio() (graph string, maps []string) {
	if p.Audio.Mode == AudioTracks && len(p.Audio.Tracks) > 1 {
		var b strings.Builder
		for _, t := range p.Audio.Tracks {
			fmt.Fprintf(&b, "[0:a:%d]", t)
		}
		fmt.Fprintf(&b, "amix=inputs=%d:duration=longest:normalize=0[aout]", len(p.Audio.Tracks))
		return b.String(), []string{"-map", "[aout]"}
	}
	track := 0
	if p.Audio.Mode == AudioTracks && len(p.Audio.Tracks) == 1 {
		track = p.Audio.Tracks[0]
	}
	// Optional, like the rest of this package's mapping: a recording without
	// audio still makes a (silent) short.
	return "", []string{"-map", "0:a:" + strconv.Itoa(track) + "?"}
}

// verticalCommand is the single full encode a vertical export is.
//
// The filtergraph goes to FFmpeg as a SIDECAR FILE (-/filter_complex, FFmpeg
// 7.1+), not as an argument: a captioned minute is dozens of drawtext filters,
// each carrying two absolute paths, and Windows caps a command line at 32K
// characters.
func (p Plan) verticalCommand(in input, workDir string) (Command, error) {
	v := p.Vertical
	pre, post := seekArgs(p.Concat, p.HeadSeek, 0)

	graph := v.pictureGraph() + ";[base]setsar=1,format=yuv420p"
	var files []SidecarFile
	if c := v.Captions; c != nil && c.DrawText && len(c.Cues) > 0 {
		if c.FontFile == "" {
			return Command{}, ErrNoCaptionFont
		}
		style := c.Style.Normalized()
		filters, capFiles := captionFilters(screens(*c, style, p.playback, p.playback(p.In), p.playback(p.Out)), style, c.FontFile, workDir)
		for _, f := range filters {
			graph += "," + f
		}
		files = capFiles
	}
	graph += "[vout]"
	audioGraph, audioMap := p.verticalAudio()
	if audioGraph != "" {
		graph += ";" + audioGraph
	}
	graphFile := SidecarFile{Path: filepath.Join(workDir, "vertical.graph"), Content: graph}
	files = append(append(files, in.file...), graphFile)

	enc := verticalEncoder(p.VideoEncoder)
	args := baseArgs()
	args = append(args, pre...)
	args = append(args, in.args...)
	args = append(args, post...)
	// The picture the short contains, which is not Out-In when the recording
	// has a gap: the concat demuxer plays the files back to back.
	args = append(args, "-t", secs(p.playback(p.Out)-p.playback(p.In)))
	args = append(args, "-/filter_complex", graphFile.Path, "-map", "[vout]")
	args = append(args, audioMap...)
	args = append(args, "-c:v", enc)
	switch enc {
	case "libx264":
		args = append(args, "-preset", "veryfast", "-crf", "20", "-profile:v", "high")
	default:
		args = append(args, "-b:v", "8M")
	}
	// 60 is every short platform's ceiling; a 120fps capture is halved, a
	// 30fps one is left alone.
	args = append(args, "-pix_fmt", "yuv420p", "-fpsmax", "60")
	if p.HeadThreads > 0 {
		args = append(args, "-threads", strconv.Itoa(p.HeadThreads))
	}
	args = append(args, "-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2")
	args = append(args, p.outputArgs(ContainerMP4, true)...)
	args = append(args, p.OutPath)
	return Command{Name: "vertical", Args: args, Output: p.OutPath, Files: files}, nil
}

// warnCaptions says what the burned captions will not show: lines past the
// graph's bound, and characters the bundled font has no glyph for. It runs
// after resolveSources, because placing a caption needs the files.
func (p *Plan) warnCaptions() {
	c := p.Vertical.Captions
	if c == nil || !c.DrawText || len(c.Cues) == 0 {
		return
	}
	style := c.Style.Normalized()
	lines := 0
	for _, s := range screens(*c, style, p.playback, p.playback(p.In), p.playback(p.Out)) {
		lines += len(s.Lines)
	}
	if lines > maxCaptionLines {
		p.warn("%d caption lines were not drawn: one short carries at most %d", lines-maxCaptionLines, maxCaptionLines)
	}
	if isBuiltinFont(style.Font) {
		for _, cue := range c.Cues {
			if r, ok := firstUndrawable(cue.Text); ok {
				p.warn("the bundled Inter font has no glyph for %q and characters like it, so those captions "+
					"will draw as boxes; add a font that has them to the fonts directory and choose it", string(r))
				return
			}
		}
	}
}

func isBuiltinFont(name string) bool {
	for _, b := range ffmpeg.BuiltinFonts {
		if name == b {
			return true
		}
	}
	return false
}

// interRanges are the Unicode blocks the bundled Inter draws: Latin with its
// extensions and combining marks, Greek, Cyrillic, and the punctuation, currency
// and symbol blocks around them. Anything else -- CJK, Arabic, Hebrew,
// Devanagari, Thai, emoji -- has no glyph and renders as a box.
var interRanges = [][2]rune{
	{0x0000, 0x036F}, {0x0370, 0x03FF}, {0x0400, 0x052F}, {0x1E00, 0x1FFF},
	{0x2000, 0x206F}, {0x20A0, 0x20CF}, {0x2100, 0x22FF},
}

// firstUndrawable is the first character of s the bundled font cannot draw.
func firstUndrawable(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ue000' || r == '\ue001' {
			continue
		}
		in := false
		for _, rg := range interRanges {
			if r >= rg[0] && r <= rg[1] {
				in = true
				break
			}
		}
		if !in {
			return r, true
		}
	}
	return 0, false
}

// playback maps a timeline position onto the clock the filtergraph sees.
//
// A short cut from one file is seeked on the INPUT, so its frames reach the
// filters starting at zero: the clock is t - In. A short spanning several files
// is read through the concat demuxer, which cannot seek and plays the files
// BACK TO BACK, so the clock is the position in that joined input -- the
// lengths of the earlier files plus the offset into this one. A gap in the
// recording takes no time on it, which is why a caption after a recorder
// restart has to be mapped through the files rather than by subtraction.
func (p Plan) playback(t time.Duration) time.Duration {
	if !p.Concat {
		return t - p.In
	}
	var played time.Duration
	for _, s := range p.Sources {
		if t < s.Start {
			return played
		}
		if t < s.End() {
			return played + (t - s.Start)
		}
		played += s.Duration
	}
	return played
}

// planVertical is PlanCut's branch for a vertical export: no keyframe snap,
// every frame encoded, the in-point exactly where it was asked for.
func (p *Plan) planVertical(v *Vertical) {
	p.Vertical = v
	p.Mode = ModePrecise
	p.DriftKnown = true
	p.InDrift = 0
	p.HeadDuration = p.Out - p.In
	p.HeadSeek = p.In
	p.HeadTrim = 0
	p.TailDuration = 0
	if p.Duration() > ShortMaxDuration {
		p.warn("this short is %s long; YouTube Shorts, Reels and TikTok's short format expect "+
			"%s or less, and a longer upload may be published as a regular video", round(p.Duration()), ShortMaxDuration)
	}
	if c := v.Captions; c != nil && len(c.Cues) > 0 && !c.DrawText {
		p.warn("this FFmpeg cannot draw text (it was built without libfreetype), so the captions " +
			"were not burned in; the short is otherwise complete")
	}
}

// ------------------------------------------------------- captions at run time

// TranscriptLine is one transcribed line in its OWN recording's milliseconds.
type TranscriptLine struct {
	Track   int
	StartMS int64
	EndMS   int64
	Text    string
}

// TranscriptSource reads one recording's transcript. A recording with none
// answers nil, nil.
type TranscriptSource func(recordingID int64) ([]TranscriptLine, error)

// CaptionCues is the captions a short of [in, out) carries: every segment's
// transcript rebased onto the timeline (the segment's Start plus the line's own
// milliseconds -- the same rebase the editor's transcript panel draws with),
// kept when it overlaps the clip and, when tracks is non-empty, when it was
// spoken on one of them.
//
// The ONE place a short's words are gathered, at plan time by the API and at
// run time by the worker, so the preview and the render cannot disagree about
// what is captioned.
func CaptionCues(segs []Segment, src TranscriptSource, in, out time.Duration, tracks []int) ([]Cue, error) {
	only := map[int]bool{}
	for _, t := range tracks {
		only[t] = true
	}
	var cues []Cue
	seen := map[int64]bool{}
	for _, seg := range segs {
		if seg.RecordingID == 0 || seen[seg.RecordingID] || src == nil {
			continue
		}
		seen[seg.RecordingID] = true
		lines, err := src(seg.RecordingID)
		if err != nil {
			return nil, err
		}
		base := seg.Start.Milliseconds()
		for _, l := range lines {
			start, end := base+l.StartMS, base+l.EndMS
			if end <= in.Milliseconds() || start >= out.Milliseconds() {
				continue
			}
			if len(only) > 0 && !only[l.Track] {
				continue
			}
			cues = append(cues, Cue{StartMS: start, EndMS: end, Text: l.Text})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].StartMS < cues[j].StartMS })
	return cues, nil
}

// WithoutCues is a copy of v whose captions carry no words, for storing in a
// job: what a job's params hold is readable by any token allowed to list jobs.
func (v *Vertical) WithoutCues() *Vertical {
	if v == nil || v.Captions == nil {
		return v
	}
	out := *v
	c := *v.Captions
	c.Cues = nil
	out.Captions = &c
	return &out
}
