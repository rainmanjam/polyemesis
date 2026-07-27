package media

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- thumbnails
//
// Three artefacts, one decode.
//
//   - POSTER, so the library is a wall of pictures rather than a wall of
//     filenames.
//   - CONTACT SHEET, a tiled grid across the whole recording, so "was anyone
//     even on camera for this hour" is answered at a glance without opening it.
//   - SPRITE SHEET + WebVTT, the tiled strip a player addresses by pixel
//     rectangle to draw a preview above the scrub bar. This is the one that
//     makes a timeline feel professional rather than homemade, and it is
//     nothing more than a grid of small JPEGs plus a text file saying which
//     rectangle belongs to which second.
//
// ThumbnailArgs builds all three as OUTPUTS OF ONE FFmpeg PROCESS, split off a
// single decode — the same argument internal/ffmpeg's stem recorder makes, and
// for the first of its reasons doubled: decoding an hour of 1080p is by far the
// most expensive thing in this file, and doing it three times to produce three
// small JPEGs would be three times the CPU stolen from a live stream.
//
// The individual builders below stay exported and stay tested, because a caller
// that only wants a poster should not pay for a contact sheet.

// Thumbnail defaults.
const (
	// DefaultPosterFraction places the poster 10% into the recording. Frame
	// zero of a broadcast is a black frame, a countdown, or a slate; a tenth of
	// the way in is past all three.
	DefaultPosterFraction = 0.10
	// DefaultPosterSeconds is where the poster goes when the duration is
	// unknown. Far enough in to be past a slate, near enough to exist in a
	// short clip.
	DefaultPosterSeconds = 10
	// DefaultPosterHeight is a library card at 2x.
	DefaultPosterHeight = 360
	// DefaultSmartFrames asks the thumbnail filter to pick the most
	// representative frame out of this many, which skips the blurred
	// mid-pan frame a blind grab lands on about a third of the time.
	DefaultSmartFrames = 100
	MaxSmartFrames     = 500

	// DefaultJPEGQuality is FFmpeg's -q:v, where 1 is best and 31 worst. 3 is
	// visually clean; these are thumbnails, not deliverables.
	DefaultJPEGQuality = 3
	MinJPEGQuality     = 1
	MaxJPEGQuality     = 31

	// Contact sheet: 5 across, 4 down is 20 frames, which reads as a grid on a
	// laptop without becoming a mosaic.
	DefaultContactCols      = 5
	DefaultContactRows      = 4
	DefaultContactTileWidth = 320
	DefaultContactTileHigh  = 180
	DefaultContactMargin    = 4
	DefaultContactPadding   = 2
	// DefaultContactInterval is the fallback spacing when the duration is not
	// known. The sheet then covers the first 200 s rather than the whole file —
	// a partial sheet beats no sheet, and vf_tile flushes an incomplete grid at
	// EOF rather than dropping it.
	DefaultContactInterval = 10

	// Sprite sheet: 10x10 tiles of 160x90, i.e. one 1600x900 JPEG per 100
	// thumbnails, which is a single cheap image request for over eight minutes
	// of a 5 s grid.
	DefaultSpriteCols     = 10
	DefaultSpriteRows     = 10
	DefaultSpriteWidth    = 160
	DefaultSpriteHeight   = 90
	DefaultSpriteInterval = 5
	MinSpriteInterval     = 0.5
	// MaxSpriteFrames caps the grid for a long recording. At the 5 s default a
	// six-hour segment would want 4320 thumbnails across 44 sheets; past this
	// the interval is widened instead, because a scrub preview that is coarse
	// is useful and one that costs 44 image requests is not.
	MaxSpriteFrames = 1200

	// MaxTileGrid bounds one axis of a tile grid. Purely a guard against a
	// configuration typo turning into a gigapixel allocation inside FFmpeg.
	MaxTileGrid = 100
	// MaxTileDimension bounds one tile.
	MaxTileDimension = 1024
)

// PosterSpec is a single representative still.
type PosterSpec struct {
	Input  string
	Output string

	// DurationSeconds is the recording's length, used to place the poster. Zero
	// means unknown, which falls back to DefaultPosterSeconds rather than
	// failing: a poster from the wrong moment beats no poster.
	DurationSeconds float64
	// AtSeconds overrides the placement outright.
	AtSeconds float64

	Width  int
	Height int
	// SmartFrames asks the thumbnail filter to choose from this many frames;
	// 0 means DefaultSmartFrames and a negative value means "grab whatever
	// frame is at the seek point", which is faster and occasionally uglier.
	SmartFrames int
	Quality     int
}

