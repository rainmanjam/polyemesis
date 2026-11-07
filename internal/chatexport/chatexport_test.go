package chatexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

var t0 = time.Date(2026, 3, 4, 20, 0, 0, 0, time.UTC)

func seed(t *testing.T, store *db.DB, msgs ...db.ChatMessage) {
	t.Helper()
	if _, err := store.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}
}

func msg(id string, p db.Platform, at time.Time, author, text string) db.ChatMessage {
	return db.ChatMessage{Platform: p, Account: "acct", MessageID: id, AuthorID: "id-" + author,
		AuthorName: author, Text: text, At: at}
}

func planFor(t *testing.T, store *db.DB) Plan {
	t.Helper()
	info := RecordingInfo{File: "rec-s1-20260304-200000.mkv", StartedAt: t0, DurationMs: 600_000,
		EndSource: "probe", StartPrecision: "1s"}
	p, err := PlanRecording(store, info, t0.UnixMilli(), t0.Add(10*time.Minute).UnixMilli(), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSidecarNamesFollowTheRecording(t *testing.T) {
	j, v := SidecarNames("dir/rec-s1-20260304-200000.mkv")
	if j != "rec-s1-20260304-200000.chat.json" || v != "rec-s1-20260304-200000.chat.vtt" {
		t.Errorf("names = %q %q", j, v)
	}
	if b, ok := SidecarBase(j); !ok || b != "rec-s1-20260304-200000" {
		t.Errorf("SidecarBase(%q) = %q %v", j, b, ok)
	}
	for _, n := range []string{"rec.mkv", ".chat.json", "notes.json"} {
		if _, ok := SidecarBase(n); ok {
			t.Errorf("%q is not a sidecar", n)
		}
	}
}

func TestJSONHeaderMessagesAndFlags(t *testing.T) {
	store := dbtest.Open(t)
	m1 := msg("1", db.PlatformTwitch, t0.Add(2500*time.Millisecond), "ana", "hello <3 & \"quotes\"")
	m1.Subscriber, m1.Moderator = true, true
	m1.Badges = json.RawMessage(`[{"id":"subscriber","version":"12","label":"Sub"}]`)
	m1.ReplyToID, m1.ReplyTo = "0", "bo"
	echo := msg("e", db.PlatformTwitch, t0.Add(time.Second), "me", "ours")
	echo.Echo = true
	seed(t, store, m1,
		msg("2", db.PlatformYouTube, t0.Add(time.Minute), "bo", "second"),
		echo,
		msg("3", db.PlatformTwitch, t0.Add(11*time.Minute), "late", "after"))
	SetGenerator("v9.9.9")
	t.Cleanup(func() { generator = "polyemesis" })

	var buf bytes.Buffer
	if err := WriteJSON(&buf, planFor(t, store)); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema       int       `json:"schema"`
		Generator    string    `json:"generator"`
		From         time.Time `json:"from"`
		Platforms    []string  `json:"platforms"`
		MessageCount int       `json:"messageCount"`
		Complete     bool      `json:"complete"`
		Excludes     []string  `json:"excludes"`
		Recording    struct {
			File           string `json:"file"`
			DurationMs     int64  `json:"durationMs"`
			StartPrecision string `json:"startPrecision"`
		} `json:"recording"`
		Messages []Entry `json:"messages"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if doc.Schema != 1 || doc.Generator != "polyemesis v9.9.9" || doc.MessageCount != 2 ||
		strings.Join(doc.Platforms, ",") != "twitch,youtube" || !doc.From.Equal(t0) ||
		doc.Recording.File != "rec-s1-20260304-200000.mkv" || doc.Recording.StartPrecision != "1s" {
		t.Errorf("header = %+v", doc)
	}
	if doc.Complete {
		t.Error("the table's oldest row is inside the window, so completeness is unproven")
	}
	if len(doc.Messages) != 2 {
		t.Fatalf("messages = %d", len(doc.Messages))
	}
	e := doc.Messages[0]
	if *e.OffsetMs != 2500 || e.Author != "ana" || e.AuthorID != "id-ana" || e.Platform != "twitch" ||
		e.Account != "acct" || strings.Join(e.Roles, ",") != "moderator,subscriber" ||
		len(e.Badges) != 1 || e.Badges[0].Version != "12" || e.ReplyTo != "bo" ||
		e.Text != `hello <3 & "quotes"` || !e.At.Equal(t0.Add(2500*time.Millisecond)) {
		t.Errorf("entry = %+v", e)
	}
	// HTML is not escaped into \\u003c in a file people read.
	if strings.Contains(buf.String(), "\\u003c") {
		t.Errorf("angle brackets were escaped:\n%s", buf.String())
	}
}

func TestCompleteMeansTheTableReachesBackPastTheWindow(t *testing.T) {
	store := dbtest.Open(t)
	seed(t, store,
		msg("old", db.PlatformTwitch, t0.Add(-time.Hour), "x", "before"),
		msg("1", db.PlatformTwitch, t0.Add(time.Minute), "ana", "in"))
	p := planFor(t, store)
	if !p.Header.Complete || p.Header.RetainedFrom != nil || !p.Retained() {
		t.Errorf("header = %+v", p.Header)
	}

	// Chat that starts mid-window: partial, and it says from when.
	store2 := dbtest.Open(t)
	seed(t, store2, msg("1", db.PlatformTwitch, t0.Add(5*time.Minute), "ana", "in"))
	p = planFor(t, store2)
	if p.Header.Complete || p.Header.RetainedFrom == nil || !p.Header.RetainedFrom.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("header = %+v", p.Header)
	}

	// An empty table proves nothing: not worth serving.
	p = planFor(t, dbtest.Open(t))
	if p.Retained() || p.Header.Complete {
		t.Errorf("an empty table is not a quiet chat: %+v", p.Header)
	}
}

func TestVTTCuesAreSafeAndOrdered(t *testing.T) {
	store := dbtest.Open(t)
	seed(t, store,
		msg("1", db.PlatformTwitch, t0.Add(3723456*time.Millisecond/1000), "ana", "a --> b <i>x</i> & y\n\nnew para"),
		msg("2", db.PlatformKick, t0.Add(4*time.Minute), "Zoë", "ok"))
	var buf bytes.Buffer
	if err := WriteVTT(&buf, planFor(t, store)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "WEBVTT\n\nNOTE\npolyemesis chat for rec-s1-20260304-200000.mkv\n") {
		t.Errorf("header:\n%s", out)
	}
	if !strings.Contains(out, "00:00:03.723 --> 00:00:08.723\n[TWITCH] ana: a --&gt; b &lt;i&gt;x&lt;/i&gt; &amp; y new para\n\n") {
		t.Errorf("cue 1:\n%s", out)
	}
	if !strings.Contains(out, "00:04:00.000 --> 00:04:05.000\n[KICK] Zoë: ok\n") {
		t.Errorf("cue 2:\n%s", out)
	}
	// "-->" only ever appears as a timing separator.
	if strings.Count(out, "-->") != 2 {
		t.Errorf("a payload smuggled a cue arrow:\n%s", out)
	}
	if vttStamp(-5) != "00:00:00.000" || vttStamp(360_000_000+1) != "100:00:00.001" {
		t.Errorf("vttStamp edge cases: %q %q", vttStamp(-5), vttStamp(360_000_001))
	}
}

func TestCSVDefusesFormulasAndKeepsFixedColumnsRaw(t *testing.T) {
	store := dbtest.Open(t)
	seed(t, store,
		msg("1", db.PlatformTwitch, t0.Add(time.Second), "=HYPERLINK(\"x\")", "+1"),
		msg("2", db.PlatformTwitch, t0.Add(2*time.Second), "  @sum", "-1"),
		msg("3", db.PlatformTwitch, t0.Add(3*time.Second), "\tTab", "fine, with comma"))
	var buf bytes.Buffer
	if err := WriteCSV(&buf, planFor(t, store), true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "offsetMs,at,atMs,platform,account,channel,messageId,authorId,author,roles,badges,text,replyToId,replyTo\n") {
		t.Errorf("header row:\n%s", out)
	}
	for _, want := range []string{
		`1000,2026-03-04T20:00:01Z,`,
		`"'=HYPERLINK(""x"")",,,'+1,`,
		`,'  @sum,,,'-1,`,
		",'\tTab,",
		`"fine, with comma"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	var noOffset bytes.Buffer
	if err := WriteCSV(&noOffset, planFor(t, store), false); err != nil || !strings.HasPrefix(noOffset.String(), "at,atMs,") {
		t.Errorf("without offsets: %v\n%s", err, noOffset.String())
	}
}

func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{
		"":         "",
		"plain":    "plain",
		"=1+1":     "'=1+1",
		"+1":       "'+1",
		"-1":       "'-1",
		"@x":       "'@x",
		"\tx":      "'\tx",
		"\rx":      "'\rx",
		"   =x":    "'   =x",
		"   ":      "   ",
		"a=b":      "a=b",
		"1-2":      "1-2",
		"héllo @x": "héllo @x",
	} {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNDJSONIsOneObjectPerLineWithNoOffsetWhenUntied(t *testing.T) {
	store := dbtest.Open(t)
	seed(t, store, msg("1", db.PlatformTwitch, t0.Add(time.Second), "ana", "a\nb"), msg("2", db.PlatformKick, t0.Add(2*time.Second), "bo", "c"))
	p, err := PlanRange(store, db.ChatRange{FromMS: t0.UnixMilli(), ToMS: t0.Add(time.Minute).UnixMilli(), Platform: db.PlatformTwitch}, t0)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteNDJSON(&buf, p); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("platform filter: %q", buf.String())
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e["text"] != "a\nb" {
		t.Errorf("line = %v (%v)", e, err)
	}
	if _, has := e["offsetMs"]; has {
		t.Error("an export with no recording carries no offset")
	}
}

type failingSource struct{ err error }

func (f failingSource) ChatRangeSummary(db.ChatRange) (db.ChatRangeSummary, error) {
	return db.ChatRangeSummary{Count: 1, MaxID: 1}, f.err
}
func (f failingSource) ForEachChatMessage(db.ChatRange, func(db.ChatMessage) error) error {
	return f.err
}
func (f failingSource) ChatOldest() (time.Time, bool, error) { return time.Time{}, false, f.err }

func TestSourceErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	if _, err := PlanRange(failingSource{boom}, db.ChatRange{}, t0); !errors.Is(err, boom) {
		t.Errorf("PlanRange err = %v", err)
	}
	p := Plan{Feed: func(func(Entry) error) error { return boom }}
	for name, w := range map[string]func() error{
		"json":   func() error { return WriteJSON(&bytes.Buffer{}, p) },
		"ndjson": func() error { return WriteNDJSON(&bytes.Buffer{}, p) },
		"csv":    func() error { return WriteCSV(&bytes.Buffer{}, p, false) },
		"vtt":    func() error { return WriteVTT(&bytes.Buffer{}, p) },
	} {
		if err := w(); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

type shortWriter struct{ n int }

func (s *shortWriter) Write(p []byte) (int, error) {
	if s.n -= len(p); s.n < 0 {
		return 0, errors.New("client went away")
	}
	return len(p), nil
}

func TestAWriterThatFailsStopsTheStream(t *testing.T) {
	store := dbtest.Open(t)
	for i := 0; i < 500; i++ {
		seed(t, store, msg(fmt.Sprint(i), db.PlatformTwitch, t0.Add(time.Duration(i)*time.Millisecond), "ana", strings.Repeat("x", 200)))
	}
	p := planFor(t, store)
	if err := WriteJSON(&shortWriter{n: 1000}, p); err == nil {
		t.Error("a failed write must surface")
	}
	if err := WriteNDJSON(&shortWriter{n: 1000}, p); err == nil {
		t.Error("a failed ndjson write must surface")
	}
}

func TestMoreThanOnePageStreamsEveryMessageOnceInOrder(t *testing.T) {
	store := dbtest.Open(t)
	const n = 2503 // more than two pages, with ties on the millisecond
	batch := make([]db.ChatMessage, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, msg(fmt.Sprintf("m%05d", i), db.PlatformTwitch, t0.Add(time.Duration(i/3)*time.Millisecond), "ana", "x"))
	}
	seed(t, store, batch...)
	info := RecordingInfo{File: "r.mkv", StartedAt: t0, EndSource: "probe"}
	p, err := PlanRecording(store, info, t0.UnixMilli(), t0.Add(time.Hour).UnixMilli(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Header.MessageCount != n {
		t.Fatalf("count = %d", p.Header.MessageCount)
	}
	var (
		got  int
		last int64 = -1
		seen       = map[string]bool{}
	)
	err = p.Feed(func(e Entry) error {
		if e.AtMs < last {
			t.Fatalf("out of order at %d", got)
		}
		last = e.AtMs
		if seen[e.MessageID] {
			t.Fatalf("%s delivered twice", e.MessageID)
		}
		seen[e.MessageID] = true
		got++
		return nil
	})
	if err != nil || got != n {
		t.Fatalf("streamed %d of %d (%v)", got, n, err)
	}

	// A message that lands after the header was taken is not in the stream, so
	// the count the header states is the count delivered.
	if _, err := store.AppendChatMessages([]db.ChatMessage{msg("late-arrival", db.PlatformTwitch, t0.Add(time.Minute), "ana", "x")}); err != nil {
		t.Fatal(err)
	}
	got = 0
	_ = p.Feed(func(Entry) error { got++; return nil })
	if got != n {
		t.Errorf("after a late arrival the stream delivered %d, header said %d", got, n)
	}
}
