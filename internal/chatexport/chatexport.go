// Package chatexport turns stored chat into files an analyst can use: JSON and
// WebVTT saved beside a recording, and JSON, CSV and NDJSON streamed on demand.
//
// Chat is a bounded replay buffer (internal/db/chat.go) and is purged, so the
// durable copy is a sidecar written when a recording finishes. Everything here
// reads through Source one page at a time and writes straight to an io.Writer,
// so a day-long recording never sits in memory.
//
// THE CLOCK IS THE STORED MESSAGE TIME. Every offset and every absolute time is
// chat_messages.at_ms, the same clock the highlight scanner lines chat up with a
// recording by. It is what the adapter put in Message.At: the platform's own
// sent time where the adapter has one (Twitch's tmi-sent-ts, for one), and the
// moment this server received the message where it has none (Normalise). So it
// is NOT uniformly the arrival time, and platform clocks differ from ours and
// from each other by whatever skew they carry.
package chatexport

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// SchemaVersion is the version of the JSON sidecar layout. Bump it when a field
// changes meaning; adding a field does not need one.
const SchemaVersion = 1

// Sidecar file suffixes, appended to the recording's name without its extension.
const (
	JSONSuffix = ".chat.json"
	VTTSuffix  = ".chat.vtt"
)

// cueSeconds is how long a WebVTT cue stays on screen. Cues are allowed to
// overlap, so a burst stacks in the player instead of replacing itself.
const cueSeconds = 5

// generator names the writer in every header. main sets it to the build version.
var generator = "polyemesis"

// SetGenerator records the build version for headers. Call it once at startup,
// before any export runs.
func SetGenerator(version string) {
	if v := strings.TrimSpace(version); v != "" {
		generator = "polyemesis " + v
	}
}

// SidecarNames returns the JSON and VTT sidecar names for a recording file.
func SidecarNames(recording string) (jsonName, vttName string) {
	base := filepath.Base(recording)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base + JSONSuffix, base + VTTSuffix
}

// SidecarBase returns the recording base a sidecar name belongs to, and false
// for a name that is not a sidecar.
func SidecarBase(name string) (string, bool) {
	for _, suf := range []string{JSONSuffix, VTTSuffix} {
		if b, ok := strings.CutSuffix(name, suf); ok && b != "" {
			return b, true
		}
	}
	return "", false
}

// Source is the chat store as an export reads it. *db.DB satisfies it.
type Source interface {
	ChatRangeSummary(db.ChatRange) (db.ChatRangeSummary, error)
	ForEachChatMessage(db.ChatRange, func(db.ChatMessage) error) error
	ChatOldest() (time.Time, bool, error)
}

// ErrNotRetained means the window held no stored chat and the purge may have
// taken it, so an empty answer would read as "nobody chatted".
var ErrNotRetained = errors.New("chat for this period is no longer stored")

// RecordingInfo is the recording a sidecar belongs to.
type RecordingInfo struct {
	File       string    `json:"file"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	// EndSource says where DurationMs came from: "probe" (ffprobe's measurement
	// of the finished file) or "mtime" (the file's last write, for a segment
	// that was never measured, such as the last one of a stream).
	EndSource string `json:"endSource"`
	// StartPrecision is the resolution of StartedAt. The recorder stamps the
	// segment's start into its file name in whole seconds; nothing finer is
	// stored.
	StartPrecision string `json:"startPrecision"`
}

// Header is the small description at the top of a JSON export.
type Header struct {
	Schema      int            `json:"schema"`
	Generator   string         `json:"generator"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Recording   *RecordingInfo `json:"recording,omitempty"`
	// From and To are the window the messages were taken from, [From, To).
	From         time.Time  `json:"from"`
	To           time.Time  `json:"to"`
	Platforms    []string   `json:"platforms"`
	MessageCount int        `json:"messageCount"`
	Clock        string     `json:"clock"`
	Excludes     []string   `json:"excludes"`
	Complete     bool       `json:"complete"`
	RetainedFrom *time.Time `json:"retainedFrom,omitempty"`
}

// Entry is one message in an export.
type Entry struct {
	// OffsetMs is the stored message time minus the recording's start. Absent in an
	// export that is not tied to a recording.
	OffsetMs  *int64    `json:"offsetMs,omitempty"`
	At        time.Time `json:"at"`
	AtMs      int64     `json:"atMs"`
	Platform  string    `json:"platform"`
	Account   string    `json:"account,omitempty"`
	Channel   string    `json:"channel,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
	AuthorID  string    `json:"authorId,omitempty"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	Roles     []string  `json:"roles"`
	Badges    []Badge   `json:"badges"`
	ReplyToID string    `json:"replyToId,omitempty"`
	ReplyTo   string    `json:"replyTo,omitempty"`
}