// PosterSeconds is where in the recording the poster is taken from.
func (s PosterSpec) PosterSeconds() float64 {
	if s.AtSeconds > 0 {
		return s.AtSeconds
	}
	if s.DurationSeconds <= 0 {
		return DefaultPosterSeconds
	}
	at := s.DurationSeconds * DefaultPosterFraction
	// A recording shorter than the fallback still gets a poster from inside
	// itself; seeking past the end produces an empty output, not an error, so
	// this has to be clamped rather than trusted.
	if at >= s.DurationSeconds {
		at = s.DurationSeconds / 2
	}
	return at
}

// PosterArgs builds the poster grab as its own process.
//
// -ss goes BEFORE -i, which makes it an input seek: FFmpeg jumps to the nearest
// preceding keyframe and decodes forward from there, instead of decoding the
// whole file and discarding it. On an hour-long segment that is the difference
// between a second and several minutes.
func PosterArgs(s PosterSpec) []string {
	if s.Width <= 0 && s.Height <= 0 {
		s.Height = DefaultPosterHeight
	}
	args := commonArgs()
	args = append(args, "-ss", formatSeconds(s.PosterSeconds()), "-i", s.Input)
	args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn")
	if vf := posterFilter(s); vf != "" {
		args = append(args, "-vf", vf)
	}
	args = append(args,
		"-frames:v", "1",
		"-q:v", strconv.Itoa(jpegQuality(s.Quality)),
		"-f", "image2",
		s.Output,
	)
	return args
}

func posterFilter(s PosterSpec) string {
	var chain []string
	if n := smartFrames(s.SmartFrames); n > 0 {
		chain = append(chain, "thumbnail="+strconv.Itoa(n))
	}
	if sc := scaleFilter(s.Width, s.Height); sc != "" {
		chain = append(chain, sc)
	}
	return strings.Join(chain, ",")
}

func smartFrames(n int) int {
	if n < 0 {
		return 0
	}
	if n == 0 {
		return DefaultSmartFrames
	}
	return clampInt(n, 2, MaxSmartFrames)
}

func jpegQuality(q int) int {
	if q <= 0 {
		return DefaultJPEGQuality
	}
	return clampInt(q, MinJPEGQuality, MaxJPEGQuality)
}

// ContactSheetSpec is one tiled grid covering the whole recording.
type ContactSheetSpec struct {
	Input  string
	Output string

	DurationSeconds float64

	Cols, Rows            int
	TileWidth, TileHeight int
	// Margin and Padding are the gutter around and between tiles. Zero takes
	// the defaults, since a contact sheet reads better with one; a NEGATIVE
	// value is how a caller asks for no gutter at all. The sprite sheet does
	// not use these — it pins both to zero, because its WebVTT addresses tiles
	// by pixel rectangle and a gutter would offset every one of them.
	Margin, Padding int
	Color           string
	Quality         int
}

// Normalized fills the zero values and clamps the rest.
func (s ContactSheetSpec) Normalized() ContactSheetSpec {
	if s.Cols <= 0 {
		s.Cols = DefaultContactCols
	}
	if s.Rows <= 0 {
		s.Rows = DefaultContactRows
	}
	s.Cols = clampInt(s.Cols, 1, MaxTileGrid)
	s.Rows = clampInt(s.Rows, 1, MaxTileGrid)
	if s.TileWidth <= 0 {
		s.TileWidth = DefaultContactTileWidth
	}
	if s.TileHeight <= 0 {
		s.TileHeight = DefaultContactTileHigh
	}
	s.TileWidth = clampInt(s.TileWidth, 16, MaxTileDimension)
	s.TileHeight = clampInt(s.TileHeight, 16, MaxTileDimension)
	s.Margin = gutter(s.Margin, DefaultContactMargin)
	s.Padding = gutter(s.Padding, DefaultContactPadding)
	return s
}

// gutter resolves a Margin or Padding: 0 takes the default, negative means
// none. See the field comment for why the zero value is not "none".
func gutter(v, def int) int {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	default:
		return clampInt(v, 0, MaxTileDimension)
	}
}

