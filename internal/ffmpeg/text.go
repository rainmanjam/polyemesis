package ffmpeg

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// Text overlays on renditions.
//
// The same rule as image overlays: this forces a video re-encode, so it lives
// on a RENDITION, where re-encoding is already the contract, and never on a
// destination, which does `-c:v copy`. See overlay.go.
//
// Unlike an image, text is a FILTER rather than a second input, so it needs no
// `[1:v]` and joins the main chain. It is drawn LAST, after any image overlay,
// because a caption underneath a logo is not a thing anyone asks for.

// TextSpec is one line of burned-in text.
//
// Geometry is percentage-based for the reason OverlaySpec gives: the same
// overlay is attached to a 1920x1080 and a 1080x1920 rendition, and a pixel
// size that reads well on one is unreadable or off-canvas on the other.
type TextSpec struct {
	// Text is the literal string to draw. It is NEVER interpreted -- see
	// drawtextFilter for why expansion is switched off explicitly.
	Text string
	// FontFile is an absolute path, resolved from the fonts directory by the
	// caller via FontPath. Empty means the built-in default, which the caller
	// is expected to have resolved too; a spec that reaches the graph with no
	// font is not drawn, because drawtext with no fontfile falls back to
	// fontconfig and the shipping image has no fonts for it to find.
	FontFile string
	Anchor   OverlayAnchor
	// SizePct is the font size as a fraction of the output HEIGHT, 0-1.
	// Height rather than width, because a glyph's legibility is set by how
	// tall it is and portrait renditions would otherwise get tiny text.
	SizePct float64
	// Color is an FFmpeg colour: a name, 0xRRGGBB, or either with @alpha.
	Color string
	// MarginXPct and MarginYPct are the gap from the anchored edges, as
	// fractions of the output width and height. Ignored on a centred axis.
	MarginXPct float64
	MarginYPct float64
	// Box draws a filled rectangle behind the text. Off by default, but it is
	// the difference between readable and not over bright or busy footage --
	// white text alone disappears against a white shirt.
	Box        bool
	BoxColor   string
	BoxOpacity float64
}

// Active reports whether this text should be compiled into the graph.
func (t *TextSpec) Active() bool {
	return t != nil && strings.TrimSpace(t.Text) != "" && t.SizePct > 0 && t.FontFile != ""
}

// Defaults applied when the operator left a field empty. Kept here so the
// validator, the graph and the UI cannot disagree about them.
const (
	DefaultTextColor    = "white"
	DefaultTextBoxColor = "black"
	// MinTextSizePx is the floor a percentage is clamped to. Below roughly this
	// the glyphs stop being legible after encoding, and drawtext accepts 0
	// happily and draws nothing.
	MinTextSizePx = 8
)

// drawtextFilter renders the drawtext filter for one TextSpec, or "" when
// there is nothing to draw.
//
// outW and outH are the rendition's output size in pixels.
//
// Three details are load-bearing, and two of them are security-relevant:
//
//   - `expansion=none`. By DEFAULT drawtext interprets the text: `%{...}`
//     expands filter expressions and, with expansion=strftime, `%H` and friends
//     expand as a date format. Operator text is not a template. Without this a
//     station name containing a bare '%' either breaks the stream or expands
//     into something nobody typed, and `%{eif:...}` becomes an expression
//     evaluator fed by a database row.
//   - The font path and the text both go into FILTER ARGUMENTS, where ':' and
//     '\' are metacharacters. On Windows the font path is
//     C:\...\Inter-Regular.ttf and carries both, so an unescaped path is a parse
//     error rather than merely wrong. escapeLavfiArg handles them at the TWO
//     levels a filtergraph unescapes; see there.
//   - fontsize is computed in Go rather than expressed as a filter variable, for
//     the reason overlayWidthPx gives: the value is already known here, and an
//     expression would differ across FFmpeg versions.
func drawtextFilter(t *TextSpec, outW, outH int) string {
	if !t.Active() || outW <= 0 || outH <= 0 {
		return ""
	}
	size := textSizePx(t.SizePct, outH)
	x, y := textPosition(t, outW, outH)

	var b strings.Builder
	b.WriteString("drawtext=fontfile=")
	b.WriteString(filterPath(t.FontFile))
	b.WriteString(":text=")
	b.WriteString(escapeLavfiArg(t.Text))
	fmt.Fprintf(&b, ":fontsize=%d:fontcolor=%s:x=%s:y=%s",
		size, escapeLavfiArg(textColorOr(t.Color, DefaultTextColor)), x, y)

	if t.Box {
		// boxborderw scales with the type, so the padding looks the same on a
		// 360p rendition and a 1080p one rather than vanishing on the larger.
		fmt.Fprintf(&b, ":box=1:boxcolor=%s:boxborderw=%d",
			escapeLavfiArg(boxColorWithAlpha(t)), maxInt(2, size/4))
	}
	// LAST, and never omitted. See the note above: this is what stops operator
	// text being treated as a template.
	//
	// Measured, not argued. With this line removed, "100% LIVE" renders ZERO
	// pixels against 2750 for "LIVE" -- drawtext consumes the '%' as an
	// expansion sequence, draws nothing at all, and exits 0. A station name
	// with a percent sign in it would produce a silently blank overlay and no
	// error anywhere. See TestDrawtextGraphRunsAndDoesNotExpandAPercentSign.
	b.WriteString(":expansion=none")
	return b.String()
}

