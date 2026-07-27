package media

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// ProbeArgs inspects a file on disk.
//
// internal/ffmpeg.ProbeArgs is for a LIVE input and carries the timeouts and
// analysis limits a stream needs; a finished file wants neither, and does want
// -show_format for the container duration that the stream probe has no use for.
func ProbeArgs(path string) []string {
	return []string{
		"-hide_banner",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}
}

// DecodeCheckArgs decodes a file all the way through and writes the result
// nowhere, so any error the decoder hits lands on stderr.
//
// `-map 0:v -map 0:a` is the load-bearing part. Without explicit maps FFmpeg's
// default stream selection picks ONE audio track, so a null-output pass over a
// six-track master would decode one track and declare the file healthy while
// track four was corrupt. On a path whose next step deletes the original, a
// check that only looks at part of the file is worse than no check.
//
// -xerror stops at the first error, which is all that is needed: one error is
// already enough to keep the original.
func DecodeCheckArgs(path string) []string {
	return []string{
		"-hide_banner", "-nostdin",
		"-v", "error",
		"-xerror",
		// A full decode of an hour-long file takes minutes, and a progress bar
		// that does not move for minutes is indistinguishable from a hung job.
		"-nostats", "-progress", "pipe:1",
		"-i", path,
		"-map", "0:v", "-map", "0:a",
		"-f", "null", "-",
	}
}

// DecodeErrors turns a decode pass's stderr into a deduplicated list.
//
// At -v error FFmpeg says nothing at all about a healthy file, so every line
// here is a real complaint. Duplicates are collapsed because a corrupt stream
// emits the same line thousands of times and the operator needs the distinct
// problems, not the count.
func DecodeErrors(stderr string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// probeFormat is the slice of ffprobe's -show_format we need. The stream half
// is parsed by ffmpeg.ParseProbe so the two packages cannot drift on what an
// audio track is.
type probeFormat struct {
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

// ParseSummary converts ffprobe JSON into the shape the verifier compares.
//
// bytes overrides the size ffprobe reports when it is non-zero, because the
// caller has already stat'd the file and a stat is the more trustworthy of the
// two — ffprobe reports the size it read, which for a file still being written
// is not the size it ends up.
func ParseSummary(raw []byte, path string, bytes int64) (FileSummary, error) {
	res, err := ffmpeg.ParseProbe(raw)
	if err != nil {
		return FileSummary{}, err
	}
	var f probeFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return FileSummary{}, fmt.Errorf("parse ffprobe format: %w", err)
	}

	s := FileSummary{Path: path, Bytes: bytes}
	if s.Bytes <= 0 {
		s.Bytes, _ = strconv.ParseInt(strings.TrimSpace(f.Format.Size), 10, 64)
	}
	s.DurationSeconds, _ = strconv.ParseFloat(strings.TrimSpace(f.Format.Duration), 64)
	if s.DurationSeconds < 0 {
		s.DurationSeconds = 0
	}
	if res.Video != nil {
		s.VideoCodec = res.Video.Codec
	}
	s.Audio = make([]TrackSummary, 0, len(res.Audio))
	for _, a := range res.Audio {
		s.Audio = append(s.Audio, TrackSummary{
			Index:    a.Index,
			Codec:    a.Codec,
			Channels: a.Channels,
			Language: a.Language,
			Title:    a.Title,
		})
	}
	return s, nil
}
