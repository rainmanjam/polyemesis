package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ytPollServer is a fake YouTube API for the live poll: insert and transition.
type ytPollServer struct {
	mu     sync.Mutex
	bodies []string
	paths  []string
	close  string // the transition response
	fail   int    // status to answer insert with, 0 for OK
}

func (f *ytPollServer) handler(w http.ResponseWriter, r *http.Request, _ int64) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.bodies = append(f.bodies, string(b))
	switch r.URL.Path {
	case "/liveChatMessages":
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"liveChatDisabled"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"poll-msg-1","snippet":{"type":"pollEvent"}}`))
	case "/liveChatMessages/transition":
		_, _ = w.Write([]byte(f.close))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func ytPollAdapter(t *testing.T, f *ytPollServer, opts func(*YouTubeConfig)) *YouTubeAdapter {
	t.Helper()
	srv := newYTStub(t, f.handler)
	return ytAdapter(t, srv.URL, func(c *YouTubeConfig) {
		c.LiveChatID = "chat-1"
		if opts != nil {
			opts(c)
		}
	})
}

// TestYouTubeCreatesAndClosesALivePollAndReadsTheTally: the insert carries a
// pollEvent with the question and options in order, and the close reads the
// final tally -- a STRING per option in the resource -- from the transition
// response.
func TestYouTubeCreatesAndClosesALivePollAndReadsTheTally(t *testing.T) {
	f := &ytPollServer{close: `{"id":"poll-msg-1","snippet":{"pollDetails":{"metadata":{"status":"closed",
		"options":[{"optionText":"Dust","tally":"12"},{"optionText":"Mirage","tally":"3"}]}}}}`}
	a := ytPollAdapter(t, f, nil)
	id, err := a.CreatePoll(context.Background(), "Next map?", []string{"Dust", "Mirage"})
	if err != nil || id != "poll-msg-1" {
		t.Fatalf("CreatePoll = %q, %v", id, err)
	}
	var body struct {
		Snippet struct {
			LiveChatID  string `json:"liveChatId"`
			Type        string `json:"type"`
			PollDetails struct {
				Metadata struct {
					QuestionText string `json:"questionText"`
					Options      []struct {
						OptionText string `json:"optionText"`
					} `json:"options"`
				} `json:"metadata"`
			} `json:"pollDetails"`
		} `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	md := body.Snippet.PollDetails.Metadata
	if body.Snippet.Type != "pollEvent" || body.Snippet.LiveChatID != "chat-1" || md.QuestionText != "Next map?" ||
		len(md.Options) != 2 || md.Options[1].OptionText != "Mirage" {
		t.Errorf("insert body = %s", f.bodies[0])
	}
	votes, err := a.ClosePoll(context.Background(), id)
	if err != nil || len(votes) != 2 || votes[0] != 12 || votes[1] != 3 {
		t.Fatalf("ClosePoll = %v, %v", votes, err)
	}
	if !strings.HasPrefix(f.paths[1], "POST /liveChatMessages/transition?") || !strings.Contains(f.paths[1], "status=closed") ||
		!strings.Contains(f.paths[1], "id=poll-msg-1") || !strings.Contains(f.paths[1], "part=snippet") {
		t.Errorf("transition = %q", f.paths[1])
	}
	// The list-wrapped form of the same response reads the same.
	f.close = `{"items":[{"snippet":{"pollDetails":{"metadata":{"options":[{"tally":"1"},{"tally":"0"}]}}}}]}`
	if votes, err := a.ClosePoll(context.Background(), id); err != nil || votes[0] != 1 {
		t.Errorf("wrapped close = %v, %v", votes, err)
	}
}

// TestAYouTubePollIsRefusedWhenTheQuotaAboveTheReserveCannotCoverIt: the
// native poll is optional, so it never spends the send reserve. 40 units
// above the reserve, or nothing is created.
func TestAYouTubePollIsRefusedWhenTheQuotaAboveTheReserveCannotCoverIt(t *testing.T) {
	f := &ytPollServer{}
	a := ytPollAdapter(t, f, func(c *YouTubeConfig) { c.QuotaUnits = 1000; c.QuotaReserve = 200 })
	a.budget.spend(1000 - 200 - (QuotaCostPollCreate + QuotaCostPollClose) + 1)
	if _, err := a.CreatePoll(context.Background(), "q", []string{"a", "b"}); !errors.Is(err, ErrPollQuota) {
		t.Fatalf("CreatePoll = %v, want ErrPollQuota", err)
	}
	if len(f.paths) != 0 {
		t.Errorf("a refused poll still called YouTube: %v", f.paths)
	}
}

func TestAYouTubePollOutsideItsLimitsOrWithNoChatIsRefused(t *testing.T) {
	f := &ytPollServer{}
	a := ytPollAdapter(t, f, nil)
	if _, err := a.CreatePoll(context.Background(), "q", []string{"a", "b", "c", "d", "e"}); err == nil {
		t.Error("five options were sent to YouTube")
	}
	noChat := ytAdapter(t, "http://example.invalid", nil)
	if _, err := noChat.CreatePoll(context.Background(), "q", []string{"a", "b"}); err == nil {
		t.Error("a poll was created with no live chat")
	}
	f.fail = http.StatusForbidden
	if _, err := a.CreatePoll(context.Background(), "q", []string{"a", "b"}); err == nil {
		t.Error("a refused insert reported success")
	}
	// A close with no tally (not the owner's token) is an error, not zeros.
	f.close = `{"snippet":{"pollDetails":{"metadata":{"options":[{"optionText":"a"},{"optionText":"b"}]}}}}`
	if _, err := a.ClosePoll(context.Background(), "x"); err == nil {
		t.Error("a missing tally read as a count")
	}
	f.close = `{"snippet":{"pollDetails":{"metadata":{}}}}`
	if _, err := a.ClosePoll(context.Background(), "x"); err == nil {
		t.Error("an empty close read as a count")
	}
}

// TestSendEachPostsEachChatItsOwnTextAndSkipsAnEmptyOne, and NativePollers
// finds the adapters that can run a native poll.
func TestSendEachPostsEachChatItsOwnTextAndSkipsAnEmptyOne(t *testing.T) {
	h := testHub(t)
	tw := &fakeAdapter{platform: db.PlatformTwitch, account: "t"}
	kk := &fakeAdapter{platform: db.PlatformKick, account: "k"}
	for _, a := range []Adapter{sendingAdapter{tw}, sendingAdapter{kk}} {
		if err := h.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "attached", func() bool { _, a := h.Adapter(db.PlatformKick, "k"); return a })
	res, err := h.SendEach(context.Background(), func(p db.Platform, _ string) string {
		if p == db.PlatformKick {
			return ""
		}
		return "hello " + string(p)
	})
	if err != nil || len(res) != 1 || res[0].Platform != db.PlatformTwitch {
		t.Fatalf("SendEach = %+v, %v", res, err)
	}
	tw.mu.Lock()
	sent := append([]string(nil), tw.sent...)
	tw.mu.Unlock()
	kk.mu.Lock()
	kSent := len(kk.sent)
	kk.mu.Unlock()
	if len(sent) != 1 || sent[0] != "hello twitch" || kSent != 0 {
		t.Errorf("twitch got %q, kick got %d", sent, kSent)
	}
	if n := len(h.NativePollers(db.PlatformYouTube)); n != 0 {
		t.Errorf("%d native pollers with no YouTube attached", n)
	}
	empty := testHub(t)
	if _, err := empty.SendEach(context.Background(), func(db.Platform, string) string { return "x" }); err == nil {
		t.Error("SendEach with nothing connected succeeded")
	}
}
