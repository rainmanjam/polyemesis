package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// fbReactSink collects chat and activity.
type fbReactSink struct {
	mu  sync.Mutex
	act []activity.Event
}

func (s *fbReactSink) Deliver(Message) {}
func (s *fbReactSink) Activity(ev activity.Event) {
	s.mu.Lock()
	s.act = append(s.act, ev)
	s.mu.Unlock()
}
func (s *fbReactSink) events() []activity.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]activity.Event(nil), s.act...)
}

// fbReactGraph is a fake Graph live video: an empty comment thread and a
// reactions edge answering in the documented shape
// {"data":[],"paging":{},"summary":{"total_count":N,...}}.
type fbReactGraph struct {
	mu        sync.Mutex
	total     string // raw JSON for summary.total_count; "" omits the summary
	types     map[string]int64
	status    int // when set, /reactions answers this with a Graph error
	body      string
	reactReqs int
}

func (g *fbReactGraph) set(total int64, types map[string]int64) {
	g.mu.Lock()
	g.total, g.types = fmt.Sprint(total), types
	g.mu.Unlock()
}

func (g *fbReactGraph) requests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reactReqs
}

func (g *fbReactGraph) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/reactions") {
			fmt.Fprint(w, `{"data":[],"paging":{}}`)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		g.reactReqs++
		if g.status != 0 {
			w.WriteHeader(g.status)
			fmt.Fprint(w, g.body)
			return
		}
		if g.total == "" {
			fmt.Fprint(w, `{"data":[],"paging":{}}`)
			return
		}
		n := g.total
		if ty := r.URL.Query().Get("type"); ty != "" {
			n = fmt.Sprint(g.types[ty])
		}
		fmt.Fprintf(w, `{"data":[],"paging":{"cursors":{"before":"a","after":"b"}},"summary":{"total_count":%s,"viewer_reaction":"NONE","can_like":false,"has_liked":false}}`, n)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runFb runs the adapter for ticks comment polls, 10 s apart on a fake clock
