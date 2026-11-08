package chat

// Facebook's Page-level actions: blocking a person, and running a live poll.
//
// Both go through the same Graph API the comment poller reads, and both are
// separate from facebook.go because they are optional capabilities
// (Banner, NativePoller) rather than part of reading the thread.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// notLiveRe matches Graph's wording for a video that is not live.
var notLiveRe = regexp.MustCompile(`(?i)not\s+(currently\s+)?live|isn'?t\s+live|live\s+video\s+(is\s+)?not|status\s+(is\s+)?not\s+live|must\s+be\s+live`)

var (
	_ NativePoller = (*FacebookAdapter)(nil)
	_ PollReader   = (*FacebookAdapter)(nil)
)

// NoTimeout marks Facebook as a platform that can remove a person and cannot
// time one out. The Page block list (/{page-id}/blocked) has no duration: a
// block stays until it is lifted, so a timeout would silently become a
// permanent ban.
func (f *FacebookAdapter) NoTimeout() {}

// graphCode is the numeric code of Meta's error envelope, or zero when err is
// not one. The body is the apiError's own, already shortened, which is whole
// for the short envelopes the block list answers with.
func graphCode(err error) int {
	code, _ := graphCodes(err)
	return code
}

// graphCodes is graphCode with the error_subcode. It parses the whole body, not
// the 300-character display copy: a realistic Graph refusal is longer than that
// and would otherwise decode to nothing, skipping every code-specific branch.
func graphCodes(err error) (code, subcode int) {
	var ae *apiError
	if !errors.As(err, &ae) {
		return 0, 0
	}
	code, subcode, _ = oauth.GraphErrorFields(ae.payload())
	return code, subcode
}

// fbTokenDead reports a Graph refusal that only reconnecting fixes: HTTP 401,
// or the 190/102 token family, which Graph usually sends as HTTP 400.
func fbTokenDead(err error) bool {
	return statusOf(err) == http.StatusUnauthorized || oauth.IsGraphTokenDead(graphCode(err))
}

// fbNotFound reports a deleted or unknown object: HTTP 404/410, or Graph's
// habit of answering 400 with code 100, subcode 33.
func fbNotFound(err error) bool {
	switch statusOf(err) {
	case http.StatusNotFound, http.StatusGone:
		return true
	}
	code, sub := graphCodes(err)
	return code == 100 && sub == 33
}

// fbRateLimited reports a Graph throttle (HTTP 429 or the 4/17/32/613/8000x
// codes) and how long Graph asked the caller to wait, zero if it did not say.
func fbRateLimited(err error) (time.Duration, bool) {
	if statusOf(err) != http.StatusTooManyRequests && !oauth.IsGraphRateLimit(graphCode(err)) {
		return 0, false
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Wait, true
	}
	return 0, true
}

// fbSummary is the one-line, operator-facing form of a failure: Meta's message
// and code for a Graph envelope, just the status for anything else (an HTML
// error page from a proxy is never echoed), the error text for transport
// errors. The raw body and fbtrace_id stay out of health and warnings.
func fbSummary(err error) string {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	if s := oauth.GraphErrorSummary(ae.payload()); s != "" {
		return s
	}
	return fmt.Sprintf("HTTP %d", ae.Status)
}

// page resolves the Page this account acts as. The errors are the operator's
// to read, so they name what to do.
func (f *FacebookAdapter) page(ctx context.Context) (id, token string, err error) {
	if f.cfg.PageAccess == nil {
		return "", "", fmt.Errorf("Facebook only lets a Page block people, and this account is not connected as a Page")
	}
	id, token, err = f.cachedPageAccess(ctx)
	switch {
	case errors.Is(err, oauth.ErrNotAPage):
		return "", "", fmt.Errorf("Facebook only lets a Page block people, and this account is a personal profile. " +
			"Connect the Page in Settings → Platforms, or block the person on Facebook itself")
	case err != nil:
		return "", "", err
	case id == "" || token == "":
		return "", "", fmt.Errorf("Facebook returned no Page access for this account; reconnect it in Settings → Platforms")
	}
	return id, token, nil
}