// escapeLavfiArg protects a value inside a FILTER ARGUMENT within a filtergraph
// description. That is one level deeper than a single-level escaper, and the extra
// level is not cosmetic.
//
// A filtergraph is unescaped TWICE: once when the description is split into
// chains and filters, and again when a filter's arguments are split on ':'. So
// a single `\:` is consumed by the first pass and the colon arrives bare at the
// second. Measured against the real parser, with a font in a directory called
// "my:fonts":
//
//	/tmp/my:fonts/f.ttf      fails
//	/tmp/my\:fonts/f.ttf     fails   <- what a single-level escaper produces
//	/tmp/my\\:fonts/f.ttf    works
//
// This is what broke the Windows runner: C:\... escaped once became
// `C\:\\Users\\...`, and FFmpeg reported "No option name near '\Users...'".
//
// A literal backslash needs four, for the same reason: two passes halve it
// twice.
var lavfiArgEscaper = strings.NewReplacer(
	`\`, `\\\\`,
	`:`, `\\:`,
	`,`, `\\,`,
	`'`, `\\'`,
	`[`, `\\[`,
	`]`, `\\]`,
	`;`, `\\;`,
)

func escapeLavfiArg(v string) string { return lavfiArgEscaper.Replace(v) }

// filterPath renders a filesystem path for use INSIDE a filter argument.
//
// Escaping alone is not enough on Windows, and the failure is not subtle: even
// filtergraph is unescaped TWICE -- once when the description is split into
// chains and filters, and again when a filter's arguments are split on ':' --
// so a single-level escaper's `\` -> `\\` collapses back to a single backslash at the
// separator is converted rather than escaped. The real error, from
// the Windows CI runner:
//
//	drawtext=fontfile=C\:\\Users\\RUNNER~1\\...\\Inter-Regular.ttf
//	[AVFilterGraph] No option name near '\Users\RUNNER~1\...'
//
// Windows accepts forward slashes in paths at the API level, so converting
// first leaves only the colon to escape and sidesteps the double-unescape
// entirely. This is the form every working example in the wild uses.
//
// filepath.ToSlash, and here it is CORRECT -- which is the exact opposite of
// internal/clipper's fileURL, where it was a bug. The difference is where the
// string is going. This path is handed to an FFmpeg running on THIS machine, so
// depending on this platform's separator is the whole point. An OTIO media
// reference is written to a file that is opened on a DIFFERENT machine, so
// depending on this platform's separator silently renamed the file.
func filterPath(p string) string {
	return escapeLavfiArg(filepath.ToSlash(p))
}

// textSizePx converts the stored percentage into a pixel size.
//
// Floored rather than allowed to reach zero: drawtext accepts fontsize=0 and
// draws nothing at all, which looks to the operator exactly like the overlay
// being ignored and gives them nothing to act on.
func textSizePx(pct float64, outH int) int {
	px := int(math.Round(clamp01(pct) * float64(outH)))
	if px < MinTextSizePx {
		return MinTextSizePx
	}
	return px
}

// textPosition renders the x and y expressions for the anchor.
//
// drawtext's own variables, which are NOT the overlay filter's: the frame is
// `w`/`h` here (overlay calls it main_w/main_h) and the drawn box is
// `text_w`/`text_h` (overlay calls it overlay_w/overlay_h). Using the wrong
// pair is a filtergraph error at start time, not a misplacement.
func textPosition(t *TextSpec, outW, outH int) (string, string) {
	mx := marginPx(t.MarginXPct, outW)
	my := marginPx(t.MarginYPct, outH)

	var x, y string
	switch t.Anchor {
	case AnchorTopCenter, AnchorCenter, AnchorBottomCenter:
		x = "(w-text_w)/2"
	case AnchorTopRight, AnchorMiddleRight, AnchorBottomRight:
		x = fmt.Sprintf("w-text_w-%d", mx)
	default:
		// Every left anchor, and an unrecognised one. Degrading to top-left
		// rather than refusing, exactly as overlayPosition does: a rendition
		// row written by a newer build must still encode, and a stream that
		// does not start is a worse answer than a caption in the wrong corner.
		x = fmt.Sprintf("%d", mx)
	}
	switch t.Anchor {
	case AnchorMiddleLeft, AnchorCenter, AnchorMiddleRight:
		y = "(h-text_h)/2"
	case AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight:
		y = fmt.Sprintf("h-text_h-%d", my)
	default:
		y = fmt.Sprintf("%d", my)
	}
	return x, y
}

// textColorOr falls back to the default for an empty value.
func textColorOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// boxColorWithAlpha renders boxcolor, folding the opacity in as FFmpeg's
// `colour@alpha` form.
//
// A box at full opacity is a solid slab that hides the picture behind it, so
// the opacity is the setting that makes this feature usable rather than a
// decoration. An operator-supplied colour that ALREADY carries an @alpha is
// left alone rather than having a second one appended, which would produce a
// colour FFmpeg rejects.
func boxColorWithAlpha(t *TextSpec) string {
	c := textColorOr(t.BoxColor, DefaultTextBoxColor)
	if strings.Contains(c, "@") {
		return c
	}
	op := clamp01(t.BoxOpacity)
	if op <= 0 || op >= 1 {
		return c
	}
	return c + "@" + trimFloat(op)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
