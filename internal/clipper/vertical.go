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

// colorBlack is the default outline, box and back colour.
const colorBlack = "#000000"

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
	// Burn reports whether this FFmpeg can burn captions at all: the libass
	// subtitles filter. A Homebrew
	// build has no drawtext filter; captions are then skipped with a warning
	// rather than failing the export, which still produces a usable short.
	Burn bool `json:"burn,omitempty"`
}

// Cue is one caption: a transcript segment, and its words when the transcriber
// recorded them.
type Cue struct {
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
	// Track is the audio track the cue was spoken on. Cues on one track are
	// one speaker's lines and follow each other; cues on different tracks
	// may truly overlap, and are stacked.
	Track int `json:"track,omitempty"`
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
		OutlineColor: colorBlack,
		Outline:      6,
		BoxColor:     colorBlack,
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
	ends := followingStarts(c.Cues)
	for i, cue := range c.Cues {
		end := ms(min(cue.EndMS, ends[i]))
		if len(cue.Words) > 0 {
			for _, ch := range wordChunks(cue.Words, style.MaxWords) {
				add(ch.from, min(ch.to, end), styleText(ch.text, style))
			}
			continue
		}
		add(ms(cue.StartMS), end, styleText(cue.Text, style))
	}
	return out
}

// followingStarts is, for each cue, the start of the next cue on the SAME
// track -- where it has to give way -- or its own end when none follows. A
// transcriber's adjacent lines often overlap by a few milliseconds; drawn as
// they come, libass would stack the next line above the last for its whole
// life, and a run of them would bounce. Cues on different tracks are
// different speakers and keep their overlap, so they stack.
func followingStarts(cues []Cue) []int64 {
	order := make([]int, len(cues))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cues[order[a]].StartMS < cues[order[b]].StartMS })
	ends := make([]int64, len(cues))
	next := map[int]int64{} // track -> start of the latest-starting cue seen so far
	for k := len(order) - 1; k >= 0; k-- {
		i := order[k]
		cue := cues[i]
		ends[i] = cue.EndMS
		if n, ok := next[cue.Track]; ok && n > cue.StartMS && n < ends[i] {
			ends[i] = n
		}
		if n, ok := next[cue.Track]; !ok || cue.StartMS < n {
			next[cue.Track] = cue.StartMS
		}
	}
	return ends
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

// maxCaptionEvents bounds how many captions one short's subtitle script
// carries. A minute of speech is a few dozen; thousands is a request carrying a
// whole recording's transcript, and libass then spends its memory on events
// nobody will see.
const maxCaptionEvents = 2000

// assTime renders a duration as an ASS timestamp, H:MM:SS.cc.
func assTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	cs := int64(d / (10 * time.Millisecond))
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

// assColour renders #rrggbb with an opacity as ASS's &HAABBGGRR, where alpha
// 00 is opaque and FF transparent.
func assColour(hex string, opacity float64) string {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		h = "ffffff"
	}
	a := int(math.Round((1 - math.Max(0, math.Min(1, opacity))) * 255))
	return fmt.Sprintf("&H%02X%s%s%s", a, strings.ToUpper(h[4:6]), strings.ToUpper(h[2:4]), strings.ToUpper(h[0:2]))
}

// wordJoiner is U+2060: zero-width, unbreaking, and enough to keep libass from
// reading a backslash in what somebody said as the start of \N or \h.
const wordJoiner = "\u2060"