// pageTokenTTL is how long a resolved Page token is reused. Resolving one costs
// a GET /me/accounts with the user token, and the poll mirror reads every few
// seconds, so without this a running poll would spend hundreds of the app's
// per-user calls an hour.
const pageTokenTTL = 10 * time.Minute

// cachedPageAccess is cfg.PageAccess behind a short cache. A success and
// ErrNotAPage are kept; every other error is asked again.
func (f *FacebookAdapter) cachedPageAccess(ctx context.Context) (string, string, error) {
	f.mu.Lock()
	if f.pageTok != "" && f.cfg.Now().Before(f.pageExpiry) {
		id, tok := f.pageID, f.pageTok
		f.mu.Unlock()
		return id, tok, nil
	}
	notPage := f.cfg.Now().Before(f.notPageUntil)
	f.mu.Unlock()
	if notPage {
		return "", "", oauth.ErrNotAPage
	}
	id, tok, err := f.cfg.PageAccess(ctx)
	if errors.Is(err, oauth.ErrNotAPage) {
		// A profile stays one; remembering it keeps a legacy ref that needs a
		// Page lookup from repeating it on every comment poll.
		f.mu.Lock()
		f.notPageUntil = f.cfg.Now().Add(pageTokenTTL)
		f.mu.Unlock()
	}
	if err == nil && id != "" && tok != "" {
		f.mu.Lock()
		f.pageID, f.pageTok, f.pageExpiry = id, tok, f.cfg.Now().Add(pageTokenTTL)
		f.mu.Unlock()
	}
	return id, tok, err
}

// dropPageToken forgets the cached Page token when Graph rejected a token, so
// the next call mints a fresh one.
func (f *FacebookAdapter) dropPageToken(err error) {
	if fbTokenDead(err) {
		f.mu.Lock()
		f.pageID, f.pageTok, f.pageExpiry = "", "", time.Time{}
		f.mu.Unlock()
	}
}

// commentToken is the token the thread is read and moderated with. Graph only
// returns a comment's author (`from`) on Page content to a Page access token,
// and Ban needs that id, so a Page account reads with its Page token. A
// profile, or a Page whose token cannot be minted, keeps the user token.
func (f *FacebookAdapter) commentToken(ctx context.Context) TokenFunc {
	tok, _ := f.commentTokenSource(ctx)
	return tok
}

// commentTokenSource is commentToken plus whether it is a Page token, so a
// caller can tell a rejected Page token from a rejected user token.
func (f *FacebookAdapter) commentTokenSource(ctx context.Context) (TokenFunc, bool) {
	if f.cfg.PageAccess != nil {
		if _, tok, err := f.cachedPageAccess(ctx); err == nil && tok != "" {
			return StaticToken(tok), true
		}
	}
	return f.cfg.Token, false
}

// Ban blocks a person from the Page: POST /{page-id}/blocked?user={id}.
//
// PERMANENT ONLY. Facebook's block list has no duration, so a positive d is
// refused before any request rather than turned into a permanent block the
// operator did not ask for. reason is not sent; the endpoint has no field for
// it.
func (f *FacebookAdapter) Ban(ctx context.Context, userID string, d time.Duration, _ string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("no user id to block")
	}
	if d > 0 {
		return fmt.Errorf("Facebook cannot time anyone out: blocking a person from a Page has no duration. " +
			"Ask for a permanent block explicitly if that is what you mean")
	}
	pageID, tok, err := f.page(ctx)
	if err != nil {
		return err
	}
	endpoint := f.cfg.APIBase + "/" + url.PathEscape(pageID) + "/blocked?" + url.Values{"user": {userID}}.Encode()
	var out map[string]bool
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodPost, endpoint, StaticToken(tok), nil, &out); err != nil {
		return f.blockError(err, "block")
	}
	// The response maps each id to whether it was blocked. An explicit false
	// is a refusal that came back as 200.
	if ok, present := out[userID]; present && !ok {
		return fmt.Errorf("Facebook did not block that person")
	}
	return nil
}

