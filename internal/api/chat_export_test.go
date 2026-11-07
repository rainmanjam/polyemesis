package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// plantRecordingChat seeds the highlight fixture's recording (started an hour
// ago, ten minutes long) with chat: two viewers on two platforms inside the
// window, a message the operator sent, and one outside it.
func plantRecordingChat(t *testing.T, srv *Server, rec int64) time.Time {
	t.Helper()
	r, err := srv.store.GetRecording(rec)
	if err != nil {
		t.Fatal(err)
	}
	start := r.StartedAt
	msgs := []db.ChatMessage{
		{Platform: db.PlatformTwitch, Account: "acct", MessageID: "m1", AuthorID: "u1", AuthorName: "Gull Watcher",
			Text: "seven gulls <3", Subscriber: true, Badges: []byte(`[{"id":"subscriber","version":"12"}]`),
			At: start.Add(2500 * time.Millisecond)},
		{Platform: db.PlatformYouTube, Account: "chan", MessageID: "m2", AuthorID: "u2", AuthorName: "=cmd|' /C calc'!A0",
			Text: "+1 first line\nsecond line", At: start.Add(61 * time.Second)},
		{Platform: db.PlatformTwitch, Account: "acct", MessageID: "echo", AuthorName: "operator", Text: "ours",
			Echo: true, At: start.Add(3 * time.Second)},
		{Platform: db.PlatformTwitch, Account: "acct", MessageID: "late", AuthorID: "u3", AuthorName: "late",
			Text: "after the recording", At: start.Add(time.Hour)},
	}
	if _, err := srv.store.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}
	return start
}

func getAs(t *testing.T, h http.Handler, sign func(*http.Request), path string) (*httpResp, string) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	sign(r)
	w := do(t, h, r)
	return &httpResp{Code: w.Code, Header: w.Header()}, w.Body.String()
}

type httpResp struct {
	Code   int
	Header http.Header
}

