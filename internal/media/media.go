// Package media turns a finished recording into the derived files a library
// needs: a low-bitrate proxy the browser can actually scrub, poster and
// contact-sheet thumbnails, a sprite sheet with its WebVTT index, and — opt-in
// — a smaller, lossy archive copy of an old recording.
//
// None of it happens inline. Every function here is either a pure argument
// builder or a jobs.Worker, because all three of these are CPU-hungry FFmpeg
// runs and the live stream owns the machine. The queue and its governor decide
// WHEN; this package only knows HOW.
//
// Two rules run through the whole file set:
//
//   - The multitrack master is sacred. The proxy is a derived navigation copy
//     and is allowed to carry one audio track; the archive re-encode is the
//     only thing here that may ever touch the master, and it must keep every
//     audio track or it does not ship. See archive.go.
//   - Derived files live in their own subdirectory. internal/recording scans
//     the recordings directory flat and treats every .mkv, .mp4 and .ts it
//     finds as a segment — a proxy written beside its master would be adopted
//     as a recording, indexed, and eventually swept by retention as if it were
//     one. Same reasoning as clips.Subdir and recording.StemsSubdir.
package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// Subdir is the recordings-directory child every derived file is written
// under. See the package comment for why this is not optional.
const Subdir = "media"

// Job kinds. The queue never interprets one, but they are stored in the
// database and named in the resource policy, so they are spelled once here.
const (
	// KindProxy generates the browser-playable scrubbing copy.
	KindProxy jobs.Kind = "media.proxy"
	// KindThumbnails generates poster, contact sheet, sprite sheet and VTT in
	// one pass.
	KindThumbnails jobs.Kind = "media.thumbnails"
	// KindArchive re-encodes an old recording smaller. This is the one kind in
	// the product that destroys data, which is why it is opt-in twice over.
	KindArchive jobs.Kind = "media.archive"
)

// Derived filenames. Fixed rather than derived from the recording's name: the
// per-recording directory already carries the identity, and a fixed name means
// the web layer can build a URL without a database round trip.
const (
	ProxyName        = "proxy.mp4"
	PosterName       = "poster.jpg"
	ContactSheetName = "contact.jpg"
	SpritePattern    = "sprite-%03d.jpg"
	SpriteVTTName    = "sprite.vtt"
	ArchiveBase      = "archive"
)

// PartialSuffix marks a file that is still being written.
//
// Every output in this package is written to <final><PartialSuffix> and renamed
// into place, for the same reason clips does it: a half-written proxy that a
// browser starts playing, or a half-written archive that a verifier measures,
// is a bug report about a corrupt file. It also makes the crash story simple —
// anything ending in .partial is garbage from a dead process.
const PartialSuffix = ".partial"

// Layout is where one recording's derived files live.
type Layout struct {
	// Name is the master recording's index filename.
	Name string
	// Dir is the per-recording directory holding everything below.
	Dir string

	Proxy         string
	Poster        string
	ContactSheet  string
	SpritePattern string
	SpriteVTT     string
	// Archive keeps the master's own extension: the verified output is renamed
	// over the original in place when replacement is asked for, and a .ts
	// segment must not come back as a file named .ts containing Matroska.
	Archive string
}

// DerivedDir is where every recording's derived media lives.
func DerivedDir(recordingsDir string) string {
	return filepath.Join(recordingsDir, Subdir)
}

// LayoutFor resolves the derived paths for one recording.
//
// recordingName is the index filename, not a path. It is joined after being
// reduced to its base name, because it ultimately originates from a database
// row and this package writes and deletes files by it.
func LayoutFor(recordingsDir, recordingName string) Layout {
	name := filepath.Base(strings.TrimSpace(recordingName))
	dir := filepath.Join(DerivedDir(recordingsDir), stripExt(name))
	return Layout{
		Name:          name,
		Dir:           dir,
		Proxy:         filepath.Join(dir, ProxyName),
		Poster:        filepath.Join(dir, PosterName),
		ContactSheet:  filepath.Join(dir, ContactSheetName),
		SpritePattern: filepath.Join(dir, SpritePattern),
		SpriteVTT:     filepath.Join(dir, SpriteVTTName),
		Archive:       filepath.Join(dir, ArchiveBase+filepath.Ext(name)),
	}
}

func stripExt(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// ValidRecordingName reports whether a name is one this package will touch.
//
// Deliberately narrow, and checked before any path is built: the name arrives
// from an HTTP request or a job's params, and everything downstream of here
// creates and removes directories.
func ValidRecordingName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	// ContainsAny over BOTH separators, spelled literally.
	//
	// The previous form -- '/' or os.PathSeparator -- reads as "both", and is
	// both only on Windows: on Linux os.PathSeparator IS '/', so the condition
	// collapsed to one check and a backslash was accepted. That is not an
	// escape on Linux, where a backslash is a legal filename character, but
	// this name is stored and the same data directory opened from a Windows
	// build reads it as a path.
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return !strings.ContainsAny(name, "\x00\n\r")
}

