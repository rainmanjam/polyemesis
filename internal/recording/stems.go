package recording

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// ---------------------------------------------------------------------- stems
//
// This file decides what the per-track stem files are CALLED and where they
// live. ffmpeg.StemRecorderArgs decides how they are written; the split is
// deliberate, because a stem's name has to agree with its codec's extension and
// with the master segment it belongs to, and neither of those is an argument
// builder's business.
//
// Stems live in a subdirectory rather than beside the masters for two reasons.
// Manager.Scan skips directories outright, so a six-track session cannot
// suddenly show forty files on the recordings page; and a subdirectory is one
// os.RemoveAll away for an operator who decides they do not want them, with no
// risk of catching a master in the same glob.

// StemsSubdir is the recordings-directory child every stem is written under.
const StemsSubdir = "stems"

// maxStemNameLen bounds the filename component built from operator free text. A
// name, not a description — and it has to survive being concatenated with a
// timestamp on filesystems with a 255-byte ceiling.
const maxStemNameLen = 32

// stemSweepSlack is how far before the oldest surviving master a stem may start
// and still be kept.
//
// A stem cuts on its segment boundary while the master waits for the next
// keyframe, so the stem belonging to a master legitimately carries an EARLIER
// timestamp. A minute is far more than any GOP and errs toward keeping a stem
// that has already lost its master, which costs disk; the other direction
// deletes audio somebody still has the video for.
const stemSweepSlack = time.Minute

// stemFilenameRE splits a stem filename into the master timestamp it shares and
// the track name appended to it. The prefix is non-greedy so the FIRST
// date-time pair is taken as the stamp, leaving anything after it — including a
// name that itself contains digits or dashes — as the stem name.
var stemFilenameRE = regexp.MustCompile(`^(.+?)-(\d{8}-\d{6})-([a-z0-9][a-z0-9-]*)$`)

// Stem is one ingest audio track destined for its own file.
type Stem struct {
	// Track is the 0-based ingest audio track index.
	Track int
	// Name is the filename component, already reduced to safe characters.
	Name string
	// Codec is what this stem is written as, already resolved: PlanStems may
	// override the requested codec for a track FLAC cannot carry.
	Codec ffmpeg.StemCodec
	// Channels is the track's width as probed, carried through so the argument
	// builder can apply its own guard without re-deriving it.
	Channels int
}

// PlanStems decides one stem per probed ingest audio track.
//
// It plans only tracks the probe actually reported. The recorder writes its
// master and its stems from one process, so a -map naming a track that is not
// there does not cost a stem, it costs the whole archive — this is the one
// place that can prevent it.
func PlanStems(src routing.Source, codec ffmpeg.StemCodec) []Stem {
	if codec == "" {
		codec = ffmpeg.DefaultStemCodec
	}
	tracks := append([]routing.Track(nil), src.Tracks...)
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Index < tracks[j].Index })

	// Two passes so that a duplicated base name suffixes BOTH files. Naming one
	// of two mics "mic" and the other "mic-2" reads as though the first one is
	// somehow the real one.
	counts := map[string]int{}
	for _, t := range tracks {
		if t.Index < 0 {
			continue
		}
		counts[stemBase(src, t.Index)]++
	}

	used := map[string]bool{}
	out := make([]Stem, 0, len(tracks))
	for _, t := range tracks {
		if t.Index < 0 {
			continue
		}
		base := stemBase(src, t.Index)
		name := base
		if counts[base] > 1 {
			// The track number, not a running counter: it ties the file back to
			// the "Track N" the operator sees in the UI, and it does not shift
			// when a different track disappears.
			name = fmt.Sprintf("%s-%d", base, t.Index+1)
		}
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s-%d-%d", base, t.Index+1, n)
		}
		used[name] = true

		c := codec
		// FLAC simply refuses to open above its channel ceiling. Falling back per
		// stem keeps the wide track AND keeps the extension honest, which is why
		// the decision lives here rather than in the argument builder: only the
		// caller that names the file can also change its extension.
		if c == ffmpeg.StemFLAC && t.Channels > ffmpeg.MaxFLACStemChannels {
			c = ffmpeg.StemWAV
		}
		out = append(out, Stem{Track: t.Index, Name: name, Codec: c, Channels: t.Channels})
	}
	return out
}

// stemBase is the human part of a stem's filename.
//
// Role first, label second. A role is a closed vocabulary, so "mic.flac" means
// the same thing on every install and a post-production script can rely on it;
// a label is free text the operator retypes whenever they feel like it. The
// label is still a far better answer than "track3", so it takes second place
// rather than being ignored.
func stemBase(src routing.Source, track int) string {
	if r := src.RoleOf(track); r != routing.RoleUnset && routing.ValidRole(r) {
		if s := sanitizeStemName(string(r)); s != "" {
			return s
		}
	}
	if s := sanitizeStemName(src.LabelOf(track)); s != "" {
		return s
	}
	return fmt.Sprintf("track%d", track+1)
}