// Unban lifts a block: DELETE /{page-id}/blocked?user={id}.
func (f *FacebookAdapter) Unban(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("no user id to unblock")
	}
	pageID, tok, err := f.page(ctx)
	if err != nil {
		return err
	}
	endpoint := f.cfg.APIBase + "/" + url.PathEscape(pageID) + "/blocked?" + url.Values{"user": {userID}}.Encode()
	var out struct {
		Success *bool `json:"success"`
	}
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodDelete, endpoint, StaticToken(tok), nil, &out); err != nil {
		return f.blockError(err, "unblock")
	}
	if out.Success != nil && !*out.Success {
		return fmt.Errorf("Facebook did not unblock that person")
	}
	return nil
}

// blockError turns the block list's documented refusals into the sentence that
// fixes them. Codes from Meta's /{page-id}/blocked reference: 3801 the list is
// full, 3802 a person cannot be blocked again so soon after being unblocked,
// 368 the action was deemed abusive, 283 and 200 a missing permission, 210 the
// person is not visible to this Page.
func (f *FacebookAdapter) blockError(err error, verb string) error {
	f.dropPageToken(err)
	switch graphCode(err) {
	case 3801:
		return fmt.Errorf("Facebook would not block that person: this Page has blocked the maximum number of people. "+
			"Unblock someone on Facebook first: %w", err)
	case 3802:
		return fmt.Errorf("Facebook would not block that person again yet: a person cannot be re-blocked so soon after "+
			"being unblocked. Try again later: %w", err)
	case 368:
		return fmt.Errorf("Facebook refused to %s that person: it deemed the action abusive or disallowed: %w", verb, err)
	case 210:
		return fmt.Errorf("Facebook refused to %s that person: they are not visible to this Page: %w", verb, err)
	case 200, 283:
		return fmt.Errorf("Facebook refused to %s that person for lack of a permission. Blocking needs a Page "+
			"access token with Page engagement access (pages_read_engagement; Facebook can also ask for "+
			"pages_read_user_content or pages_manage_metadata), and Meta's reference does not say which one. "+
			"Reconnect the Page in Settings → Platforms and accept every permission: %w", verb, err)
	}
	if fbTokenDead(err) {
		return Fatal(fmt.Errorf("facebook rejected the access token; reconnect the Facebook account in Settings → Platforms"))
	}
	if wait, limited := fbRateLimited(err); limited {
		return fmt.Errorf("Facebook is rate-limiting polyemesis, so it could not %s that person. %s", verb, fbWaitAdvice(wait))
	}
	switch statusOf(err) {
	case http.StatusForbidden:
		return fmt.Errorf("Facebook refused to %s that person. Only a Page admin's Page token may: %w", verb, err)
	}
	return err
}

// ------------------------------------------------------------------- polls

// PollReader is the optional half of NativePoller for a platform that can
// report a running poll's count without closing it. YouTube cannot, so it
// does not implement this; Facebook can.
type PollReader interface {
	NativePoller
	// PollVotes returns the running count of each option, in order, and
	// whether the poll is still open on the platform.
	PollVotes(ctx context.Context, pollID string) (votes []int, open bool, err error)
}

// pollToken is the token a poll is created and read with: the token that
// created the live video. For a Page that is the Page's token, for a profile
// the user's.
func (f *FacebookAdapter) pollToken(ctx context.Context) (TokenFunc, error) {
	if f.cfg.PageAccess != nil {
		_, tok, err := f.cachedPageAccess(ctx)
		switch {
		case err == nil && tok != "":
			return StaticToken(tok), nil
		case errors.Is(err, oauth.ErrNotAPage):
			// A profile: its own token created the live video.
		case err != nil:
			return nil, err
		}
	}
	return f.cfg.Token, nil
}