// Badge is one platform badge as the chat store keeps it.
type Badge struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Label   string `json:"label,omitempty"`
}

// Feed delivers entries to emit, oldest first, stopping at the first error.
type Feed func(emit func(Entry) error) error

// Plan is a header and the feed of messages it describes.
type Plan struct {
	Header Header
	Feed   Feed
}

// EntryOf converts a stored message. startMS is the recording start the offset
// is taken from, or zero for an export with no recording.
func EntryOf(m db.ChatMessage, startMS int64, withOffset bool) Entry {
	at := m.At.UTC()
	e := Entry{
		At: at, AtMs: m.At.UnixMilli(),
		Platform: string(m.Platform), Account: m.Account, Channel: m.Channel,
		MessageID: m.MessageID, AuthorID: m.AuthorID, Author: m.AuthorName, Text: m.Text,
		Roles: rolesOf(m), Badges: badgesOf(m.Badges),
		ReplyToID: m.ReplyToID, ReplyTo: m.ReplyTo,
	}
	if withOffset {
		off := e.AtMs - startMS
		e.OffsetMs = &off
	}
	return e
}

func rolesOf(m db.ChatMessage) []string {
	roles := []string{}
	if m.Broadcaster {
		roles = append(roles, "broadcaster")
	}
	if m.Moderator {
		roles = append(roles, "moderator")
	}
	if m.Subscriber {
		roles = append(roles, "subscriber")
	}
	return roles
}

