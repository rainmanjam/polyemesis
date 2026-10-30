package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/polls"
)

// Chat polls (#14). The counting is internal/polls; this file is its REST
// face and the Twitch mirror.
//
// WHO SEES WHAT. A poll's result is counts: per option, per platform. Who voted
// for what never leaves internal/polls, so there is nothing here to withhold
// from a read token, and the GETs follow the method rule like the rest of the
// API -- a monitoring script or a read-only MCP client can show the result.
// Starting and ending a poll posts into every chat as the streamer, so both
// are admin.

const pollsUnavailable = "polls need the chat hub, which this server was started without"

func (s *Server) requirePolls(w http.ResponseWriter) bool {
	if s.polls == nil {
		writeError(w, http.StatusServiceUnavailable, pollsUnavailable)
		return false
	}
	return true
}

// storedResult decodes a stored poll. A row whose result predates its close
// -- an interrupted poll -- still carries what it opened with, and the row's
// own state and end time win, so the history never shows an interrupted poll
// as open.
func storedResult(p db.Poll) polls.Result {
	var r polls.Result
	_ = json.Unmarshal(p.Result, &r)
	r.ID = p.ID
	r.Question = p.Question
	r.State = polls.State(p.State)
	r.DurationSeconds = p.DurationS
	r.StartedAt = p.StartedAt
	r.EndsAt = p.EndsAt
	if !p.EndedAt.IsZero() {
		t := p.EndedAt
		r.EndedAt = &t
	}
	if len(r.Options) != len(p.Options) {
		r.Options = make([]polls.OptionResult, len(p.Options))
		for i, l := range p.Options {
			r.Options[i] = polls.OptionResult{Label: l, ByPlatform: map[string]int{}}
		}
	}
	if r.ByPlatform == nil {
		r.ByPlatform = map[string]int{}
	}
	return r
}

type pollsResponse struct {
	// Current is the running poll, or null.
	Current *polls.Result  `json:"current"`
	History []polls.Result `json:"history"`
}

// pollQuery reads ?since=, ?until= (RFC 3339) and ?limit=.
func pollQuery(r *http.Request) (db.PollQuery, error) {
	var q db.PollQuery
	for name, dst := range map[string]*time.Time{"since": &q.Since, "until": &q.Until} {
		if v := r.URL.Query().Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return q, fmt.Errorf("%s must be an RFC 3339 time", name)
			}
			*dst = t
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return q, errors.New("limit must be a positive number")
		}
		q.Limit = n
	}
	return q, nil
}

func (s *Server) pollHistory(q db.PollQuery) ([]polls.Result, error) {
	rows, err := s.store.Polls(q)
	if err != nil {
		return nil, err
	}
	out := make([]polls.Result, 0, len(rows))
	for _, p := range rows {
		out = append(out, storedResult(p))
	}
	return out, nil
}

