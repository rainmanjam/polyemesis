package clipper

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EDL export, in OpenTimelineIO's JSON schema.
//
// The point is that a cut made here does not have to STAY here. A user marks
// in and out points against a multitrack recording, exports, and opens the
// result in Resolve or Premiere with the media already linked and the cuts
// already on the timeline — no conforming, no relinking, no retyping
// timecodes.
//
// The JSON is written directly rather than by shelling out to the OTIO Python
// library, and that is not a shortcut. Every external tool in this product is
// optional and detected; a format whose only writer is a Python package would
// make "export an EDL" the one feature that needs a second runtime installed.
// The schema is small, stable and documented, and the objects below are all of
// it that a cut list uses.
//
// Schema versions are deliberately the OLD ones. OTIO upgrades an older
// document to the running library's version on read and has done since 0.9;
// it cannot downgrade a newer one. Writing Clip.1 therefore opens in every
// reader that exists, while writing whatever is current would fail in the
// editors most likely to be on the far end of this.

// DefaultEDLRate is the frame rate an EDL falls back to.
//
// A caller that knows the recording's real rate must pass it. This is only the
// floor for one that does not: an EDL at the wrong rate still opens, still
// links the media and still shows the cuts, it just rounds each edit to a
// different frame — which is recoverable in an editor, while a file that will
// not open is not.
const DefaultEDLRate = 30.0

// EDL is a cut list ready to hand to an editor.
type EDL struct {
	// Name is the timeline's name in the editor.
	Name string `json:"name"`
	// Rate is the timeline frame rate. Zero means DefaultEDLRate.
	Rate float64 `json:"rate"`
	// Clips are laid end to end on one video track, in the order given.
	Clips []EDLClip `json:"clips"`
}