// Interval is the spacing between tiles, chosen so the grid spans the whole
// recording.
func (s ContactSheetSpec) Interval() float64 {
	s = s.Normalized()
	if s.DurationSeconds <= 0 {
		return DefaultContactInterval
	}
	iv := s.DurationSeconds / float64(s.Cols*s.Rows)
	if iv <= 0 {
		return DefaultContactInterval
	}
	return iv
}

// ContactSheetArgs builds the contact sheet as its own process.
func ContactSheetArgs(s ContactSheetSpec) []string {
	s = s.Normalized()
	iv := s.Interval()

	args := commonArgs()
	// Half an interval in, so each tile is the middle of the slice it stands
	// for rather than its first frame — and so tile one is not the black frame
	// every broadcast opens with.
	args = append(args, "-ss", formatSeconds(iv/2), "-i", s.Input)
	args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn")
	args = append(args, "-vf", contactFilter(s, iv))
	args = append(args,
		// One sheet, even if the arithmetic drifts and a second grid could be
		// filled. vf_tile flushes a partial grid at EOF, so a short recording
		// still produces a sheet rather than nothing.
		"-frames:v", "1",
		"-q:v", strconv.Itoa(jpegQuality(s.Quality)),
		"-f", "image2",
		s.Output,
	)
	return args
}

func contactFilter(s ContactSheetSpec, interval float64) string {
	color := filterColor(s.Color)
	return fmt.Sprintf("fps=1/%s,%s,tile=%dx%d:margin=%d:padding=%d:color=%s",
		formatSeconds(interval),
		fitTileFilter(s.TileWidth, s.TileHeight, color),
		s.Cols, s.Rows, s.Margin, s.Padding, color)
}

// SpriteSpec is the scrub-bar preview strip: a run of tiled sheets plus the
// WebVTT that indexes them.
type SpriteSpec struct {
	Input string
	// OutputPattern is an image2 pattern, e.g. /path/sprite-%03d.jpg. The muxer
	// numbers sheets from 1.
	OutputPattern string

	DurationSeconds float64
	IntervalSeconds float64

	Cols, Rows            int
	TileWidth, TileHeight int
	Color                 string
	Quality               int
}

// Normalized fills the zero values and clamps the rest.
func (s SpriteSpec) Normalized() SpriteSpec {
	if s.Cols <= 0 {
		s.Cols = DefaultSpriteCols
	}
	if s.Rows <= 0 {
		s.Rows = DefaultSpriteRows
	}
	s.Cols = clampInt(s.Cols, 1, MaxTileGrid)
	s.Rows = clampInt(s.Rows, 1, MaxTileGrid)
	if s.TileWidth <= 0 {
		s.TileWidth = DefaultSpriteWidth
	}
	if s.TileHeight <= 0 {
		s.TileHeight = DefaultSpriteHeight
	}
	s.TileWidth = clampInt(s.TileWidth, 16, MaxTileDimension)
	s.TileHeight = clampInt(s.TileHeight, 16, MaxTileDimension)
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = DefaultSpriteInterval
	}
	if s.IntervalSeconds < MinSpriteInterval {
		s.IntervalSeconds = MinSpriteInterval
	}
	return s
}

// Interval is the spacing actually used, widened when the requested one would
// produce more thumbnails than MaxSpriteFrames.
//
// Widening rather than truncating is the point: a coarse preview across the
// whole recording is useful, while a fine preview that stops an hour in looks
// like a bug to the person dragging the scrub bar.
func (s SpriteSpec) Interval() float64 {
	s = s.Normalized()
	if s.DurationSeconds <= 0 {
		return s.IntervalSeconds
	}
	iv := s.IntervalSeconds
	if n := math.Ceil(s.DurationSeconds / iv); n > MaxSpriteFrames {
		iv = s.DurationSeconds / MaxSpriteFrames
	}
	return iv
}

// Frames is how many thumbnails the sheet run holds.
func (s SpriteSpec) Frames() int {
	n := s.Normalized()
	if n.DurationSeconds <= 0 {
		return 0
	}
	return int(math.Ceil(n.DurationSeconds / n.Interval()))
}

// Sheets is how many image files the run produces.
func (s SpriteSpec) Sheets() int {
	n := s.Normalized()
	per := n.Cols * n.Rows
	frames := n.Frames()
	if frames == 0 || per == 0 {
		return 0
	}
	return (frames + per - 1) / per
}