// CreatePoll puts a native poll on the live video: POST /{live-video-id}/polls
// with question and options (Live Video API, "Polls"). Facebook accepts one
// only while the live video is LIVE, so a poll started before the stream is up
// is refused with a sentence saying so.
func (f *FacebookAdapter) CreatePoll(ctx context.Context, question string, options []string) (string, error) {
	videoID := f.liveVideoID()
	if videoID == "" {
		return "", fmt.Errorf("Facebook has no live video for this account right now")
	}
	tok, err := f.pollToken(ctx)
	if err != nil {
		return "", err
	}
	// Graph takes `options` as a JSON array in one parameter, as the Live
	// Video API polls guide's example does, not as PHP-style options[].
	opts, err := json.Marshal(options)
	if err != nil {
		return "", err
	}
	q := url.Values{"question": {question}, "options": {string(opts)}}
	var out struct {
		ID string `json:"id"`
	}
	endpoint := f.cfg.APIBase + "/" + url.PathEscape(videoID) + "/polls?" + q.Encode()
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodPost, endpoint, tok, nil, &out); err != nil {
		return "", f.pollError(err, "start")
	}
	if out.ID == "" {
		return "", fmt.Errorf("Facebook answered with no poll id")
	}
	return out.ID, nil
}

// PollVotes reads the running count and status:
// GET /{poll-id}?fields=status,poll_options{...}. open is false once the poll
// is closed on Facebook's side.
func (f *FacebookAdapter) PollVotes(ctx context.Context, pollID string) ([]int, bool, error) {
	tok, err := f.pollToken(ctx)
	if err != nil {
		return nil, false, err
	}
	var out struct {
		Status      string `json:"status"`
		PollOptions struct {
			Data []struct {
				Order      int `json:"order"`
				TotalVotes int `json:"total_votes"`
			} `json:"data"`
		} `json:"poll_options"`
	}
	endpoint := f.cfg.APIBase + "/" + url.PathEscape(pollID) + "?" +
		url.Values{"fields": {"status,poll_options{order,total_votes}"}}.Encode()
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodGet, endpoint, tok, nil, &out); err != nil {
		return nil, false, f.pollError(err, "read")
	}
	opts := out.PollOptions.Data
	if len(opts) == 0 {
		return nil, false, fmt.Errorf("Facebook returned no options for the poll")
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].Order < opts[j].Order })
	votes := make([]int, len(opts))
	for i, o := range opts {
		votes[i] = max(o.TotalVotes, 0)
	}
	return votes, !strings.EqualFold(strings.TrimSpace(out.Status), "closed"), nil
}

// ClosePoll closes the poll (POST /{poll-id}?action=CLOSE) and returns the
// final count. The count is read after the close, so a vote that landed while
// the close was in flight is in it.
func (f *FacebookAdapter) ClosePoll(ctx context.Context, pollID string) ([]int, error) {
	tok, err := f.pollToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := f.cfg.APIBase + "/" + url.PathEscape(pollID) + "?" + url.Values{"action": {"CLOSE"}}.Encode()
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodPost, endpoint, tok, nil, nil); err != nil {
		return nil, f.pollError(err, "close")
	}
	votes, _, err := f.PollVotes(ctx, pollID)
	return votes, err
}

// pollError names what the poll endpoints need. Meta's reference lists only
// error 200 (permissions) for them, and the guide says a poll can only be
// created on a live video whose status is LIVE.
func (f *FacebookAdapter) pollError(err error, verb string) error {
	f.dropPageToken(err)
	switch graphCode(err) {
	case 200:
		return fmt.Errorf("Facebook refused to %s the poll for lack of a permission. Polls need the same access the "+
			"broadcast was created with (publish_video for a profile, pages_manage_posts and pages_read_engagement "+
			"for a Page), and the Live Video API feature: %w", verb, err)
	}
	if fbTokenDead(err) {
		return Fatal(fmt.Errorf("facebook rejected the access token; reconnect the Facebook account in Settings → Platforms"))
	}
	if wait, limited := fbRateLimited(err); limited {
		return fmt.Errorf("Facebook is rate-limiting polyemesis, so it could not %s the poll. %s", verb, fbWaitAdvice(wait))
	}
	if verb == "start" && statusOf(err) == http.StatusBadRequest {
		var ae *apiError
		if errors.As(err, &ae) && notLiveRe.MatchString(ae.Body) {
			return fmt.Errorf("Facebook did not start the poll. It only accepts a poll on a live video that is live right now, "+
				"so the stream has to be up before the poll starts: %w", err)
		}
		return fmt.Errorf("Facebook did not start the poll: %w", err)
	}
	return err
}