// EDLClip is one cut against one media file.
type EDLClip struct {
	Name string `json:"name"`
	// Path is the media file on disk. It becomes a file:// URL.
	Path string `json:"path"`
	// In is where the cut starts INSIDE that file, not on the timeline.
	In       time.Duration `json:"in"`
	Duration time.Duration `json:"duration"`
	// Available is how long the media file runs. Zero means unknown, and the
	// reference then advertises only the cut's own range — which is honest, and
	// which an editor handles by refusing to trim past it rather than by
	// reading past the end of a file.
	Available time.Duration `json:"available,omitempty"`

	Markers  []EDLMarker    `json:"markers,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EDLMarker is a flag on a clip, positioned in the same coordinates as
// EDLClip.In: inside the media file.
type EDLMarker struct {
	Name  string        `json:"name"`
	At    time.Duration `json:"at"`
	Color string        `json:"color,omitempty"`
	// Comment is the sentence the editor shows when the marker is opened.
	Comment string `json:"comment,omitempty"`
}

// Marker colours OTIO defines. Only the ones this package uses are named.
const (
	MarkerRed  = "RED"
	MarkerBlue = "BLUE"
)

// FromPlan turns a resolved cut into a one-timeline EDL.
//
// A cut that spans segment boundaries becomes SEVERAL clips, one per source
// file, butted together in order. That is the truth of what is on disk: the
// editor gets the same two files the clipper would have concatenated, and a
// user who wants to trim across the seam can, which they could not if the seam
// had been flattened into a single opaque reference.
//
// When the in-point moved, the first clip carries a marker at the frame the
// user originally asked for. The EDL is frequently the artefact that outlives
// the conversation about drift, so it says so out loud rather than leaving the
// discrepancy to be rediscovered in an edit suite.
func FromPlan(p Plan, rate float64) EDL {
	name := p.Title
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(p.OutPath), filepath.Ext(p.OutPath))
	}
	if name == "" {
		name = "clip"
	}

	e := EDL{Name: name, Rate: rate}
	for i, s := range p.Sources {
		in, out := maxDur(p.In, s.Start), minDur(p.Out, s.End())
		if out <= in {
			continue
		}
		c := EDLClip{
			Name:      name,
			Path:      s.Path,
			In:        in - s.Start,
			Duration:  out - in,
			Available: s.Duration,
			Metadata: map[string]any{
				"polyemesis": map[string]any{
					"mode":         string(p.Mode),
					"timelineIn":   in.Seconds(),
					"timelineOut":  out.Seconds(),
					"reEncodedSec": p.HeadDuration.Seconds(),
				},
			},
		}
		if i == 0 && p.DriftKnown && p.InDrift != 0 {
			c.Metadata["polyemesis"].(map[string]any)["inDriftSec"] = p.InDrift.Seconds()
			c.Markers = append(c.Markers, EDLMarker{
				Name:  "requested in-point",
				At:    p.RequestedIn - s.Start,
				Color: MarkerRed,
				Comment: fmt.Sprintf("the cut was snapped to the nearest keyframe, %s earlier",
					round(p.InDrift)),
			})
		}
		e.Clips = append(e.Clips, c)
	}
	return e
}

// WriteOTIO writes an EDL beside a clip. The file is written whole and renamed,
// so a reader never sees a half-written timeline.
func WriteOTIO(path string, e EDL) error {
	raw, err := e.MarshalOTIO()
	if err != nil {
		return err
	}
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("clipper: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("clipper: publish %s: %w", path, err)
	}
	return nil
}

// OTIOExt is the conventional extension. ".otio" is what the OTIO tooling
// itself registers, and what an editor's import dialog filters on.
const OTIOExt = ".otio"

// MarshalOTIO renders the EDL as an OpenTimelineIO document.
func (e EDL) MarshalOTIO() ([]byte, error) {
	rate := e.Rate
	if rate <= 0 {
		rate = DefaultEDLRate
	}

	track := otioTrack{
		Schema:   "Track.1",
		Name:     "V1",
		Kind:     "Video",
		Metadata: map[string]any{},
		Effects:  []any{},
		Markers:  []any{},
		Children: make([]otioClip, 0, len(e.Clips)),
	}
	for _, c := range e.Clips {
		track.Children = append(track.Children, c.otio(rate))
	}

	doc := otioTimeline{
		Schema:   "Timeline.1",
		Name:     e.Name,
		Metadata: map[string]any{},
		// A timeline that starts at zero rather than at an hour, because these
		// cuts are addressed from the head of a recording and not from a tape's
		// 01:00:00:00 start-of-programme convention.
		GlobalStartTime: newOTIOTime(0, rate),
		Tracks: otioStack{
			Schema:   "Stack.1",
			Name:     "tracks",
			Metadata: map[string]any{},
			Effects:  []any{},
			Markers:  []any{},
			Children: []otioTrack{track},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func (c EDLClip) otio(rate float64) otioClip {
	available := c.Available
	if available <= 0 {
		// Not "unknown" in the file, because an absent available_range makes
		// some readers treat the media as unbounded. Advertising the cut's own
		// span is the conservative truth.
		available = c.In + c.Duration
	}
	out := otioClip{
		Schema:      "Clip.1",
		Name:        c.Name,
		Metadata:    orEmpty(c.Metadata),
		Effects:     []any{},
		Markers:     []otioMarker{},
		SourceRange: newOTIORange(c.In, c.Duration, rate),
		MediaReference: otioReference{
			Schema:         "ExternalReference.1",
			Name:           filepath.Base(c.Path),
			Metadata:       map[string]any{},
			TargetURL:      fileURL(c.Path),
			AvailableRange: newOTIORange(0, available, rate),
		},
	}
	for _, m := range c.Markers {
		out.Markers = append(out.Markers, otioMarker{
			Schema:   "Marker.2",
			Name:     m.Name,
			Color:    orString(m.Color, MarkerBlue),
			Comment:  m.Comment,
			Metadata: map[string]any{},
			// A zero-length marked_range is a point marker, which is what an
			// in-point is.
			MarkedRange: newOTIORange(m.At, 0, rate),
		})
	}
	return out
}

// fileURL turns a path into the file:// URL OTIO's media references carry.
//
// Windows matters here: C:\media\rec.mkv has to become file:///C:/media/rec.mkv
// or the timeline resolves to nothing on the machine it is opened on. What it
// must NOT do is replace every backslash it sees — a backslash is a perfectly
// legal character in a POSIX filename, and "my\clip.mkv" is one file, not two
// directories. Only a drive-letter or UNC prefix is treated as Windows, and no
// POSIX absolute path can start with either.
//
// Deliberately NOT filepath.ToSlash: that function is defined by the platform
// the code is RUNNING on, and it rewrites every backslash when that platform is
// Windows. So a recording named `my\clip.mkv` produced a correct URL on Linux
// and silently became file:///rec/my/clip.mkv — a path to a different, absent
// file — on Windows. An OTIO file is routinely written on one machine and
// opened on another, so this mapping has to depend only on the path itself.
func fileURL(path string) string {
	if path == "" {
		return ""
	}
	// UNC: \\server\share\file.mkv is file://server/share/file.mkv, with the
	// server as the URL HOST rather than part of the path. Handled explicitly
	// because the drive-letter branch below cannot see it and would otherwise
	// percent-escape the separators into one long nonexistent filename.
	if strings.HasPrefix(path, `\\`) {
		rest := strings.ReplaceAll(path[2:], `\`, "/")
		host, tail, _ := strings.Cut(rest, "/")
		u := url.URL{Scheme: "file", Host: host, Path: "/" + tail}
		return u.String()
	}
	p := path
	if hasDriveLetter(p) {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// hasDriveLetter reports the "C:" prefix that makes a path unambiguously
// Windows.
func hasDriveLetter(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---------------------------------------------------------------- OTIO shapes
//
// One struct per schema object, with every field the chosen version declares.
// Omitting an optional field is how a document that looks fine here fails to
// deserialise in the reader, so nothing below uses omitempty.

type otioTime struct {
	Schema string  `json:"OTIO_SCHEMA"`
	Rate   float64 `json:"rate"`
	Value  float64 `json:"value"`
}

type otioRange struct {
	Schema    string   `json:"OTIO_SCHEMA"`
	Duration  otioTime `json:"duration"`
	StartTime otioTime `json:"start_time"`
}

// newOTIOTime converts seconds to frames.
//
// OTIO counts in frames at a rate, so every boundary is rounded to the nearest
// one. That rounding is the reason DefaultEDLRate is documented as a fallback
// rather than a default worth relying on: at the wrong rate every cut lands on
// a different frame than the clip on disk does.
func newOTIOTime(d time.Duration, rate float64) otioTime {
	return otioTime{Schema: "RationalTime.1", Rate: rate, Value: math.Round(d.Seconds() * rate)}
}

func newOTIORange(start, dur time.Duration, rate float64) otioRange {
	return otioRange{
		Schema:    "TimeRange.1",
		StartTime: newOTIOTime(start, rate),
		Duration:  newOTIOTime(dur, rate),
	}
}

type otioReference struct {
	Schema         string         `json:"OTIO_SCHEMA"`
	Name           string         `json:"name"`
	Metadata       map[string]any `json:"metadata"`
	AvailableRange otioRange      `json:"available_range"`
	TargetURL      string         `json:"target_url"`
}

type otioMarker struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Name        string         `json:"name"`
	Color       string         `json:"color"`
	Comment     string         `json:"comment"`
	MarkedRange otioRange      `json:"marked_range"`
	Metadata    map[string]any `json:"metadata"`
}

type otioClip struct {
	Schema         string         `json:"OTIO_SCHEMA"`
	Name           string         `json:"name"`
	Metadata       map[string]any `json:"metadata"`
	Effects        []any          `json:"effects"`
	Markers        []otioMarker   `json:"markers"`
	SourceRange    otioRange      `json:"source_range"`
	MediaReference otioReference  `json:"media_reference"`
}

type otioTrack struct {
	Schema   string         `json:"OTIO_SCHEMA"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Metadata map[string]any `json:"metadata"`
	Effects  []any          `json:"effects"`
	Markers  []any          `json:"markers"`
	Children []otioClip     `json:"children"`
	// A track with no source_range is the whole of its children, which is what
	// a cut list means. Explicit null rather than absent.
	SourceRange *otioRange `json:"source_range"`
}

type otioStack struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Name        string         `json:"name"`
	Metadata    map[string]any `json:"metadata"`
	Effects     []any          `json:"effects"`
	Markers     []any          `json:"markers"`
	Children    []otioTrack    `json:"children"`
	SourceRange *otioRange     `json:"source_range"`
}

type otioTimeline struct {
	Schema          string         `json:"OTIO_SCHEMA"`
	Name            string         `json:"name"`
	Metadata        map[string]any `json:"metadata"`
	GlobalStartTime otioTime       `json:"global_start_time"`
	Tracks          otioStack      `json:"tracks"`
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