// assEscaper makes transcript text inert in an ASS Dialogue line: every
// backslash is followed by a word joiner (so "\N" in speech is not a line
// break), and braces -- which open an override block -- are escaped the way
// libass reads as literal. Measured against libass in the shipping image:
// "{\fs300}BIG" renders as those characters, at the caption size.
var assEscaper = strings.NewReplacer(`\`, `\`+wordJoiner, `{`, `\{`, `}`, `\}`)

// captionPlacement is where a caption block sits: its alignment and vertical
// margin in the 1080x1920 script. Above the middle the block hangs from the top
// at PositionPct, so concurrent captions stack DOWNWARDS; below it the block
// stands on the bottom, so they stack upwards. Either way a stack grows away
// from the nearer edge. libass keeps an ordinary stack in the frame; under
// heavy overload -- more concurrent captions than the frame has lines -- it
// can still push lines off the far edge, where they are lost.
func captionPlacement(style CaptionStyle) (alignment, marginV int) {
	half := fontPx(style) / 2
	centre := int(math.Round(float64(VerticalHeight) * style.PositionPct / 100))
	if style.PositionPct < 50 {
		return 8, max(0, centre-half)
	}
	return 2, max(0, VerticalHeight-centre-half)
}

// assScript is the subtitle script a short's captions are burned from.
//
// libass draws it, through FFmpeg's subtitles filter. That is what gives the
// captions everything the hand-rolled drawtext chain did not have: shaping for
// right-to-left and complex scripts, a FALLBACK FONT from fontconfig for any
// glyph the chosen font lacks, concurrent captions stacked rather than drawn
// over each other, and text kept inside the frame at any position.
//
// The style is the template's: the chosen font's family and weight, size,
// colours, outline -- or an opaque box -- and position. Each screen is one
// event; its wrapped lines are joined with \N so a long cue still pages the
// way MaxLines asks, and libass wraps further if a line is still too wide.
func assScript(scr []captionScreen, style CaptionStyle, family string, bold bool) string {
	size := fontPx(style)
	align, marginV := captionPlacement(style)
	marginH := VerticalWidth * 6 / 100
	boldFlag := 0
	if bold {
		boldFlag = -1
	}
	borderStyle, outline := 1, 0
	outlineColour, backColour := assColour(style.OutlineColor, 1), assColour(colorBlack, 0)
	if style.Outline > 0 {
		outline = style.Outline
	}
	if style.Box {
		// An opaque box behind the text: libass draws it in the outline
		// colour, with the outline width as its padding.
		borderStyle, outline = 3, max(4, size/4)
		outlineColour = assColour(style.BoxColor, style.BoxOpacity)
		backColour = outlineColour
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 0\nScaledBorderAndShadow: yes\n\n",
		VerticalWidth, VerticalHeight)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, " +
		"Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, " +
		"MarginL, MarginR, MarginV, Encoding\n")
	// Encoding -1: libass takes each event's base direction from its own
	// text, so a Hebrew or Arabic sentence ends at the left, punctuation
	// included. 1 (the usual default) would force a left-to-right paragraph.
	fmt.Fprintf(&b, "Style: Caption,%s,%d,%s,&H000000FF,%s,%s,%d,0,0,0,100,100,0,0,%d,%d,0,%d,%d,%d,%d,-1\n\n",
		strings.NewReplacer(",", " ").Replace(family), size, assColour(style.Color, 1), outlineColour, backColour,
		boldFlag, borderStyle, outline, align, marginH, marginH, marginV)
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for i, sc := range scr {
		if i >= maxCaptionEvents {
			break
		}
		lines := make([]string, len(sc.Lines))
		for j, l := range sc.Lines {
			lines[j] = assEscaper.Replace(l)
		}
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Caption,,0,0,0,,%s\n", assTime(sc.From), assTime(sc.To), strings.Join(lines, `\N`))
	}
	return b.String()
}

// captionFamily is the family libass should select for a font file, and its
// weight: read from the file, or -- for a file that cannot be read -- guessed
// from the name, so a missing file costs the exact face rather than the
// captions.
func captionFamily(path string) (family string, bold bool) {
	if fam, b, err := ffmpeg.FontFamily(path); err == nil {
		return fam, b
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	fam, weight, _ := strings.Cut(name, "-")
	return fam, strings.Contains(strings.ToLower(weight), "bold")
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
	if c := v.Captions; c != nil && c.Burn && len(c.Cues) > 0 {
		if c.FontFile == "" {
			return Command{}, ErrNoCaptionFont
		}
		style := c.Style.Normalized()
		family, bold := captionFamily(c.FontFile)
		script := SidecarFile{
			Path:    filepath.Join(workDir, "captions.ass"),
			Content: assScript(screens(*c, style, p.playback, p.playback(p.In), p.playback(p.Out)), style, family, bold),
		}
		// fontsdir adds the server's fonts directory -- the bundled Inter and
		// anything the operator dropped in -- to what fontconfig already has
		// installed, and fontconfig supplies any glyph those lack. Both paths
		// through GraphArgPath: they are filter options inside a graph.
		graph += ",subtitles=filename=" + ffmpeg.GraphArgPath(script.Path) +
			":fontsdir=" + ffmpeg.GraphArgPath(filepath.Dir(c.FontFile))
		files = append(files, script)
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

// warnCaptions says what the burned captions will not show, or may not show
// as asked: events past the script's bound, and writing the chosen font has no
// glyphs for. It runs after resolveSources, because placing a caption needs the
// files.
//
// THE WARNING NEVER QUOTES A CAPTION. A plan's warnings reach the job result,
// and a job is readable by a read-scoped token, while the words of a
// transcript are not; so a script is named -- "Han", "Arabic" -- and never a
// character of what was said.
func (p *Plan) warnCaptions() {
	c := p.Vertical.Captions
	if c == nil || !c.Burn || len(c.Cues) == 0 {
		return
	}
	style := c.Style.Normalized()
	if n := len(screens(*c, style, p.playback, p.playback(p.In), p.playback(p.Out))); n > maxCaptionEvents {
		p.warn("%d captions were not drawn: one short carries at most %d", n-maxCaptionEvents, maxCaptionEvents)
	}
	if isBuiltinFont(style.Font) {
		for _, cue := range c.Cues {
			if script, ok := uncoveredScript(cue.Text); ok {
				p.warn("some captions are written in %s, which the bundled Inter font does not have: they are drawn "+
					"with an installed font that does, or as boxes if none is installed (see the caption fonts notes)", script)
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
// and symbol blocks around them.
var interRanges = [][2]rune{
	{0x0000, 0x036F}, {0x0370, 0x03FF}, {0x0400, 0x052F}, {0x1E00, 0x1FFF},
	{0x2000, 0x206F}, {0x20A0, 0x20CF}, {0x2100, 0x22FF},
}

// namedScripts are the writing systems a warning can name.
var namedScripts = []struct {
	name  string
	table *unicode.RangeTable
}{
	{"Chinese, Japanese or Korean (Han)", unicode.Han}, {"Japanese (Hiragana)", unicode.Hiragana},
	{"Japanese (Katakana)", unicode.Katakana}, {"Korean (Hangul)", unicode.Hangul}, {"Arabic", unicode.Arabic},
	{"Hebrew", unicode.Hebrew}, {"Devanagari", unicode.Devanagari}, {"Thai", unicode.Thai},
	{"Bengali", unicode.Bengali}, {"Tamil", unicode.Tamil}, {"Armenian", unicode.Armenian}, {"Georgian", unicode.Georgian},
}

// uncoveredScript names the writing system of the first character in s the
// bundled font cannot draw -- by script, never by character.
func uncoveredScript(s string) (string, bool) {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ue000' || r == '\ue001' || inRanges(r, interRanges) {
			continue
		}
		for _, sc := range namedScripts {
			if unicode.Is(sc.table, r) {
				return sc.name, true
			}
		}
		if r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) {
			return "emoji", true
		}
		return "symbols outside its character set", true
	}
	return "", false
}

func inRanges(r rune, rs [][2]rune) bool {
	for _, rg := range rs {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
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
		// One file, seeked on the input to the in-point: the clock is the
		// position in that file, from the in-point. Through the file rather
		// than by subtraction, so an out-point in a gap after it cannot run
		// the clock past the picture.
		return p.played(t) - p.played(p.In)
	}
	return p.played(t)
}

// played is how far into the files, laid back to back, timeline position t
// falls. A gap takes no time.
func (p Plan) played(t time.Duration) time.Duration {
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

// startOnPicture moves an in-point that falls in a gap -- after a recorder
// stopped and before it resumed -- to where the picture resumes. Seeking a
// file to a negative offset clamps to its start, so the short would begin at
// the resume point anyway; the plan has to agree, or its length and every
// caption are off by the gap.
func (p *Plan) startOnPicture() {
	if len(p.Sources) == 0 || p.In >= p.Sources[0].Start {
		return
	}
	p.warn("the in-point %s is in a gap in the recording; the short starts where the recording resumes, at %s",
		round(p.In), round(p.Sources[0].Start))
	p.In = p.Sources[0].Start
	p.HeadSeek = 0
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
	if p.HeadThreads == 0 {
		p.HeadThreads = VerticalThreads()
	}
}

// measureVertical settles a short's length once its sources are known and its
// in-point is on picture: what PLAYS, the files laid back to back, not Out-In.
// A gap takes no time in the short, so it neither counts as re-encoded nor
// toward the 60 s guard.
func (p *Plan) measureVertical() {
	p.HeadDuration = p.Duration()
	if p.HeadDuration > ShortMaxDuration {
		p.warn("this short is %s long; YouTube Shorts, Reels and TikTok's short format expect "+
			"%s or less, and a longer upload may be published as a regular video", round(p.HeadDuration), ShortMaxDuration)
	}
	if c := p.Vertical.Captions; c != nil && len(c.Cues) > 0 && !c.Burn {
		p.warn("this FFmpeg cannot burn captions (it has no subtitles filter -- it was built without " +
			"libass), so the captions were not burned in; the short is otherwise complete")
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
			cues = append(cues, Cue{StartMS: start, EndMS: end, Text: l.Text, Track: l.Track})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].StartMS < cues[j].StartMS })
	return cues, nil
}

// ForJob is a copy of v fit to store in a job, whose params are readable by any
// token allowed to list jobs: the captions keep their tracks and style and lose
// the words, the server's absolute font path and the FFmpeg capability. The
// worker restores all three when it runs (WithTranscript, WithCaptionFonts,
// WithCaptionBurn).
func (v *Vertical) ForJob() *Vertical {
	if v == nil || v.Captions == nil {
		return v
	}
	out := *v
	c := *v.Captions
	c.Cues, c.FontFile, c.Burn = nil, "", false
	out.Captions = &c
	return &out
}
