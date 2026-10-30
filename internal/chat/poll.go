package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// NativePoller is the optional capability for running a platform's OWN poll in
// the chat this adapter is connected to (#14). YouTube is the one chat adapter
// with it; Twitch's native polls are a Helix call made with the account's
// OAuth token, in internal/oauth, not over the chat connection.
type NativePoller interface {
	Adapter
	// CreatePoll starts a native poll and returns its id.
	CreatePoll(ctx context.Context, question string, options []string) (string, error)
	// ClosePoll ends it and returns the final count of each option, in order.
	ClosePoll(ctx context.Context, pollID string) ([]int, error)
}

// ErrPollQuota is CreatePoll refusing because the daily quota above the send
// reserve cannot cover the poll's create and close. The chat poll then runs
// without the native one.
var ErrPollQuota = errors.New("not enough YouTube API quota above the send reserve for a native poll")

// NativePollers returns every attached adapter on platform p that can run a
// native poll, in account order.
func (h *Hub) NativePollers(p db.Platform) []NativePoller {
	h.mu.Lock()
	var out []NativePoller
	for _, r := range h.runners {
		if r.platform != p {
			continue
		}
		if np, ok := r.adapter.(NativePoller); ok {
			out = append(out, np)
		}
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Account() < out[j].Account() })
	return out
}

// SendEach is Send with a text per connected chat: textFor returns what to
// post to one platform account, or "" to post nothing there. A chat poll needs
// it because a chat where a native poll is running is told to vote in that
// poll, and every other chat to type a number.
func (h *Hub) SendEach(ctx context.Context, textFor func(p db.Platform, account string) string) ([]SendResult, error) {
	h.mu.Lock()
	rs := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		rs = append(rs, r)
	}
	h.mu.Unlock()
	if len(rs) == 0 {
		return nil, fmt.Errorf("chat: no platform is connected")
	}
	results := make([]SendResult, len(rs))
	sent := make([]bool, len(rs))
	var wg sync.WaitGroup
	for i, r := range rs {
		text := strings.TrimSpace(textFor(r.platform, r.account))
		if text == "" {
			continue
		}
		sent[i] = true
		wg.Add(1)
		go func(i int, r *runner, text string) {
			defer wg.Done()
			results[i] = r.send(ctx, text)
		}(i, r, text)
	}
	wg.Wait()
	out := make([]SendResult, 0, len(rs))
	for i, ok := range sent {
		if ok {
			out = append(out, results[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].Account < out[j].Account
	})
	return out, nil
}

// ---------------------------------------------------------------- YouTube

// The live poll's quota: liveChatMessages.insert with a pollEvent and
// liveChatMessages.transition to close it, 20 units each in Google's
// published table.
const (
	QuotaCostPollCreate = 20
	QuotaCostPollClose  = 20
)

// YouTube's poll limits. The API reference sets 2 to 4 options and no text
// lengths; the chat poll's own limits (100 and 30 characters) already apply
// before a mirror is tried.
const (
	YouTubePollMinOptions = 2
	YouTubePollMaxOptions = 4
)

// fitsAboveReserve reports whether cost fits in the budget WITHOUT spending
// the send reserve. A native poll is optional: it must never cost the
// operator the ability to reply on stream later in the day.
func (b *budget) fitsAboveReserve(cost int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked()
	return b.limit-b.reserve-b.used >= cost
}

var _ NativePoller = (*YouTubeAdapter)(nil)

// CreatePoll posts a live poll into the broadcast's chat: liveChatMessages
// .insert with snippet.type pollEvent. Needs no new scope -- the `youtube`
// scope polyemesis already holds covers insert and transition. Refused, with
// ErrPollQuota, unless the create AND the close fit above the send reserve.
func (y *YouTubeAdapter) CreatePoll(ctx context.Context, question string, options []string) (string, error) {
	if len(options) < YouTubePollMinOptions || len(options) > YouTubePollMaxOptions {
		return "", fmt.Errorf("YouTube polls have %d to %d options", YouTubePollMinOptions, YouTubePollMaxOptions)
	}
	y.mu.Lock()
	chatID := y.chatID
	y.mu.Unlock()
	if chatID == "" {
		return "", fmt.Errorf("YouTube has no live chat open right now")
	}
	if !y.budget.fitsAboveReserve(QuotaCostPollCreate + QuotaCostPollClose) {
		return "", ErrPollQuota
	}
	opts := make([]map[string]string, len(options))
	for i, o := range options {
		opts[i] = map[string]string{"optionText": o}
	}
	payload := map[string]any{"snippet": map[string]any{
		"liveChatId": chatID,
		"type":       "pollEvent",
		"pollDetails": map[string]any{"metadata": map[string]any{
			"questionText": question,
			"options":      opts,
		}},
	}}
	var out struct {
		ID string `json:"id"`
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodPost, y.cfg.APIBase+"/liveChatMessages?part=snippet",
		y.cfg.Token, payload, &out)
	y.budget.spend(QuotaCostPollCreate)
	if err != nil {
		return "", y.classify(err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("YouTube answered with no poll id")
	}
	return out.ID, nil
}

type ytPollItem struct {
	Snippet struct {
		PollDetails struct {
			Metadata struct {
				Status  string `json:"status"`
				Options []struct {
					// Tally is a STRING in the resource, and present only
					// when the request is authorised by the channel owner.
					Tally string `json:"tally"`
				} `json:"options"`
			} `json:"metadata"`
		} `json:"pollDetails"`
	} `json:"snippet"`
}

// ClosePoll closes the poll (liveChatMessages.transition, status=closed) and
// reads the final tally from the response, which part=snippet makes the
// poll's own resource.
//
// THE ONLY READ THERE IS. The API has no call that reads one live chat message
// by id, so a running YouTube poll's count cannot be read without closing it;
// the YouTube share of a mirrored poll is therefore merged once, at close.
//
// Never refused for want of quota, like Delete: a poll left open on YouTube
// after the chat poll ended is worse than 20 units past the reserve.
func (y *YouTubeAdapter) ClosePoll(ctx context.Context, pollID string) ([]int, error) {
	q := url.Values{"id": {pollID}, "status": {"closed"}, "part": {"snippet"}}
	// The resource and the list wrapper are both decoded: the reference says
	// "a liveChatMessages resource" for the response, which reads either way.
	var raw struct {
		ytPollItem
		Items []ytPollItem `json:"items"`
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodPost, y.cfg.APIBase+"/liveChatMessages/transition?"+q.Encode(),
		y.cfg.Token, nil, &raw)
	y.budget.spend(QuotaCostPollClose)
	if err != nil {
		return nil, y.classify(err)
	}
	item := raw.ytPollItem
	if len(raw.Items) > 0 {
		item = raw.Items[0]
	}
	opts := item.Snippet.PollDetails.Metadata.Options
	if len(opts) == 0 {
		return nil, fmt.Errorf("YouTube closed the poll but returned no tally")
	}
	votes := make([]int, len(opts))
	for i, o := range opts {
		if o.Tally == "" {
			return nil, fmt.Errorf("YouTube returned no tally; only the channel owner's token can read one")
		}
		n, err := strconv.Atoi(o.Tally)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("YouTube returned an unreadable tally %q", o.Tally)
		}
		votes[i] = n
	}
	return votes, nil
}
