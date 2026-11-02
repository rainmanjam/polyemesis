package analytics

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"
)

// Stream is one on-air span: from the first tick some destination was running
// to the tick that found none for the close grace.
type Stream struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// DurationMs is EndedAt (or, for the open stream, the last tick) minus
	// StartedAt.
	DurationMs int64 `json:"durationMs"`
	// PeakTotal and AverageTotal are carried on the list so it can be read
	// without opening each report. Nil when nothing reported.
	PeakTotal    *int     `json:"peakTotal,omitempty"`
	AverageTotal *float64 `json:"averageTotal,omitempty"`
}

// ChatCount is one platform's chat messages during a stream.
type ChatCount struct {
	Platform string `json:"platform"`
	Messages int    `json:"messages"`
}

// RecordingLink is a recording that overlaps the stream.
type RecordingLink struct {
	ID         int64     `json:"id"`
	Filename   string    `json:"filename"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	// Transcript says whether the recording has one. The words themselves are
	// behind the library's own (read-denied) transcript route.
	Transcript bool `json:"transcript"`
}

// ClipLink is a clip captured during the stream.
type ClipLink struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Seconds   float64   `json:"seconds"`
}

// Report is one stream's whole report.
type Report struct {
	Stream  Stream  `json:"stream"`
	Summary Summary `json:"summary"`
	// Chat is messages per platform. Polyemesis's own echoes are not counted.
	Chat []ChatCount `json:"chat"`
	// TopChatters is the most active authors, most messages first.
	TopChatters []Chatter `json:"topChatters"`
	// Player is the public player's QoE over the stream. Nil when the player
	// was not running, or when its counters were reset mid-stream (then a
	// delta would be invented).
	Player     *PlayerQoE      `json:"player,omitempty"`
	Recordings []RecordingLink `json:"recordings"`
	Clips      []ClipLink      `json:"clips"`
}

// NotReported is what a CSV cell says for a count nobody sent. Text, not an
// empty cell: a spreadsheet sums an empty cell as 0, which is the false zero
// this package refuses everywhere else.
const NotReported = "not reported"

// WriteCSV writes the report as CSV: one block per table, a blank line apart,
// each block with its own header row.
func WriteCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	write := func(rec ...string) {
		for i := range rec {
			rec[i] = neutralise(rec[i])
		}
		_ = cw.Write(rec)
	}
	blank := func() { _ = cw.Write([]string{}) }

	ended := ""
	if r.Stream.EndedAt != nil {
		ended = r.Stream.EndedAt.UTC().Format(time.RFC3339)
	}
	write("stream_id", "title", "started_at", "ended_at", "duration_seconds")
	write(strconv.FormatInt(r.Stream.ID, 10), r.Stream.Title,
		r.Stream.StartedAt.UTC().Format(time.RFC3339), ended,
		strconv.FormatInt(r.Stream.DurationMs/1000, 10))
	blank()

	write("platform", "account", "name", "samples", "reported_samples", "peak_viewers", "average_viewers")
	for _, row := range r.Summary.Rows {
		write(row.Platform, row.Account, row.Name, strconv.Itoa(row.Samples), strconv.Itoa(row.Reported),
			intCell(row.Peak), floatCell(row.Average))
	}
	write("total", "", "", "", "", intCell(r.Summary.PeakTotal), floatCell(r.Summary.AverageTotal))
	blank()

	write("chat_platform", "messages")
	for _, c := range r.Chat {
		write(c.Platform, strconv.Itoa(c.Messages))
	}
	blank()

	write("chatter_platform", "chatter", "messages")
	for _, c := range r.TopChatters {
		write(c.Platform, c.Name, strconv.Itoa(c.Messages))
	}

	if p := r.Player; p != nil {
		blank()
		write("player_sessions", "player_startups", "player_rebuffers", "player_sessions_with_errors")
		write(strconv.FormatInt(p.Sessions, 10), strconv.FormatInt(p.Startups, 10),
			strconv.FormatInt(p.Rebuffers, 10), strconv.FormatInt(p.SessionsWithErrors, 10))
	}

	if len(r.Recordings) > 0 {
		blank()
		write("recording_id", "filename", "started_at", "duration_seconds", "transcript")
		for _, rec := range r.Recordings {
			write(strconv.FormatInt(rec.ID, 10), rec.Filename, rec.StartedAt.UTC().Format(time.RFC3339),
				strconv.FormatInt(rec.DurationMs/1000, 10), strconv.FormatBool(rec.Transcript))
		}
	}
	if len(r.Clips) > 0 {
		blank()
		write("clip", "created_at", "seconds")
		for _, c := range r.Clips {
			write(c.Name, c.CreatedAt.UTC().Format(time.RFC3339), strconv.FormatFloat(c.Seconds, 'f', 1, 64))
		}
	}

	cw.Flush()
	return cw.Error()
}

func intCell(v *int) string {
	if v == nil {
		return NotReported
	}
	return strconv.Itoa(*v)
}

func floatCell(v *float64) string {
	if v == nil {
		return NotReported
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

// neutralise stops a cell being read as a formula. A chatter's name and a
// broadcast title are typed by somebody else, and a spreadsheet that opens
// "=HYPERLINK(...)" from a CSV runs it. A leading apostrophe is the
// spreadsheet's own "this is text" marker (OWASP's CSV injection guidance).
// A negative number never reaches here: every count this file writes is >= 0.
func neutralise(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