// sanitizeStemName reduces operator free text to something that is a filename
// on every platform polyemesis runs on, and that stays legible when a shell
// glob or a DAW import dialog gets hold of it.
//
// This is a whitelist, not an escape: the input reaches a path, and the only
// safe answer to "what should we do with this byte" is to drop anything not
// positively known to be harmless.
func sanitizeStemName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			// Everything else — spaces, dots, slashes, accents, emoji — collapses
			// to the separator. A leading dot would hide the file; a slash would
			// escape the directory.
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if len(out) > maxStemNameLen {
		out = strings.Trim(out[:maxStemNameLen], "-")
	}
	// A label of nothing but punctuation sanitises away entirely, and "" would
	// name the stem after the master with only the extension to tell them apart.
	if out == "" {
		return ""
	}
	// A purely numeric label reads as a track number it is not, and collides
	// with the -N suffix used to disambiguate duplicates. Treat it as no name.
	if strings.Trim(out, "0123456789") == "" {
		return ""
	}
	return out
}

// StemsDir is the stem subdirectory of a recordings directory.
func StemsDir(recordingsDir string) string {
	return filepath.Join(recordingsDir, StemsSubdir)
}

// StemsDir is where this Manager's stems live.
func (m *Manager) StemsDir() string { return StemsDir(m.dir) }

// StemPattern turns the recorder's master output pattern into this stem's.
//
// The stem keeps the master's whole strftime prefix and appends its own name,
// so rec-%Y%m%d-%H%M%S.mkv becomes stems/rec-%Y%m%d-%H%M%S-mic.flac and a
// human sorting the directory sees which session a stem came from without
// consulting anything.
func StemPattern(recordingsDir, masterPattern string, s Stem) string {
	base := filepath.Base(masterPattern)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(StemsDir(recordingsDir), base+"-"+s.Name+s.Codec.Ext())
}

// StemSpecs is the whole plan rendered for ffmpeg.StemRecorderArgs.
func StemSpecs(recordingsDir, masterPattern string, stems []Stem) []ffmpeg.StemSpec {
	out := make([]ffmpeg.StemSpec, 0, len(stems))
	for _, s := range stems {
		out = append(out, ffmpeg.StemSpec{
			Track:    s.Track,
			Path:     StemPattern(recordingsDir, masterPattern, s),
			Codec:    s.Codec,
			Channels: s.Channels,
		})
	}
	return out
}

// EnsureStemsDir creates the stem subdirectory. FFmpeg will not create it
// itself, and every stem output failing to open is a recorder that crash-loops
// without ever writing the master either.
func EnsureStemsDir(recordingsDir string) error {
	return os.MkdirAll(StemsDir(recordingsDir), 0o755)
}

// ParseStemFilename splits a stem filename into the master timestamp it shares
// and the track name, e.g. rec-20240115-143000-mic.flac -> the segment start
// and "mic". It reports false for anything that is not a stem this build wrote.
func ParseStemFilename(name string) (time.Time, string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ffmpeg.StemFLAC.Ext() && ext != ffmpeg.StemWAV.Ext() {
		return time.Time{}, "", false
	}
	mm := stemFilenameRE.FindStringSubmatch(strings.TrimSuffix(name, filepath.Ext(name)))
	if mm == nil {
		return time.Time{}, "", false
	}
	t, err := time.ParseInLocation("20060102-150405", mm[2], time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	return t, mm[3], true
}

// SweepStems deletes stems whose master segment retention has already removed.
//
// Stems are not indexed, so they cannot be swept by the same size and age rules
// as the masters — and they must not be, because a stem's whole value is being
// the master's companion. Instead this follows the master index: retention
// always deletes oldest first, so any stem starting before the oldest surviving
// master is one whose master is gone.
//
// It deletes nothing it does not understand. An empty index is treated as "we
// cannot tell" rather than "everything is orphaned": the index is rebuilt by a
// scan that may not have run yet, and a wrong answer here is unrecoverable.
func (m *Manager) SweepStems() (bool, error) {
	dir := m.StemsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	indexed, err := m.store.ListRecordings()
	if err != nil {
		return false, err
	}
	if len(indexed) == 0 {
		return false, nil
	}
	oldest := indexed[0].StartedAt
	for _, r := range indexed[1:] {
		if r.StartedAt.Before(oldest) {
			oldest = r.StartedAt
		}
	}
	cutoff := oldest.Add(-stemSweepSlack)

	changed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		start, _, ok := ParseStemFilename(e.Name())
		if !ok || !start.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			m.log.Warn("delete orphaned stem", "file", e.Name(), "err", err)
			continue
		}
		m.log.Info("deleted orphaned stem", "file", e.Name(), "reason", "master segment expired")
		changed = true
	}
	return changed, nil
}

// StemBytes totals what the stems occupy on disk.
//
// The recordings index counts masters only, so without this the size cap would
// under-report a stem-enabled install several-fold — exactly the direction that
// fills a volume.
func (m *Manager) StemBytes() (int64, error) {
	entries, err := os.ReadDir(m.StemsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, ok := ParseStemFilename(e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}