// handleListPolls is the running poll and the history, newest first. The
// running poll is in both: Current is live, the history row is as stored.
func (s *Server) handleListPolls(w http.ResponseWriter, r *http.Request) {
	if !s.requirePolls(w) {
		return
	}
	q, err := pollQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hist, err := s.pollHistory(q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := pollsResponse{History: hist}
	if cur, ok := s.polls.Current(); ok {
		out.Current = &cur
		for i := range out.History {
			if out.History[i].ID == cur.ID {
				out.History[i] = cur
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requirePolls(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid poll id")
		return
	}
	if cur, ok := s.polls.Current(); ok && cur.ID == id {
		writeJSON(w, http.StatusOK, cur)
		return
	}
	p, err := s.store.Poll(id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such poll")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, storedResult(*p))
}

func (s *Server) handleStartPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requirePolls(w) {
		return
	}
	var spec polls.Spec
	if !decodeJSON(w, r, &spec) {
		return
	}
	res, err := s.polls.Start(spec)
	switch {
	case errors.Is(err, polls.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, polls.ErrRunning):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleEndPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requirePolls(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid poll id")
		return
	}
	res, err := s.polls.End(id)
	if errors.Is(err, polls.ErrNoPoll) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleExportPolls is the history as CSV (the default) or JSON, optionally
// one poll (?id=) or a time range (?since=, ?until=). One CSV row per option
// per platform, plus a "total" row per option, so a spreadsheet pivot needs no
// unpacking. Counts only, like every other surface.
func (s *Server) handleExportPolls(w http.ResponseWriter, r *http.Request) {
	if !s.requirePolls(w) {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		writeError(w, http.StatusBadRequest, `format must be "csv" or "json"`)
		return
	}
	var hist []polls.Result
	if v := r.URL.Query().Get("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid poll id")
			return
		}
		p, err := s.store.Poll(id)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such poll")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		hist = []polls.Result{storedResult(*p)}
	} else {
		q, err := pollQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if q.Limit == 0 {
			q.Limit = 1000
		}
		if hist, err = s.pollHistory(q); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	name := "polyemesis-polls." + format
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	if format == "json" {
		writeJSON(w, http.StatusOK, hist)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"poll_id", "started_at", "ended_at", "state", "question", "option", "label", "platform", "votes"})
	for _, p := range hist {
		ended := ""
		if p.EndedAt != nil {
			ended = p.EndedAt.UTC().Format(time.RFC3339)
		}
		for i, o := range p.Options {
			base := []string{strconv.FormatInt(p.ID, 10), p.StartedAt.UTC().Format(time.RFC3339), ended,
				string(p.State), csvSafe(p.Question), strconv.Itoa(i + 1), csvSafe(o.Label)}
			plats := make([]string, 0, len(o.ByPlatform))
			for pl := range o.ByPlatform {
				plats = append(plats, pl)
			}
			sort.Strings(plats)
			for _, pl := range plats {
				_ = cw.Write(append(append([]string(nil), base...), pl, strconv.Itoa(o.ByPlatform[pl])))
			}
			_ = cw.Write(append(append([]string(nil), base...), "total", strconv.Itoa(o.Votes)))
		}
	}
	cw.Flush()
}

// csvSafe defuses a cell a spreadsheet would run as a formula. Poll text is the
// operator's own, but an export is opened in Excel, and "=HYPERLINK(...)" in a
// question is a formula there.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ---------------------------------------------------------------- mirror

// nativePollMirror runs a poll as the platforms' own polls: a Twitch poll on
// every connected Twitch account that holds the polls scope (Helix, with the
// account's token), and a YouTube live poll in every connected YouTube chat
// (liveChatMessages, through the chat adapter that owns that chat and its
// quota).
type nativePollMirror struct{ s *Server }

// youTubePollRef is a YouTube live poll: the adapter that created it and the
// poll message's id.
type youTubePollRef struct {
	poller chat.NativePoller
	pollID string
}

type twitchPollRef struct {
	accountID     int64
	broadcasterID string
	pollID        string
}

// PollMirror is the native-poll mirror for the polls engine.
func (s *Server) PollMirror() polls.Mirror { return nativePollMirror{s: s} }

func hasScope(granted, want string) bool {
	for _, g := range strings.Fields(strings.ReplaceAll(granted, ",", " ")) {
		if g == want {
			return true
		}
	}
	return false
}

func (m nativePollMirror) twitch() (*oauth.Twitch, error) {
	p, err := m.s.providers.Get(db.PlatformTwitch)
	if err != nil {
		return nil, err
	}
	tw, ok := p.(*oauth.Twitch)
	if !ok {
		return nil, errors.New("twitch provider is not available")
	}
	return tw, nil
}

func skipped(reason string) polls.Mirrored {
	return polls.Mirrored{Result: polls.NativeResult{Platform: string(db.PlatformTwitch), Status: polls.NativeSkipped, Reason: reason}}
}

func ytSkipped(reason string) polls.Mirrored {
	return polls.Mirrored{Result: polls.NativeResult{Platform: string(db.PlatformYouTube), Status: polls.NativeSkipped, Reason: reason}}
}

// Start implements polls.Mirror.
func (m nativePollMirror) Start(ctx context.Context, spec polls.Spec) []polls.Mirrored {
	var out []polls.Mirrored
	if spec.MirrorTwitch {
		out = append(out, m.startTwitch(ctx, spec)...)
	}
	if spec.MirrorYouTube {
		out = append(out, m.startYouTube(ctx, spec)...)
	}
	return out
}

// startYouTube creates a live poll in every connected YouTube chat that can
// run one. A refusal -- too many options, no live chat open, the quota above
// the send reserve too low -- leaves that chat voting by number, and says why.
func (m nativePollMirror) startYouTube(ctx context.Context, spec polls.Spec) []polls.Mirrored {
	if len(spec.Options) > chat.YouTubePollMaxOptions {
		return []polls.Mirrored{ytSkipped(fmt.Sprintf("YouTube polls have at most %d options", chat.YouTubePollMaxOptions))}
	}
	if m.s.chat == nil {
		return []polls.Mirrored{ytSkipped("YouTube chat is not connected")}
	}
	pollers := m.s.chat.NativePollers(db.PlatformYouTube)
	if len(pollers) == 0 {
		return []polls.Mirrored{ytSkipped("YouTube chat is not connected")}
	}
	var out []polls.Mirrored
	for _, p := range pollers {
		id, err := p.CreatePoll(ctx, spec.Question, spec.Options)
		if err != nil {
			status := polls.NativeFailed
			if errors.Is(err, chat.ErrPollQuota) {
				status = polls.NativeSkipped
			}
			out = append(out, polls.Mirrored{Account: p.Account(), Result: polls.NativeResult{
				Platform: string(db.PlatformYouTube), Status: status, Reason: alerts.Redact(err.Error())}})
			continue
		}
		out = append(out, polls.Mirrored{
			Account: p.Account(),
			Result: polls.NativeResult{Platform: string(db.PlatformYouTube), Status: polls.NativeMirrored,
				Reason: "YouTube reports a live poll's count only when it closes; it is added then"},
			Ref: youTubePollRef{poller: p, pollID: id},
		})
	}
	return out
}

func (m nativePollMirror) startTwitch(ctx context.Context, spec polls.Spec) []polls.Mirrored {
	accts, err := m.s.store.ListPlatformAccounts()
	if err != nil {
		return []polls.Mirrored{{Result: polls.NativeResult{Platform: "twitch", Status: polls.NativeFailed,
			Reason: "could not read the connected accounts"}}}
	}
	var twitchAccts []db.PlatformAccount
	for _, a := range accts {
		if a.Platform == db.PlatformTwitch {
			twitchAccts = append(twitchAccts, a)
		}
	}
	if len(twitchAccts) == 0 {
		return []polls.Mirrored{skipped("no Twitch account is connected")}
	}
	if reason := oauth.TwitchPollFits(spec.Question, spec.Options, spec.DurationSeconds); reason != "" {
		return []polls.Mirrored{skipped(reason)}
	}
	tw, err := m.twitch()
	if err != nil {
		return []polls.Mirrored{skipped(err.Error())}
	}
	creds, err := m.s.store.GetPlatformCreds(m.s.box, db.PlatformTwitch)
	if err != nil {
		return []polls.Mirrored{skipped("Twitch developer credentials are missing")}
	}
	var out []polls.Mirrored
	for _, a := range twitchAccts {
		if !hasScope(a.Scopes, "channel:manage:polls") {
			out = append(out, skipped("reconnect this Twitch account to allow native polls"))
			continue
		}
		acct, err := m.s.tokenFor(ctx, a.ID)
		if err != nil {
			out = append(out, polls.Mirrored{Result: polls.NativeResult{Platform: "twitch", Status: polls.NativeFailed,
				Reason: alerts.Redact(err.Error())}})
			continue
		}
		p, err := tw.CreatePoll(ctx, creds.ClientID, acct.AccessToken, acct.AccountRef, spec.Question, spec.Options, spec.DurationSeconds)
		if err != nil {
			// A 403 is the ordinary answer for a channel that is not an
			// Affiliate or Partner. Said once, on the poll.
			out = append(out, polls.Mirrored{Result: polls.NativeResult{Platform: "twitch", Status: polls.NativeFailed,
				Reason: alerts.Redact(err.Error())}})
			continue
		}
		out = append(out, polls.Mirrored{
			Account: acct.AccountRef,
			Result:  polls.NativeResult{Platform: "twitch", Status: polls.NativeMirrored, Votes: p.Votes},
			Ref:     twitchPollRef{accountID: a.ID, broadcasterID: acct.AccountRef, pollID: p.ID},
		})
	}
	return out
}

// Read implements polls.Mirror.
func (m nativePollMirror) Read(ctx context.Context, mm polls.Mirrored, end bool) ([]int, bool, error) {
	switch ref := mm.Ref.(type) {
	case twitchPollRef:
		return m.readTwitch(ctx, ref, end)
	case youTubePollRef:
		if !end {
			// No API reads one running chat message: nothing to report yet.
			return nil, true, nil
		}
		votes, err := ref.poller.ClosePoll(ctx, ref.pollID)
		return votes, false, err
	}
	return nil, false, errors.New("not a native poll this server created")
}

// readTwitch reads, or ends and reads, a Twitch poll. Live is Twitch's own
// status: anything but ACTIVE before the chat poll ended is a poll that was
// closed, archived or removed on Twitch's side.
func (m nativePollMirror) readTwitch(ctx context.Context, ref twitchPollRef, end bool) ([]int, bool, error) {
	tw, err := m.twitch()
	if err != nil {
		return nil, false, err
	}
	creds, err := m.s.store.GetPlatformCreds(m.s.box, db.PlatformTwitch)
	if err != nil {
		return nil, false, err
	}
	acct, err := m.s.tokenFor(ctx, ref.accountID)
	if err != nil {
		return nil, false, err
	}
	if end {
		p, err := tw.EndPoll(ctx, creds.ClientID, acct.AccessToken, ref.broadcasterID, ref.pollID)
		if err == nil {
			return p.Votes, false, nil
		}
		// Already over on Twitch's side (it ran its own clock): read the
		// final count instead.
	}
	p, err := tw.GetPoll(ctx, creds.ClientID, acct.AccessToken, ref.broadcasterID, ref.pollID)
	if err != nil {
		return nil, false, err
	}
	return p.Votes, p.Status == "ACTIVE", nil
}
