package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// searchServer seeds a scrollback worth searching across two platforms.
func searchServer(t *testing.T) (http.Handler, func(*http.Request)) {
	t.Helper()
	_, h, store := testServer(t, config.Config{})
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	storeChat(t, store,
		chatRow(db.PlatformTwitch, "t1", "ana", "the audio dropped out", base),
		chatRow(db.PlatformTwitch, "t2", "bo", "audio is fine here", base.Add(time.Minute)),
		chatRow(db.PlatformYouTube, "y1", "cy", "audio sounds good", base.Add(2*time.Minute)),
		chatRow(db.PlatformTwitch, "t3", "dee", "unrelated chatter", base.Add(3*time.Minute)),
	)
	return h, login(t, h)
}

func searchFor(t *testing.T, h http.Handler, sign func(*http.Request), query string) chatSearchResult {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/chat/search"+query, nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /chat/search%s = %d, want 200; body %s", query, w.Code, w.Body.String())
	}
	var out chatSearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestChatSearchFindsMatchesNewestFirst(t *testing.T) {
	h, sign := searchServer(t)

	out := searchFor(t, h, sign, "?q=audio")
	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want the 3 mentioning audio: %+v", len(out.Messages), out.Messages)
	}
	// Newest first, unlike the scrollback reads: this is a result list, and the
	// most recent match is the one being hunted for.
	if out.Messages[0].Text != "audio sounds good" {
		t.Errorf("first result = %q, want the newest match", out.Messages[0].Text)
	}
	// The same caveat the user card carries, for the same reason: an operator
	// who reads "no results" as "never said it" has been misled by a table that
	// is purged on a timer.
	if out.RetentionNote == "" {
		t.Error("no retention note; an empty result would read as proof of absence")
	}
}

func TestChatSearchNarrowsToPlatform(t *testing.T) {
	h, sign := searchServer(t)

	out := searchFor(t, h, sign, "?q=audio&platform=youtube")
	if len(out.Messages) != 1 || out.Messages[0].Platform != db.PlatformYouTube {
		t.Fatalf("youtube search = %+v, want only the youtube match", out.Messages)
	}
	if out.Platform != db.PlatformYouTube {
		t.Errorf("platform echoed as %q, want youtube so the UI can label the result set", out.Platform)
	}
}

func TestChatSearchRequiresATerm(t *testing.T) {
	h, sign := searchServer(t)

	// An empty term must not fall through to "return the table". The pane
	// already shows recent messages; a search that answers everything hides
	// that nothing was actually searched for.
	for _, q := range []string{"", "?q=", "?q=%20%20"} {
		r := jsonRequest(t, http.MethodGet, "/api/v1/chat/search"+q, nil)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("GET /chat/search%s = %d, want 400", q, w.Code)
		}
	}
}

func TestChatSearchReportsTruncation(t *testing.T) {
	h, sign := searchServer(t)

	// Hitting the limit means older matches may exist, and the UI has to be
	// able to say so rather than present a capped list as the whole answer.
	out := searchFor(t, h, sign, "?q=audio&limit=2")
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want the limit of 2", len(out.Messages))
	}
	if !out.Truncated {
		t.Error("truncated = false at the limit; the UI cannot warn there is more")
	}

	out = searchFor(t, h, sign, "?q=unrelated")
	if out.Truncated {
		t.Error("truncated = true for a complete result set")
	}
}

// Search is behind the same authentication as the rest of chat. Scrollback
// names viewers and quotes them, so an unauthenticated reader must not be able
// to mine it.
func TestChatSearchRequiresAuth(t *testing.T) {
	h, _ := searchServer(t)

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat/search?q=audio", nil)
	if w := do(t, h, r); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated search = %d, want 401", w.Code)
	}
}
