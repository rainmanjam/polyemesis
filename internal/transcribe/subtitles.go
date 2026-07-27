package transcribe

import (
	"fmt"
	"strings"
)

// Subtitle emission.
//
// SRT and WebVTT look almost identical and differ in exactly the places that
// make a wrong file fail silently rather than loudly: SRT separates the seconds
// from the milliseconds with a COMMA and WebVTT with a PERIOD. A player handed
// the wrong one does not report an error; it shows no subtitles, or shows them
// with every cue at 00:00. That is why the two formatters are separate
// functions with separate tests instead of one function with a flag.

// SubtitleFormat names an emitted subtitle file type.
type SubtitleFormat string

const (
	FormatSRT SubtitleFormat = "srt"
	FormatVTT SubtitleFormat = "vtt"
	// FormatJSON is our own structured transcript, which is the only format
	// that carries track index, speaker and confidence.
	FormatJSON SubtitleFormat = "json"
	// FormatText is running prose, for the people who just want to grep it.
	FormatText SubtitleFormat = "txt"
)

// SubtitleFormats is the catalogue a UI offers, in the order it should show it.
func SubtitleFormats() []SubtitleFormat {
	return []SubtitleFormat{FormatSRT, FormatVTT, FormatJSON, FormatText}
}

// ValidFormat reports whether f is a format this build can write.
func ValidFormat(f SubtitleFormat) bool {
	switch f {
	case FormatSRT, FormatVTT, FormatJSON, FormatText:
		return true
	}
	return false
}

// Ext is the filename extension for a format, dot included.
func (f SubtitleFormat) Ext() string { return "." + string(f) }

// SubtitleOptions tunes what goes into a cue body.
type SubtitleOptions struct {
	// Speakers prefixes each cue with "Name: ". It is off by default because a
	// single-track transcript would prefix every line with the same name, and on
	// for the merged multi-track file, where it is the entire point.
	Speakers bool
	// MinDurationMS lengthens a cue that is too short to read. Whisper happily
	// emits a 40 ms cue for "Mm.", which flashes and is gone. Zero disables it.
	MinDurationMS int64
}

// srtTimeFormat and vttTimeFormat differ ONLY in the decimal separator. Both
// are HH:MM:SS with a three-digit fractional part and zero-padded to two digits
// on the hours: "0:00:01.5" is accepted by some players and rejected by others,
// and 00:00:01.500 is accepted by all of them.
const (
	srtTimeFormat = "%02d:%02d:%02d,%03d"
	vttTimeFormat = "%02d:%02d:%02d.%03d"
)

// FormatSRTTime renders milliseconds as an SRT timestamp: 00:00:01,500.
func FormatSRTTime(ms int64) string { return formatTime(srtTimeFormat, ms) }

// FormatVTTTime renders milliseconds as a WebVTT timestamp: 00:00:01.500.
func FormatVTTTime(ms int64) string { return formatTime(vttTimeFormat, ms) }

func formatTime(layout string, ms int64) string {
	// A negative timestamp is not representable in either format, and "-1:59:59"
	// is parsed by players as a large positive time rather than being rejected.
	// Clamping is the only reading that cannot corrupt the rest of the file.
	if ms < 0 {
		ms = 0
	}
	h := ms / 3600000
	m := (ms % 3600000) / 60000
	s := (ms % 60000) / 1000
	frac := ms % 1000
	// Hours deliberately overflow past two digits rather than wrapping: a
	// 100-hour recording is absurd but a cue that silently jumps back to zero
	// is worse than an ugly timestamp.
	return fmt.Sprintf(layout, h, m, s, frac)
}

// SRT renders segments as a SubRip file.
//
// Cues are numbered from 1, blocks are separated by a blank line and the file
// ends with one. Line endings are LF: the SubRip convention is CRLF, but every
// player in circulation accepts LF, and emitting CRLF on a Unix host produces a
// file that looks corrupted in every text editor the operator will open it in.
func SRT(segs []Segment, opt SubtitleOptions) string {
	var b strings.Builder
	n := 0
	for _, s := range applyCueRules(segs, opt) {
		n++
		fmt.Fprintf(&b, "%d\n", n)
		fmt.Fprintf(&b, "%s --> %s\n", FormatSRTTime(s.StartMS), FormatSRTTime(s.EndMS))
		b.WriteString(cueBody(s, opt, false))
		b.WriteString("\n\n")
	}
	return b.String()
}

// VTT renders segments as a WebVTT file.
//
// The WEBVTT header line is mandatory — a file without it is not parsed at all,
// which is the single most common way a hand-written VTT fails. Cue payloads are
// HTML-escaped because WebVTT treats < and & as markup, so a transcript
// containing "R&D" or "<laughs>" would otherwise lose text on screen.
func VTT(segs []Segment, opt SubtitleOptions) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	n := 0
	for _, s := range applyCueRules(segs, opt) {
		n++
		fmt.Fprintf(&b, "%d\n", n)
		fmt.Fprintf(&b, "%s --> %s\n", FormatVTTTime(s.StartMS), FormatVTTTime(s.EndMS))
		b.WriteString(cueBody(s, opt, true))
		b.WriteString("\n\n")
	}
	return b.String()
}

// applyCueRules normalises and then applies the presentation-only fixes that
// must not touch the stored transcript: a cue may be stretched to be readable,
// but the JSON must keep the timestamps the model actually produced.
func applyCueRules(segs []Segment, opt SubtitleOptions) []Segment {
	out := NormalizeSegments(segs)
	if opt.MinDurationMS <= 0 {
		return out
	}
	for i := range out {
		if out[i].EndMS-out[i].StartMS >= opt.MinDurationMS {
			continue
		}
		end := out[i].StartMS + opt.MinDurationMS
		// Never extend a cue over the start of the next one on the same track:
		// two overlapping cues render stacked or drop one entirely, depending on
		// the player. Across tracks overlap is expected — people interrupt.
		if next, ok := nextOnTrack(out, i); ok && end > next {
			end = next
		}
		if end > out[i].EndMS {
			out[i].EndMS = end
		}
	}
	return out
}

func nextOnTrack(segs []Segment, i int) (int64, bool) {
	for j := i + 1; j < len(segs); j++ {
		if segs[j].Track == segs[i].Track {
			return segs[j].StartMS, true
		}
	}
	return 0, false
}

func cueBody(s Segment, opt SubtitleOptions, escape bool) string {
	text := s.Text
	if escape {
		text = escapeVTT(text)
	}
	if opt.Speakers && s.Speaker != "" {
		speaker := s.Speaker
		if escape {
			speaker = escapeVTT(speaker)
		}
		return speaker + ": " + text
	}
	return text
}

// escapeVTT escapes the three characters WebVTT reads as markup. The ampersand
// must be replaced first or the escapes introduced for < and > get escaped in
// turn and the viewer sees "&amp;lt;".
func escapeVTT(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// PlainText renders segments as prose, one utterance per line, with a speaker
// prefix when asked. No timestamps: this is the format for reading and for
// grep, and the timestamped formats are one function call away.
func PlainText(segs []Segment, opt SubtitleOptions) string {
	var b strings.Builder
	for _, s := range NormalizeSegments(segs) {
		if opt.Speakers && s.Speaker != "" {
			b.WriteString(s.Speaker)
			b.WriteString(": ")
		}
		b.WriteString(s.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