// SpriteArgs builds the sprite sheets as their own process.
//
// No -frames:v here: the muxer writes as many sheets as the grid fills, and the
// last, partial one is flushed at EOF.
func SpriteArgs(s SpriteSpec) []string {
	s = s.Normalized()
	color := filterColor(s.Color)

	args := commonArgs()
	args = append(args, "-i", s.Input)
	args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn")
	// margin and padding are pinned to zero rather than left at their
	// defaults. The WebVTT below addresses each thumbnail as a pixel rectangle
	// computed from col*width and row*height, and any gutter between tiles
	// would offset every rectangle after the first.
	args = append(args, "-vf", fmt.Sprintf("fps=1/%s,%s,tile=%dx%d:margin=0:padding=0:color=%s",
		formatSeconds(s.Interval()),
		fitTileFilter(s.TileWidth, s.TileHeight, color),
		s.Cols, s.Rows, color))
	args = append(args,
		"-q:v", strconv.Itoa(jpegQuality(s.Quality)),
		"-f", "image2",
		s.OutputPattern,
	)
	return args
}

// SheetName renders the filename of sheet n, numbered from 1 the way the image2
// muxer numbers them.
//
// A pattern with no verb in it is returned unchanged, because Sprintf would
// otherwise append its own complaint to the filename and the VTT would point at
// a file that does not exist.
func SheetName(pattern string, n int) string {
	base := baseName(pattern)
	if !strings.Contains(base, "%") {
		return base
	}
	return fmt.Sprintf(base, n)
}

// VTT renders the WebVTT that maps playback time to a rectangle in a sheet.
//
// The format is a cue per thumbnail whose payload is an image URL with a
// #xywh=x,y,w,h fragment. Players resolve that URL relative to the VTT, which
// is why only the sheets' base names appear: the .vtt and its sheets live in
// the same directory, so the file survives being served from any URL prefix.
//
// Returns "" when the duration is unknown. A VTT with no cues is not a
// degraded preview, it is a file that makes a player draw nothing while
// pretending a preview exists.
func (s SpriteSpec) VTT() string {
	n := s.Normalized()
	frames := n.Frames()
	if frames <= 0 {
		return ""
	}
	iv := n.Interval()
	per := n.Cols * n.Rows

	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := 0; i < frames; i++ {
		start := float64(i) * iv
		end := start + iv
		// The last cue is trimmed to the real end, so a player asking for the
		// final second does not fall off the end of the cue list.
		if end > n.DurationSeconds {
			end = n.DurationSeconds
		}
		sheet := i/per + 1
		within := i % per
		x := (within % n.Cols) * n.TileWidth
		y := (within / n.Cols) * n.TileHeight

		fmt.Fprintf(&b, "%s --> %s\n", vttTime(start), vttTime(end))
		fmt.Fprintf(&b, "%s#xywh=%d,%d,%d,%d\n\n",
			SheetName(n.OutputPattern, sheet), x, y, n.TileWidth, n.TileHeight)
	}
	return b.String()
}

// vttTime renders HH:MM:SS.mmm, which is the only timestamp form the WebVTT
// grammar accepts for cues over an hour long — and recordings here are
// routinely over an hour long.
func vttTime(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) {
		seconds = 0
	}
	ms := int64(math.Round(seconds * 1000))
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	sec := ms / 1000
	ms -= sec * 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, sec, ms)
}

// ---------------------------------------------------------------- single pass

// ThumbnailSpec asks for any combination of the three artefacts.
type ThumbnailSpec struct {
	Input string

	// Poster, ContactSheet and Sprites are each generated when their output is
	// set. An empty output means "not this one", so a caller that only wants a
	// poster does not pay for a full-file decode of a contact sheet.
	Poster       PosterSpec
	ContactSheet ContactSheetSpec
	Sprites      SpriteSpec

	DurationSeconds float64
}

// Normalized propagates Input and DurationSeconds down into the three
// sub-specs, so a caller sets them once.
func (s ThumbnailSpec) Normalized() ThumbnailSpec {
	s.Poster.Input = s.Input
	s.ContactSheet.Input = s.Input
	s.Sprites.Input = s.Input
	if s.Poster.DurationSeconds == 0 {
		s.Poster.DurationSeconds = s.DurationSeconds
	}
	if s.ContactSheet.DurationSeconds == 0 {
		s.ContactSheet.DurationSeconds = s.DurationSeconds
	}
	if s.Sprites.DurationSeconds == 0 {
		s.Sprites.DurationSeconds = s.DurationSeconds
	}
	return s
}