func TestRecordingChatIsBuiltFromTheTableWhenNoSidecarExists(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	start := plantRecordingChat(t, srv, rec)
	path := "/api/v1/recordings/" + strconv.FormatInt(rec, 10) + "/chat"

	resp, body := getAs(t, h, sign, path)
	if resp.Code != http.StatusOK {
		t.Fatalf("json: %d %s", resp.Code, body)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "rec-20261102-090000.chat.json") {
		t.Errorf("Content-Disposition = %q", got)
	}
	var doc struct {
		Schema       int `json:"schema"`
		MessageCount int `json:"messageCount"`
		Platforms    []string
		Recording    struct {
			File      string `json:"file"`
			EndSource string `json:"endSource"`
		} `json:"recording"`
		Messages []struct {
			OffsetMs int64    `json:"offsetMs"`
			At       string   `json:"at"`
			Platform string   `json:"platform"`
			Author   string   `json:"author"`
			Roles    []string `json:"roles"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not json: %v\n%s", err, body)
	}
	if doc.Schema != 1 || doc.MessageCount != 2 || len(doc.Messages) != 2 {
		t.Fatalf("header/messages = %+v", doc)
	}
	if doc.Recording.File != "rec-20261102-090000.mkv" || doc.Recording.EndSource != "probe" {
		t.Errorf("recording = %+v", doc.Recording)
	}
	m := doc.Messages[0]
	if m.OffsetMs != 2500 || m.Author != "Gull Watcher" || m.Platform != "twitch" || len(m.Roles) != 1 || m.Roles[0] != "subscriber" {
		t.Errorf("first message = %+v", m)
	}
	if !strings.HasSuffix(m.At, "Z") {
		t.Errorf("at %q is not UTC", m.At)
	}
	_ = start
	if strings.Contains(body, "ours") || strings.Contains(body, "after the recording") {
		t.Error("an echo or an out-of-window message leaked into the export")
	}

	resp, body = getAs(t, h, sign, path+"?format=vtt")
	if resp.Code != http.StatusOK || !strings.HasPrefix(body, "WEBVTT") {
		t.Fatalf("vtt: %d %s", resp.Code, body)
	}
	if !strings.Contains(body, "00:00:02.500 --> 00:00:07.500\n[TWITCH] Gull Watcher: seven gulls &lt;3") {
		t.Errorf("vtt cue missing:\n%s", body)
	}
	if !strings.Contains(body, "[YOUTUBE] =cmd|' /C calc'!A0: +1 first line second line") {
		t.Errorf("multi-line text should be one cue line:\n%s", body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/vtt") {
		t.Errorf("vtt content type = %q", got)
	}
}

func TestRecordingChatServesTheSavedSidecarAsWritten(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	dir := srv.eng().Recordings().Dir()
	// No chat in the table at all: the sidecar is the only source.
	if err := os.WriteFile(filepath.Join(dir, "rec-20261102-090000.chat.json"), []byte(`{"schema":1,"saved":"yes"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rec-20261102-090000.chat.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/recordings/" + strconv.FormatInt(rec, 10) + "/chat"
	resp, body := getAs(t, h, sign, path)
	if resp.Code != http.StatusOK || body != `{"schema":1,"saved":"yes"}` {
		t.Fatalf("json sidecar: %d %q", resp.Code, body)
	}
	resp, body = getAs(t, h, sign, path+"?format=vtt")
	if resp.Code != http.StatusOK || body != "WEBVTT\n" {
		t.Fatalf("vtt sidecar: %d %q", resp.Code, body)
	}

	// And the list says so.
	resp, body = getAs(t, h, sign, "/api/v1/recordings")
	if resp.Code != http.StatusOK || !strings.Contains(body, `"chatSaved":true`) {
		t.Errorf("list should report chatSaved: %d %s", resp.Code, body)
	}
}

func TestRecordingChatSaysSoWhenNothingWasSavedAndTheTableLostIt(t *testing.T) {
	h, sign, _, rec := highlightFixture(t)
	path := "/api/v1/recordings/" + strconv.FormatInt(rec, 10) + "/chat"
	resp, body := getAs(t, h, sign, path)
	if resp.Code != http.StatusNotFound || !strings.Contains(body, "no longer stored") {
		t.Fatalf("want a 404 that says why, got %d %s", resp.Code, body)
	}
	resp, body = getAs(t, h, sign, "/api/v1/recordings")
	if resp.Code != http.StatusOK || !strings.Contains(body, `"chatSaved":false`) {
		t.Errorf("list should report chatSaved false: %d %s", resp.Code, body)
	}
}

func TestRecordingChatIsAQuietFileNotA404WhenTheTableIsKnownComplete(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	// Chat from BEFORE the recording proves the table reaches back past its
	// start, so a window with no messages is a quiet chat rather than a purged one.
	r, _ := srv.store.GetRecording(rec)
	if _, err := srv.store.AppendChatMessages([]db.ChatMessage{{Platform: db.PlatformTwitch, MessageID: "old",
		AuthorName: "x", Text: "before", At: r.StartedAt.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	resp, body := getAs(t, h, sign, "/api/v1/recordings/"+strconv.FormatInt(rec, 10)+"/chat")
	if resp.Code != http.StatusOK || !strings.Contains(body, `"messageCount":0`) || !strings.Contains(body, `"complete":true`) {
		t.Fatalf("want a valid empty export: %d %s", resp.Code, body)
	}
}

func TestRecordingChatRefusesBadInputBeforeAnyByte(t *testing.T) {
	h, sign, _, rec := highlightFixture(t)
	id := strconv.FormatInt(rec, 10)
	for path, want := range map[string]int{
		"/api/v1/recordings/" + id + "/chat?format=csv": http.StatusBadRequest,
		"/api/v1/recordings/abc/chat":                   http.StatusBadRequest,
		"/api/v1/recordings/99999/chat":                 http.StatusNotFound,
	} {
		if resp, body := getAs(t, h, sign, path); resp.Code != want {
			t.Errorf("%s = %d (%s), want %d", path, resp.Code, body, want)
		}
	}
}

func TestChatExportStreamsAWindowInEachFormat(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	start := plantRecordingChat(t, srv, rec)
	q := func(extra string) string {
		v := url.Values{}
		v.Set("from", start.Add(-time.Minute).UTC().Format(time.RFC3339))
		v.Set("to", start.Add(10*time.Minute).UTC().Format(time.RFC3339))
		return "/api/v1/chat/export?" + v.Encode() + extra
	}

	resp, body := getAs(t, h, sign, q(""))
	if resp.Code != http.StatusOK || !strings.Contains(body, `"messageCount":2`) || strings.Contains(body, `"recording"`) {
		t.Fatalf("json: %d %s", resp.Code, body)
	}
	if strings.Contains(body, `"offsetMs"`) {
		t.Error("a window with no recording has no offsets")
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "polyemesis-chat-") || !strings.HasSuffix(cd, `.json"`) && !strings.HasSuffix(cd, ".json") {
		t.Errorf("Content-Disposition = %q", cd)
	}

	resp, body = getAs(t, h, sign, q("&format=ndjson"))
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if resp.Code != http.StatusOK || len(lines) != 2 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("ndjson: %d %q", resp.Code, body)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first["author"] != "Gull Watcher" {
		t.Errorf("first ndjson line = %v (%v)", first, err)
	}

	resp, body = getAs(t, h, sign, q("&format=csv"))
	if resp.Code != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d", resp.Code)
	}
	// The formula-shaped name and text are defused; the first line of the
	// multi-line message keeps its newline inside a quoted cell.
	if !strings.Contains(body, `,'=cmd|' /C calc'!A0,`) || !strings.Contains(body, `"'+1 first line`) {
		t.Errorf("csv cells not neutralised:\n%s", body)
	}

	resp, body = getAs(t, h, sign, q("&platform=youtube"))
	if !strings.Contains(body, `"messageCount":1`) || strings.Contains(body, "Gull Watcher") {
		t.Errorf("platform filter: %s", body)
	}
}

func TestChatExportRefusesBadParametersBeforeAnyByte(t *testing.T) {
	h, sign, _, _ := highlightFixture(t)
	for _, path := range []string{
		"/api/v1/chat/export",                                 // no from
		"/api/v1/chat/export?from=yesterday",                  // not a time
		"/api/v1/chat/export?from=1000&to=500",                // to before from
		"/api/v1/chat/export?from=1000&to=soon",               // bad to
		"/api/v1/chat/export?from=1000&format=xml",            // bad format
		"/api/v1/chat/export?from=1000&platform=Twitch%20TV!", // bad platform
	} {
		if resp, body := getAs(t, h, sign, path); resp.Code != http.StatusBadRequest {
			t.Errorf("%s = %d (%s), want 400", path, resp.Code, body)
		}
	}
	// Unix milliseconds are accepted, and `to` defaults to now.
	if resp, body := getAs(t, h, sign, "/api/v1/chat/export?from=1000"); resp.Code != http.StatusOK {
		t.Errorf("from in ms: %d %s", resp.Code, body)
	}
}

func TestChatExportIsRefusedToAReadToken(t *testing.T) {
	h, sign, _, rec := highlightFixture(t)
	read := createScopedToken(t, h, sign, "chat-export-read", db.ScopeRead)
	for _, path := range []string{
		"/api/v1/chat/export?from=1000",
		"/api/v1/recordings/" + strconv.FormatInt(rec, 10) + "/chat",
	} {
		if resp, _ := getAs(t, h, bearer(read), path); resp.Code != http.StatusForbidden {
			t.Errorf("%s = %d for a read token, want 403", path, resp.Code)
		}
	}
}
