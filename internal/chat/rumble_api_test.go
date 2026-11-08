package chat

// Tests for what the Rumble adapter reads beyond chat text: the follower and
// subscriber counts on the same get-data poll, detection of a poll that
// outran Rumble's 50-entry cap, and the key staying out of every string.
// Nothing here has been run against a live account.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rumbleBody builds a live get-data body with n messages whose text starts at
// offset, so two bodies can be made to overlap or not.
func rumbleBody(n, offset int, extra string) string {
	var b strings.Builder
	b.WriteString(`{` + extra + `"livestreams":[{"is_live":true,"title":"t","watching_now":3,"chat":{"recent_messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"username":"u%d","badges":[],"text":"m%d","created_on":"2026-08-13T17:59:00+00:00"}`, offset+i, offset+i)
	}
	b.WriteString(`]}}]}`)
	return b.String()
}

func TestRumbleReportsFollowerAndSubscriberCounts(t *testing.T) {
	s := rumbleOK(t, rumbleBody(1, 0, `"followers":{"num_followers":1200,"num_followers_total":1500},"subscribers":{"num_subscribers":34},`))
	a := rumbleAdapter(t, s.URL, nil)
	if _, err := rumbleRun(t, a, 1); err != nil {
		t.Fatal(err)
	}
	h := a.Health()
	if h.Followers == nil || *h.Followers != 1200 || h.Subscribers == nil || *h.Subscribers != 34 {
		t.Errorf("followers/subscribers = %v/%v, want 1200/34", h.Followers, h.Subscribers)
	}
}

func TestRumbleReportsNoFollowerCountWhenNoneWasSent(t *testing.T) {
	s := rumbleOK(t, rumbleBody(1, 0, ""))
	a := rumbleAdapter(t, s.URL, nil)
	if _, err := rumbleRun(t, a, 1); err != nil {
		t.Fatal(err)
	}
	if h := a.Health(); h.Followers != nil || h.Subscribers != nil {
		t.Errorf("counts = %v/%v, want nil: absent is not zero", h.Followers, h.Subscribers)
	}
}

func TestRumbleFlagsAFullPollWithNoOverlap(t *testing.T) {
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, call int64) {
		_, _ = w.Write([]byte(rumbleBody(50, int(call)*1000, "")))
	})
	a := rumbleAdapter(t, s.URL, nil)
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.cfg.Sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) >= 2 {
			cancel()
			return false
		}
		return true
	}
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	h := a.Health()
	if h.State != StateLive || h.Detail != rumbleDropNote {
		t.Errorf("health = %+v, want live with the dropped-messages note", h)
	}
	// The first poll has nothing to compare with, so only later waits are
	// judged: there every entry was new and older ones were cut.
	if len(waits) < 2 || waits[1] != rumbleMinPoll {
		t.Errorf("waits = %v, want the %v floor on the poll after a full, all-new response", waits, rumbleMinPoll)
	}
}

func TestRumbleDoesNotFlagFirstPollOverlapOrShortPolls(t *testing.T) {
	cases := map[string]func(call int64) string{
		"full but overlapping": func(call int64) string { return rumbleBody(50, int(call)*10, "") },
		"no overlap but short": func(call int64) string { return rumbleBody(10, int(call)*1000, "") },
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, call int64) {
				_, _ = w.Write([]byte(body(call)))
			})
			a := rumbleAdapter(t, s.URL, nil)
			if _, err := rumbleRun(t, a, 3); err != nil {
				t.Fatal(err)
			}
			if d := a.Health().Detail; d != "" {
				t.Errorf("Detail = %q, want none", d)
			}
		})
	}
	t.Run("first poll", func(t *testing.T) {
		s := rumbleOK(t, rumbleBody(50, 0, ""))
		a := rumbleAdapter(t, s.URL, nil)
		if _, err := rumbleRun(t, a, 1); err != nil {
			t.Fatal(err)
		}
		if d := a.Health().Detail; d != "" {
			t.Errorf("Detail = %q on the first poll, which has nothing to compare with", d)
		}
	})
}

func TestRumbleDropNoteExpires(t *testing.T) {
	a := rumbleAdapter(t, "http://unused", nil)
	now := rumbleNow
	a.cfg.Now = func() time.Time { return now }
	a.noteDrop()
	a.setLive(nil, nil, nil)
	if a.Health().Detail != rumbleDropNote {
		t.Fatal("note missing right after a drop")
	}
	now = now.Add(rumbleDropNoteFor + time.Second)
	a.setLive(nil, nil, nil)
	if d := a.Health().Detail; d != "" {
		t.Errorf("Detail = %q after the note window, want none", d)
	}
}

func TestRumbleHonoursTheCapRumbleReports(t *testing.T) {
	if got := (rumbleResponse{MaxNumResults: 20}).chatCap(); got != 20 {
		t.Errorf("chatCap = %d, want 20", got)
	}
	if got := (rumbleResponse{}).chatCap(); got != rumbleChatCap {
		t.Errorf("chatCap = %d, want default %d", got, rumbleChatCap)
	}
}

func TestRumbleAcceptsAPastedURLAsTheKey(t *testing.T) {
	a, err := NewRumble(RumbleConfig{Key: "https://rumble.com/-livestream-api/get-data?key=abc%2F123"})
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.Key != "abc/123" {
		t.Errorf("Key = %q, want the key parameter alone", a.cfg.Key)
	}
}

func TestRumbleScrubsAKeyParameterItDoesNotHoldLiterally(t *testing.T) {
	a := rumbleAdapter(t, "http://unused", nil)
	got := a.scrub(`Get "https://rumble.com/-livestream-api/get-data?key=SOMETHING-ELSE&x=1": boom`)
	if strings.Contains(got, "SOMETHING-ELSE") || !strings.Contains(got, rumbleKeyPlaceholder) {
		t.Errorf("scrub = %q", got)
	}
}

func TestRumbleTransportErrorCarriesNoKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // connection refused: the error text embeds the full URL
	a := rumbleAdapter(t, base, nil)
	_, err := rumbleRun(t, a, 1)
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "test-api-key-abc123") {
		t.Errorf("error leaks the key: %v", err)
	}
	if strings.Contains(a.Health().Detail, "test-api-key-abc123") {
		t.Errorf("health leaks the key: %q", a.Health().Detail)
	}
}

// Once a chat holds a full snapshot every poll returns 50 entries. A quiet
// chat must not be read as saturated just because the snapshot is full.
func TestRumbleQuietFullChatIsNotPolledAtTheFloor(t *testing.T) {
	s := rumbleOK(t, rumbleBody(50, 0, ""))
	a := rumbleAdapter(t, s.URL, nil)
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.cfg.Sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) >= 3 {
			cancel()
			return false
		}
		return true
	}
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	if len(waits) < 3 {
		t.Fatalf("waits = %v, want 3", waits)
	}
	// Poll 1 is the first sight (messages delivered); polls 2 and 3 add nothing.
	for _, w := range waits[1:] {
		if w == rumbleMinPoll {
			t.Errorf("waits = %v: an unchanged full snapshot was polled at the %v floor", waits, rumbleMinPoll)
		}
	}
}