// badgesOf decodes the stored badge array. A column that will not decode yields
// none: the message is what the reader came for.
func badgesOf(raw json.RawMessage) []Badge {
	out := []Badge{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// PlanRecording describes the chat that arrived during one recording.
// [startMS, endMS) is the window; see recording.ChatWindow for how it is chosen.
func PlanRecording(src Source, info RecordingInfo, startMS, endMS int64, now time.Time) (Plan, error) {
	p, err := plan(src, db.ChatRange{FromMS: startMS, ToMS: endMS}, startMS, true, now)
	if err != nil {
		return Plan{}, err
	}
	p.Header.Recording = &info
	return p, nil
}

// PlanRange describes the chat in an arbitrary window, optionally one platform.
func PlanRange(src Source, q db.ChatRange, now time.Time) (Plan, error) {
	return plan(src, q, 0, false, now)
}

func plan(src Source, q db.ChatRange, startMS int64, withOffset bool, now time.Time) (Plan, error) {
	sum, err := src.ChatRangeSummary(q)
	if err != nil {
		return Plan{}, err
	}
	oldest, any, err := src.ChatOldest()
	if err != nil {
		return Plan{}, err
	}
	q.MaxID = sum.MaxID
	h := Header{
		Schema: SchemaVersion, Generator: generator, GeneratedAt: now.UTC(),
		From: time.UnixMilli(q.FromMS).UTC(), To: time.UnixMilli(q.ToMS).UTC(),
		Platforms: []string{}, MessageCount: sum.Count,
		Clock:    "stored message time (at_ms): the platform's own timestamp where its adapter supplies one, otherwise when this server received the message; the clock the highlight scanner uses",
		Excludes: []string{"echo", "moderation-removed"},
		// Retained chat is always a suffix of time (see db.ChatOldest), so a
		// window that starts at or after the oldest row lost nothing to the purge.
		Complete: any && !oldest.After(time.UnixMilli(q.FromMS)),
	}
	for _, p := range sum.Platforms {
		h.Platforms = append(h.Platforms, string(p))
	}
	if !h.Complete && any {
		o := oldest.UTC()
		h.RetainedFrom = &o
	}
	feed := func(emit func(Entry) error) error {
		if sum.Count == 0 {
			return nil
		}
		return src.ForEachChatMessage(q, func(m db.ChatMessage) error {
			return emit(EntryOf(m, startMS, withOffset))
		})
	}
	return Plan{Header: h, Feed: feed}, nil
}

// Retained reports whether an export of this plan is worth serving: it holds
// messages, or it is known to be complete (a quiet chat, not a purged one).
func (p Plan) Retained() bool { return p.Header.MessageCount > 0 || p.Header.Complete }

// WriteJSON writes the header and every message as one JSON document, one
// message per line so a very large file can still be read with line tools.
func WriteJSON(w io.Writer, p Plan) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	head, err := marshal(p.Header)
	if err != nil {
		return err
	}
	// Reopen the header object to append the array: the header stays first and
	// the messages are never held together in memory.
	if _, err := bw.Write(bytes.TrimSuffix(head, []byte("}"))); err != nil {
		return err
	}
	if _, err := bw.WriteString(",\"messages\":["); err != nil {
		return err
	}
	n := 0
	err = p.Feed(func(e Entry) error {
		b, err := marshal(e)
		if err != nil {
			return err
		}
		sep := "\n"
		if n > 0 {
			sep = ",\n"
		}
		n++
		if _, err := bw.WriteString(sep); err != nil {
			return err
		}
		_, err = bw.Write(b)
		return err
	})
	if err != nil {
		return err
	}
	if n > 0 {
		if _, err := bw.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := bw.WriteString("]}\n"); err != nil {
		return err
	}
	return bw.Flush()
}

// marshal is json.Marshal without HTML escaping, so "<3" stays "<3" in a file
// people read.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// WriteNDJSON writes one message per line and no header.
func WriteNDJSON(w io.Writer, p Plan) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	err := p.Feed(func(e Entry) error {
		b, err := marshal(e)
		if err != nil {
			return err
		}
		if _, err := bw.Write(b); err != nil {
			return err
		}
		return bw.WriteByte('\n')
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// csvColumns are the CSV header cells, in order. offsetMs is present only in an
// export tied to a recording.
var csvColumns = []string{"at", "atMs", "platform", "account", "channel", "messageId",
	"authorId", "author", "roles", "badges", "text", "replyToId", "replyTo"}

// WriteCSV writes a header row and one row per message.
//
// A cell that begins with = + - @ (or a tab or carriage return) is prefixed
// with an apostrophe, because a spreadsheet would otherwise run it as a formula
// and chat text is typed by strangers. The apostrophe is a visible change to
// the value; platform, time and number columns are fixed formats and left alone.
func WriteCSV(w io.Writer, p Plan, withOffset bool) error {
	cw := csv.NewWriter(w)
	cols := csvColumns
	if withOffset {
		cols = append([]string{"offsetMs"}, csvColumns...)
	}
	if err := cw.Write(cols); err != nil {
		return err
	}
	err := p.Feed(func(e Entry) error {
		row := make([]string, 0, len(cols))
		if withOffset {
			off := int64(0)
			if e.OffsetMs != nil {
				off = *e.OffsetMs
			}
			row = append(row, strconv.FormatInt(off, 10))
		}
		row = append(row,
			e.At.Format(time.RFC3339Nano), strconv.FormatInt(e.AtMs, 10), e.Platform,
			SafeCell(e.Account), SafeCell(e.Channel), SafeCell(e.MessageID),
			SafeCell(e.AuthorID), SafeCell(e.Author),
			strings.Join(e.Roles, "|"), SafeCell(badgeList(e.Badges)),
			SafeCell(e.Text), SafeCell(e.ReplyToID), SafeCell(e.ReplyTo))
		return cw.Write(row)
	})
	if err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

func badgeList(bs []Badge) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		s := b.ID
		if b.Version != "" {
			s += "/" + b.Version
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "|")
}

// SafeCell neutralises a spreadsheet formula. The first character that is not
// leading whitespace decides, because a spreadsheet skips the space.
func SafeCell(s string) string {
	t := strings.TrimLeft(s, " ")
	if t == "" {
		return s
	}
	switch t[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// WriteVTT writes WebVTT cues, "[PLATFORM] name: text", at each message's
// offset from the recording start. A player or editor shows them as subtitles,
// so the chat replays against the video.
func WriteVTT(w io.Writer, p Plan) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	title := "polyemesis chat"
	if r := p.Header.Recording; r != nil {
		title += " for " + r.File
	}
	// A NOTE block may not contain "-->"; vttText escapes the ">" that spells it.
	if _, err := fmt.Fprintf(bw, "WEBVTT\n\nNOTE\n%s\nstarted %s\n\n", vttText(title),
		p.Header.From.Format(time.RFC3339)); err != nil {
		return err
	}
	err := p.Feed(func(e Entry) error {
		var off int64
		if e.OffsetMs != nil {
			off = *e.OffsetMs
		}
		_, err := fmt.Fprintf(bw, "%s --> %s\n[%s] %s: %s\n\n",
			vttStamp(off), vttStamp(off+cueSeconds*1000),
			strings.ToUpper(e.Platform), vttText(e.Author), vttText(e.Text))
		return err
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// vttStamp formats milliseconds as HH:MM:SS.mmm.
func vttStamp(ms int64) string {
	ms = max(ms, 0)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// vttText makes arbitrary text safe inside a cue: the three characters WebVTT
// treats as markup are escaped, and every line break becomes a space because a
// blank line ends the cue and "-->" would start a new timing line.
func vttText(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	// Fields splits on every Unicode space, line breaks included, so a run of
	// them collapses to one space.
	return strings.Join(strings.Fields(s), " ")
}
