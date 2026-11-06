// Package segname owns the recording segment filename: the one place that
// both writes it and reads it back.
//
// A master segment is rec-s<sourceID>-YYYYMMDD-HHMMSS.mkv. Segments an earlier
// release wrote are rec-YYYYMMDD-HHMMSS.mkv, and they are still on disk, so
// every reader here accepts both. A stem is the master's extension-less name
// with -<track> appended (rec-s3-20240115-143000-mic.flac).
//
// The format lived in four timestamp layouts and three regular expressions
// across the recording and clips packages, and they drifted: the check that
// lets Delete remove the stopped recorder's own last segment kept matching
// only the old name after the recorder moved to the new one, so it never
// fired. Writer and readers now share the constants below.
//
// It is a leaf package, importing only the standard library, so the clips
// package can share the timestamp layout without depending on recording.
package segname

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Prefix starts every master segment's name.
	Prefix = "rec-"
	// SourceTag follows Prefix, then the programme's ID, in segments written
	// since the name began carrying the programme.
	SourceTag = "s"
	// MasterExt is the container the recorder writes masters in.
	MasterExt = ".mkv"
	// StampLayout is the start timestamp as a Go time layout.
	StampLayout = "20060102-150405"
	// stampStrftime is StampLayout as the strftime pattern ffmpeg expands.
	// The two must render the same string for the same instant; the tests
	// pin that.
	stampStrftime = "%Y%m%d-%H%M%S"
	// stampRE matches a rendered stamp's shape.
	stampRE = `\d{8}-\d{6}`
)

var (
	// masterRE splits an extension-less master name into the prefix it shares
	// with its stems (rec, or rec-s3 for a tagged programme) and its start.
	masterRE = regexp.MustCompile(`^(.+?)-(` + stampRE + `)$`)
	// stemRE splits an extension-less stem name into the master prefix, the
	// master timestamp it shares, and the track name appended to it. The
	// prefix is non-greedy so the FIRST date-time pair is taken as the stamp,
	// leaving anything after it -- including a name that itself contains
	// digits or dashes -- as the track name.
	stemRE = regexp.MustCompile(`^(.+?)-(` + stampRE + `)-([a-z0-9][a-z0-9-]*)$`)
)

// Pattern is the strftime output pattern, without a directory, that the
// recorder for sourceID writes master segments to:
// rec-s<sourceID>-%Y%m%d-%H%M%S.mkv.
func Pattern(sourceID int64) string {
	return Prefix + SourceTag + strconv.FormatInt(sourceID, 10) + "-" + stampStrftime + MasterExt
}

// FormatStamp renders t in StampLayout, in t's own location.
func FormatStamp(t time.Time) string { return t.Format(StampLayout) }

// ParseStamp reads a StampLayout timestamp in local time, which is the zone
// ffmpeg's strftime and FormatStamp's callers write it in.
func ParseStamp(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(StampLayout, s, time.Local)
	return t, err == nil
}

// Source reads the programme out of a master segment's filename, and reports
// false for a name that does not carry one -- a segment written before names
// did, or anything the recorder did not write. It never guesses.
func Source(name string) (int64, bool) {
	rest, ok := strings.CutPrefix(filepath.Base(name), Prefix+SourceTag)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	return parseSourceID(digits)
}

// parseSourceID accepts the decimal ID of a real programme: digits only, and
// greater than zero.
func parseSourceID(digits string) (int64, bool) {
	if digits == "" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// IsRecorderSegment reports whether name has exactly the shape of a master
// segment a recorder writes: rec-s<sourceID>-YYYYMMDD-HHMMSS.mkv, or the
// rec-YYYYMMDD-HHMMSS.mkv an earlier release wrote. name is a bare filename;
// a path is not one.
func IsRecorderSegment(name string) bool {
	rest, ok := strings.CutPrefix(name, Prefix)
	if !ok {
		return false
	}
	if rest, ok = strings.CutSuffix(rest, MasterExt); !ok {
		return false
	}
	if tagged, ok := strings.CutPrefix(rest, SourceTag); ok {
		digits, stamp, ok := strings.Cut(tagged, "-")
		if !ok {
			return false
		}
		if _, ok := parseSourceID(digits); !ok {
			return false
		}
		rest = stamp
	}
	return isStamp(rest)
}

// isStamp reports whether s is eight digits, a hyphen and six digits.
func isStamp(s string) bool {
	if len(s) != len(StampLayout) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// TrailingStamp reads the start out of the last two hyphen-separated fields of
// name's extension-less form: rec-s3-20240115-143000.mkv, the legacy
// rec-20240115-143000.mkv, and a file destination's rolled-over
// booth-20240115-143000.mkv all answer.
func TrailingStamp(name string) (time.Time, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) < 3 {
		return time.Time{}, false
	}
	return ParseStamp(parts[len(parts)-2] + "-" + parts[len(parts)-1])
}

// SplitMaster returns a master's stem prefix (rec-s3, or rec for a legacy
// segment) and its start, read from the name exactly as SplitStem reads a
// stem's. filename may carry a directory and an extension.
func SplitMaster(filename string) (string, time.Time, bool) {
	base := filepath.Base(filename)
	mm := masterRE.FindStringSubmatch(strings.TrimSuffix(base, filepath.Ext(base)))
	if mm == nil {
		return "", time.Time{}, false
	}
	t, ok := ParseStamp(mm[2])
	if !ok {
		return "", time.Time{}, false
	}
	return mm[1], t, true
}

// StemName is a stem filename read back into its parts.
type StemName struct {
	// Prefix is the master's prefix: rec-s3, or rec for a legacy segment.
	Prefix string
	// Stamp is the master's timestamp exactly as the name spells it.
	Stamp string
	// Start is Stamp read in local time.
	Start time.Time
	// Track is the stem's own name, appended after the stamp.
	Track string
}

// MasterBase is the extension-less name of the master the stem was recorded
// beside, spelled from the stem's own bytes rather than re-rendered from
// Start, so a local time that does not exist (a DST gap) cannot change it.
func (s StemName) MasterBase() string { return s.Prefix + "-" + s.Stamp }

// SplitStem splits an extension-less stem name into its parts:
// rec-s3-20240115-143000-mic -> rec-s3, 20240115-143000, mic.
func SplitStem(base string) (StemName, bool) {
	mm := stemRE.FindStringSubmatch(base)
	if mm == nil {
		return StemName{}, false
	}
	t, ok := ParseStamp(mm[2])
	if !ok {
		return StemName{}, false
	}
	return StemName{Prefix: mm[1], Stamp: mm[2], Start: t, Track: mm[3]}, true
}
