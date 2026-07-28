package transcribe

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parsing whisper.cpp's two outputs.
//
// The JSON file is authoritative — it carries millisecond offsets as integers
// and, with --output-json-full, per-token probabilities. The stdout lines are
// the fallback for a build too old to write JSON and the source of live
// progress while the run is in flight. Both are parsed because a transcription
// that ran for forty minutes and then could not be read is forty minutes of a
// live machine's spare CPU thrown away.

// whisperJSON is the shape whisper.cpp writes with -oj / -ojf. Only the fields
// we use are declared; the file also carries model and parameter blocks that
// are of no interest once the job has recorded what it ran.
type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Params struct {
		Language string `json:"language"`
	} `json:"params"`
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Timestamps struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"timestamps"`
		Text   string `json:"text"`
		Tokens []struct {
			Text string  `json:"text"`
			P    float64 `json:"p"`
		} `json:"tokens"`
	} `json:"transcription"`
}

// ParseJSON reads whisper.cpp's JSON output into segments.
//
// Offsets are preferred over the formatted timestamps because they are already
// integers in milliseconds; the timestamp strings are only consulted when the
// offsets are absent, which happens on some older builds for the very first
// segment.
func ParseJSON(raw []byte, lang *string) ([]Segment, error) {
	var doc whisperJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse whisper json: %w", err)
	}
	if lang != nil {
		if doc.Result.Language != "" {
			*lang = doc.Result.Language
		} else if doc.Params.Language != "" && doc.Params.Language != "auto" {
			*lang = doc.Params.Language
		}
	}
	out := make([]Segment, 0, len(doc.Transcription))
	for _, t := range doc.Transcription {
		seg := Segment{
			StartMS: t.Offsets.From,
			EndMS:   t.Offsets.To,
			Text:    strings.TrimSpace(t.Text),
		}
		if seg.StartMS == 0 && seg.EndMS == 0 && t.Timestamps.From != "" {
			if ms, ok := ParseTimestamp(t.Timestamps.From); ok {
				seg.StartMS = ms
			}
			if ms, ok := ParseTimestamp(t.Timestamps.To); ok {
				seg.EndMS = ms
			}
		}
		if c, ok := meanTokenProbability(t.Tokens); ok {
			seg.Confidence, seg.ConfidenceKnown = c, true
		}
		out = append(out, seg)
	}
	return NormalizeSegments(out), nil
}

// meanTokenProbability averages the token probabilities into a segment
// confidence.
//
// Whisper's special tokens — the [_BEG_] marker and the [_TT_nnn] timestamp
// tokens — carry probabilities that describe the model's segmentation, not its
// transcription, and including them drags a perfectly confident sentence down
// or props up a hopeless one. A segment of nothing but special tokens has no
// confidence rather than a confidence of zero.
func meanTokenProbability(tokens []struct {
	Text string  `json:"text"`
	P    float64 `json:"p"`
}) (float64, bool) {
	var sum float64
	var n int
	for _, tk := range tokens {
		if strings.HasPrefix(tk.Text, "[_") {
			continue
		}
		sum += tk.P
		n++
	}
	if n == 0 {
		return 0, false
	}
	c := sum / float64(n)
	if c < 0 {
		c = 0
	}
	if c > 1 {
		c = 1
	}
	return c, true
}

// segmentLineRE matches one of whisper.cpp's stdout lines:
//
//	[00:00:00.000 --> 00:00:02.000]   Hello world
//
// The separator inside a timestamp is a period on stdout, but some builds print
// a comma, so both are accepted here. This is the read side; the write side in
// subtitles.go is strict about which character belongs to which format.
var segmentLineRE = regexp.MustCompile(`^\[(\d{1,2}:\d{2}:\d{2}[.,]\d{3})\s*-->\s*(\d{1,2}:\d{2}:\d{2}[.,]\d{3})\]\s*(.*)$`)

// ansiRE strips the colour codes whisper emits under --print-colors, which
// otherwise end up inside the transcript text.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ParseSegmentLine reads one stdout line. ok is false for the many lines that
// are not segments — banners, timings, warnings.
func ParseSegmentLine(line string) (Segment, bool) {
	line = strings.TrimRight(ansiRE.ReplaceAllString(line, ""), "\r\n")
	m := segmentLineRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Segment{}, false
	}
	start, ok1 := ParseTimestamp(m[1])
	end, ok2 := ParseTimestamp(m[2])
	if !ok1 || !ok2 {
		return Segment{}, false
	}
	text := strings.TrimSpace(m[3])
	if text == "" {
		return Segment{}, false
	}
	return Segment{StartMS: start, EndMS: end, Text: text}, true
}

// ParseStdout reads a whole whisper stdout capture into segments.
func ParseStdout(s string) []Segment {
	var out []Segment
	for _, line := range strings.Split(s, "\n") {
		if seg, ok := ParseSegmentLine(line); ok {
			out = append(out, seg)
		}
	}
	return NormalizeSegments(out)
}

// timestampRE matches HH:MM:SS with a three-digit fraction after either
// separator. Whisper prints hours unpadded on some builds, hence {1,2}.
var timestampRE = regexp.MustCompile(`^(\d{1,3}):(\d{2}):(\d{2})[.,](\d{3})$`)

// ParseTimestamp converts an SRT/WebVTT-shaped timestamp to milliseconds. It
// accepts both decimal separators, which is the right choice for a reader: the
// strictness belongs on the writing side, where getting it wrong is silent.
func ParseTimestamp(s string) (int64, bool) {
	m := timestampRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.ParseInt(m[1], 10, 64)
	min, _ := strconv.ParseInt(m[2], 10, 64)
	sec, _ := strconv.ParseInt(m[3], 10, 64)
	ms, _ := strconv.ParseInt(m[4], 10, 64)
	if min > 59 || sec > 59 {
		return 0, false
	}
	return h*3600000 + min*60000 + sec*1000 + ms, true
}

// progressRE matches whisper.cpp's progress callback line:
//
//	whisper_print_progress_callback: progress =  35%
var progressRE = regexp.MustCompile(`progress\s*=\s*(\d{1,3})\s*%`)

// ParseProgressLine extracts a 0..1 fraction from a whisper progress line.
func ParseProgressLine(line string) (float64, bool) {
	m := progressRE.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct / 100, true
}

// errorLineRE recognises the messages worth putting in the job's log tail. A
// forty-minute run must not push its own failure reason out of a bounded tail
// with routine chatter, so only lines that look like a problem are kept.
var errorLineRE = regexp.MustCompile(`(?i)\b(error|failed|failure|cannot|unable to|not found|out of memory|invalid)\b`)

// IsNoteworthy reports whether a line from whisper's stderr belongs in the
// job log.
func IsNoteworthy(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return errorLineRE.MatchString(line)
}