// Outputs reports which artefacts this spec asks for.
func (s ThumbnailSpec) Outputs() (poster, contact, sprites bool) {
	return s.Poster.Output != "", s.ContactSheet.Output != "", s.Sprites.OutputPattern != ""
}

// ThumbnailArgs builds every requested artefact as outputs of ONE process.
//
// One decode feeds all three through a split, which matters because decoding
// the recording is the entire cost here — the JPEG encoding is free by
// comparison. Three separate passes would triple the CPU this steals from a
// live stream to produce the same bytes.
//
// The poster is trimmed rather than input-seeked, because an input seek belongs
// to the whole process and the other two outputs need to start at zero. The
// trim is inside the graph, so it costs a decode of the skipped region — the
// price of sharing the decode, and still a third of what three passes cost.
//
// Returns nil when nothing is asked for, which the caller must treat as "done",
// not as an error.
func ThumbnailArgs(s ThumbnailSpec) []string {
	s = s.Normalized()
	wantPoster, wantContact, wantSprites := s.Outputs()
	if !wantPoster && !wantContact && !wantSprites {
		return nil
	}

	// One output does not need a split, and vf_split with a single branch is
	// legal but noisy; the dedicated builders are also better at it, since a
	// lone poster can use a real input seek.
	switch {
	case wantPoster && !wantContact && !wantSprites:
		return PosterArgs(s.Poster)
	case wantContact && !wantPoster && !wantSprites:
		return ContactSheetArgs(s.ContactSheet)
	case wantSprites && !wantPoster && !wantContact:
		return SpriteArgs(s.Sprites)
	}

	contact := s.ContactSheet.Normalized()
	sprites := s.Sprites.Normalized()

	var labels []string
	var chains []string
	if wantPoster {
		labels = append(labels, "[po]")
	}
	if wantContact {
		labels = append(labels, "[co]")
	}
	if wantSprites {
		labels = append(labels, "[so]")
	}

	var g strings.Builder
	fmt.Fprintf(&g, "[0:v]split=%d%s;", len(labels), strings.Join(labels, ""))

	if wantPoster {
		var chain []string
		at := s.Poster.PosterSeconds()
		if at > 0 {
			// setpts rebases the trimmed segment to zero. Without it the first
			// frame carries the original timestamp and the image2 muxer, which
			// takes the first frame it is handed, is unaffected — but the
			// thumbnail filter's own frame counting is, and a graph that only
			// works by accident is one that breaks on the next FFmpeg.
			chain = append(chain, "trim=start="+formatSeconds(at), "setpts=PTS-STARTPTS")
		}
		if pf := posterFilter(s.Poster); pf != "" {
			chain = append(chain, pf)
		}
		if len(chain) == 0 {
			chain = append(chain, "null")
		}
		chains = append(chains, "[po]"+strings.Join(chain, ",")+"[pout]")
	}
	if wantContact {
		chains = append(chains, "[co]"+contactFilter(contact, contact.Interval())+"[cout]")
	}
	if wantSprites {
		color := filterColor(sprites.Color)
		chains = append(chains, fmt.Sprintf("[so]fps=1/%s,%s,tile=%dx%d:margin=0:padding=0:color=%s[sout]",
			formatSeconds(sprites.Interval()),
			fitTileFilter(sprites.TileWidth, sprites.TileHeight, color),
			sprites.Cols, sprites.Rows, color))
	}
	g.WriteString(strings.Join(chains, ";"))

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args, "-i", s.Input, "-filter_complex", g.String())

	if wantPoster {
		args = append(args, "-map", "[pout]", "-frames:v", "1",
			"-q:v", strconv.Itoa(jpegQuality(s.Poster.Quality)), "-f", "image2", s.Poster.Output)
	}
	if wantContact {
		args = append(args, "-map", "[cout]", "-frames:v", "1",
			"-q:v", strconv.Itoa(jpegQuality(contact.Quality)), "-f", "image2", contact.Output)
	}
	if wantSprites {
		args = append(args, "-map", "[sout]",
			"-q:v", strconv.Itoa(jpegQuality(sprites.Quality)), "-f", "image2", sprites.OutputPattern)
	}
	return args
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