// (so a reaction sample is due every third poll), calling step(tick) before
// each poll after the first.
func runFb(t *testing.T, a *FacebookAdapter, sink Sink, clk *time.Time, ticks int, step func(tick int)) {
	t.Helper()
	tick := 0
	a.cfg.Sleep = func(context.Context, time.Duration) bool {
		tick++
		*clk = clk.Add(10 * time.Second)
		if tick >= ticks {
			return false
		}
		if step != nil {
			step(tick)
		}
		return true
	}
	if err := a.Run(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
}

func fbReactAdapter(t *testing.T, g *fbReactGraph, on bool) (*FacebookAdapter, *time.Time) {
	t.Helper()
	clk := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	a := fbAdapter(t, g.server(t).URL, func(c *FacebookConfig) {
		c.Now = func() time.Time { return clk }
		c.ReactionsEnabled = func() bool { return on }
	})
	return a, &clk
}

var fbBase = map[string]int64{"LIKE": 100, "LOVE": 20, "WOW": 3}

func TestFacebookReactionsSeedSilentlyThenReportDeltas(t *testing.T) {
	g := &fbReactGraph{}
	g.set(123, fbBase)
	a, clk := fbReactAdapter(t, g, true)
	sink := &fbReactSink{}
	runFb(t, a, sink, clk, 13, func(tick int) {
		switch tick {
		case 3: // second sample: +7 (5 like, 2 love)
			g.set(130, map[string]int64{"LIKE": 105, "LOVE": 22, "WOW": 3})
		case 6: // third sample: nothing new
		case 9: // fourth sample: people un-reacted; baseline moves down, silent
			g.set(120, map[string]int64{"LIKE": 95, "LOVE": 22, "WOW": 3})
		case 12: // fifth sample: +4 from the lowered baseline
			g.set(124, map[string]int64{"LIKE": 99, "LOVE": 22, "WOW": 3})
		}
	})
	got := sink.events()
	if len(got) != 2 {
		t.Fatalf("want 2 deltas (the seed and the no-change and the drop are silent), got %d: %+v", len(got), got)
	}
	first, second := got[0], got[1]
	if first.Kind != activity.KindReaction || first.Count != 7 || first.Breakdown["like"] != 5 || first.Breakdown["love"] != 2 {
		t.Fatalf("first = %+v", first)
	}
	if _, has := first.Breakdown["wow"]; has {
		t.Fatalf("a type that did not move must not be in the breakdown: %+v", first.Breakdown)
	}
	if second.Count != 4 || second.Breakdown["like"] != 4 {
		t.Fatalf("second = %+v", second)
	}
	if first.Actor != nil || first.Amount != nil || first.Message != "" {
		t.Fatalf("an aggregate has no actor, amount or viewer text: %+v", first)
	}
	if first.Source.Type != "live_video.reactions" || first.Source.ID == "" || first.Source.ID == second.Source.ID {
		t.Fatalf("each window needs its own synthetic id: %q %q", first.Source.ID, second.Source.ID)
	}
	if !strings.HasPrefix(first.Source.ID, "live-1:") {
		t.Fatalf("id = %q", first.Source.ID)
	}
	first.Platform, first.Account = "facebook", "page:123" // the runner stamps these
	if err := first.Validate(); err != nil {
		t.Fatalf("event must validate: %v", err)
	}
}

func TestFacebookReactionsAreSampledEveryThirdPollAndNeverFasterThan30s(t *testing.T) {
	g := &fbReactGraph{}
	g.set(10, fbBase)
	a, clk := fbReactAdapter(t, g, true)
	runFb(t, a, &fbReactSink{}, clk, 3, nil) // polls 1,2,3: only poll 1 samples (1 total + 3 type reads is the seed)
	if got := g.requests(); got != 1+len(fbReactionTypes) {
		t.Fatalf("reaction requests = %d, want the seed only (%d)", got, 1+len(fbReactionTypes))
	}
}

func TestFacebookReactionsRestartDoesNotReplay(t *testing.T) {
	g := &fbReactGraph{}
	g.set(500, fbBase)
	a, clk := fbReactAdapter(t, g, true)
	sink := &fbReactSink{}
	runFb(t, a, sink, clk, 4, func(tick int) {
		if tick == 3 {
			g.set(505, map[string]int64{"LIKE": 105, "LOVE": 20, "WOW": 3})
		}
	})
	if n := len(sink.events()); n != 1 {
		t.Fatalf("first run: %d events", n)
	}
	// The connection drops and the supervisor runs the adapter again, a lot
	// later, after 1000 more reactions arrived. They are history, not news.
	g.set(1505, map[string]int64{"LIKE": 1105, "LOVE": 20, "WOW": 3})
	*clk = clk.Add(time.Hour)
	runFb(t, a, sink, clk, 4, func(tick int) {
		if tick == 3 {
			g.set(1508, map[string]int64{"LIKE": 1108, "LOVE": 20, "WOW": 3})
		}
	})
	got := sink.events()
	if len(got) != 2 || got[1].Count != 3 {
		t.Fatalf("after the reconnect only the 3 new reactions count, got %+v", got)
	}
	if got[0].Source.ID == got[1].Source.ID {
		t.Fatal("windows across a reconnect must not share a dedupe id")
	}
}

func TestFacebookReactionsOffMeansNoRequests(t *testing.T) {
	g := &fbReactGraph{}
	g.set(10, fbBase)
	a, clk := fbReactAdapter(t, g, false)
	sink := &fbReactSink{}
	runFb(t, a, sink, clk, 8, nil)
	if g.requests() != 0 || len(sink.events()) != 0 {
		t.Fatalf("reactions are off by default: %d requests, %d events", g.requests(), len(sink.events()))
	}
	if st := a.ActivityKinds(); st[0].Kind != activity.KindReaction || st[0].State != activity.KindOff {
		t.Fatalf("kinds = %+v", st)
	}
	// A NewFacebook with no ReactionsEnabled at all is also off.
	b, err := NewFacebook(FacebookConfig{Token: StaticToken("x")})
	if err != nil {
		t.Fatal(err)
	}
	if b.reactionsOn() {
		t.Fatal("no setting means off")
	}
}

func TestFacebookReactionsNeedingAppReviewAreUnavailableNotAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http 403", http.StatusForbidden, `{"error":{"message":"(#10) Application does not have permission for this action","type":"OAuthException","code":10,"fbtrace_id":"A1"}}`},
		{"code 200 as a 400", http.StatusBadRequest, `{"error":{"message":"(#200) Requires pages_read_engagement permission","type":"OAuthException","code":200,"fbtrace_id":"A2"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &fbReactGraph{status: tc.status, body: tc.body}
			a, clk := fbReactAdapter(t, g, true)
			sink := &fbReactSink{}
			runFb(t, a, sink, clk, 14, nil)
			st := a.ActivityKinds()[0]
			if st.State != activity.KindUnavailable || !strings.Contains(st.Detail, "App Review") {
				t.Fatalf("reaction = %+v", st)
			}
			if h := a.Health(); h.State != StateLive {
				t.Fatalf("the comments are fine, so the adapter stays live, got %+v", h)
			}
			if g.requests() != 1 {
				t.Fatalf("a permission refusal must be left alone for a long time, saw %d requests", g.requests())
			}
			if len(sink.events()) != 0 {
				t.Fatal("no events")
			}
		})
	}
}

func TestFacebookReactionsMalformedPayloadIsIgnored(t *testing.T) {
	g := &fbReactGraph{}
	g.set(100, fbBase)
	a, clk := fbReactAdapter(t, g, true)
	sink := &fbReactSink{}
	runFb(t, a, sink, clk, 10, func(tick int) {
		switch tick {
		case 3: // sample 2: Graph answers with no summary at all
			g.mu.Lock()
			g.total = ""
			g.mu.Unlock()
		case 6: // sample 3: a negative total
			g.mu.Lock()
			g.total = "-5"
			g.mu.Unlock()
		case 9: // sample 4: recovered, and +2 over the ORIGINAL baseline
			g.set(102, map[string]int64{"LIKE": 102, "LOVE": 20, "WOW": 3})
		}
	})
	got := sink.events()
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("junk must neither emit nor move the baseline, got %+v", got)
	}
	if h := a.Health(); h.State != StateLive {
		t.Fatalf("health = %+v", h)
	}
}

func TestFacebookActivityKindsSayWhyThereIsNoTip(t *testing.T) {
	g := &fbReactGraph{}
	a, _ := fbReactAdapter(t, g, true)
	kinds := a.ActivityKinds()
	if kinds[0].Kind != activity.KindReaction || kinds[0].State != activity.KindOn {
		t.Fatalf("kinds = %+v", kinds)
	}
	var tip *activity.KindStatus
	for i := range kinds {
		if kinds[i].Kind == activity.KindTip {
			tip = &kinds[i]
		}
	}
	if tip == nil || tip.State != activity.KindUnavailable || !strings.Contains(tip.Detail, "Stars") {
		t.Fatalf("tip = %+v", tip)
	}
	for _, k := range kinds {
		if !k.State.Valid() || strings.Contains(k.Detail, "fb-token") {
			t.Fatalf("bad status %+v", k)
		}
	}
}