// Resolve turns a recording name and a derived filename into an absolute path,
// refusing anything that escapes the derived directory.
//
// Every filesystem operation that takes a name from outside this package goes
// through it. Mirrors clips.Resolve and recording.Manager.Resolve rather than
// inventing a third shape.
func Resolve(recordingsDir, recordingName, file string) (string, error) {
	if !ValidRecordingName(recordingName) {
		return "", fmt.Errorf("invalid recording name %q", recordingName)
	}
	if !ValidRecordingName(file) {
		return "", fmt.Errorf("invalid derived media name %q", file)
	}
	base, err := filepath.Abs(LayoutFor(recordingsDir, recordingName).Dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, file)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("derived media %q escapes its recording's directory", file)
	}
	return full, nil
}

// Remove deletes every derived file for one recording. Called when the master
// is deleted by hand or by retention: a proxy whose master is gone is dead
// weight nothing will ever ask for again.
func Remove(recordingsDir, recordingName string) error {
	if !ValidRecordingName(recordingName) {
		return fmt.Errorf("invalid recording name %q", recordingName)
	}
	dir := LayoutFor(recordingsDir, recordingName).Dir
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Sweep deletes derived media whose master recording is no longer indexed, and
// returns the recording names it cleaned up.
//
// known is the index, keyed by recording filename. An EMPTY index means "we
// cannot tell", not "everything is orphaned", and sweeps nothing — the same
// guard recording.SweepStems uses, and for the same reason: the index is
// rebuilt by a scan that may not have run yet, and a wrong answer here throws
// away work nobody asked to throw away.
func Sweep(recordingsDir string, known map[string]bool) ([]string, error) {
	if len(known) == 0 {
		return nil, nil
	}
	root := DerivedDir(recordingsDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// The directory is named after the master minus its extension, and the
	// index is keyed on the full filename, so the comparison is done in the
	// direction that cannot guess: build the set of surviving directory names
	// from the index rather than trying to reconstruct an extension.
	survives := make(map[string]bool, len(known))
	for name := range known {
		survives[stripExt(filepath.Base(name))] = true
	}

	var removed []string
	for _, e := range entries {
		if !e.IsDir() || survives[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			// One unreadable directory must not stop the rest of the sweep.
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// Bytes totals what derived media occupies.
//
// The recordings index counts masters only, so without this a library with
// proxies for every segment under-reports its own footprint by a tenth or so —
// small per file, and the direction that quietly fills a volume.
func Bytes(recordingsDir string) (int64, error) {
	var total int64
	root := DerivedDir(recordingsDir)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			// Skip what cannot be read rather than failing the whole total; a
			// disk figure that is slightly low beats a disk page that errors.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return total, err
	}
	return total, nil
}

// ---------------------------------------------------------------- arg helpers

// commonArgs are the flags every child process here gets.
//
// It mirrors internal/ffmpeg's unexported commonArgs deliberately rather than
// importing it, because that one is not exported; the -y is the addition. Every
// output in this package is written to a .partial path, and a .partial left
// behind by a killed process must not make the retry hang on FFmpeg's
// interactive overwrite prompt.
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-y"}
}

// progressArgs routes machine-readable stats to stdout, leaving stderr as a
// pure human log — the same split internal/ffmpeg uses, so ffmpeg.ParseProgress
// reads our children too.
func progressArgs() []string {
	return []string{"-nostats", "-progress", "pipe:1"}
}

// evenExpr wraps an FFmpeg expression so its value lands on an even number of
// pixels, which 4:2:0 chroma subsampling requires of every dimension.
func evenExpr(expr string) string { return "2*floor(" + expr + "/2)" }

// filterColor keeps operator text off the filter graph unless it is
// unmistakably a colour.
//
// The value lands inside a filter argument, where a comma or a colon would
// silently re-cut the whole chain into different filters. Anything outside
// FFmpeg's colour vocabulary becomes black rather than an error. Same rule and
// same reasoning as ffmpeg.padColor; duplicated because that one is unexported.
func filterColor(c string) string {
	c = strings.TrimSpace(c)
	if c == "" || len(c) > 24 {
		return "black"
	}
	for i, r := range c {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '#' && i == 0:
		default:
			return "black"
		}
	}
	return c
}

// fitTileFilter scales a frame to fit inside w x h and pads it back out to
// exactly w x h.
//
// The pad is not cosmetic. A sprite sheet's WebVTT addresses each thumbnail by
// pixel rectangle, so every tile has to be the same size to the pixel; a plain
// scale=-2:h on a source whose aspect differs from the tile's would slide every
// later cue sideways and show the viewer a sliver of the next frame.
//
// force_divisible_by=2 keeps the derived side even, which is a start failure
// rather than a warning on an encoder that wants 4:2:0.
func fitTileFilter(w, h int, color string) string {
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:%s:%s:%s",
		w, h, w, h, evenExpr("(ow-iw)/2"), evenExpr("(oh-ih)/2"), color)
}

// scaleFilter sizes a frame, or returns "" when neither dimension is set — a
// no-op scale still costs a colour-space round trip.
//
// -2 rather than -1 for the derived dimension: -1 can land on an odd number and
// an H.264 encoder then refuses to open.
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

// formatSeconds renders a duration for a filter argument without an exponent or
// a trailing run of zeros. FFmpeg parses "1e-06" as an expression rather than a
// number in some positions, which fails at graph configuration time.
func formatSeconds(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
