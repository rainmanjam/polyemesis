package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"unicode/utf8"
)

// Native Twitch polls (#14): Create Poll, Get Polls and End Poll on Helix,
// under the channel:manage:polls scope (ScopeVersion 5). Twitch runs polls for
// Affiliates and Partners only, and answers 403 for any other channel.
//
// The limits are Twitch's, from the Create Poll request-body table: 2 to 5
// choices, a title of at most 60 characters, a choice of at most 25, and a
// duration of 15 to 1800 seconds. A poll outside them is not mirrored -- see
// TwitchPollFits -- rather than shortened, because a native poll whose options
// read differently from the chat poll's would be two polls.
const (
	twitchPollsPath       = "/polls"
	TwitchPollMaxChoices  = 5
	TwitchPollMaxTitle    = 60
	TwitchPollMaxChoice   = 25
	TwitchPollMinDuration = 15
	TwitchPollMaxDuration = 1800
)

// TwitchPoll is a native poll as polyemesis needs it: its id, Twitch's status,
// and the vote count of each choice in order. Twitch's counts are anonymous.
type TwitchPoll struct {
	ID     string
	Status string
	Votes  []int
}

type twitchPollJSON struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Choices []struct {
		Votes int `json:"votes"`
	} `json:"choices"`
}

func (p twitchPollJSON) poll() *TwitchPoll {
	out := &TwitchPoll{ID: p.ID, Status: p.Status}
	for _, c := range p.Choices {
		out.Votes = append(out.Votes, c.Votes)
	}
	return out
}

func firstPoll(data []twitchPollJSON) (*TwitchPoll, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("twitch answered with no poll")
	}
	return data[0].poll(), nil
}

// TwitchPollFits says why a poll cannot be mirrored to Twitch, or "" when it
// can.
func TwitchPollFits(title string, choices []string, seconds int) string {
	switch {
	case len(choices) > TwitchPollMaxChoices:
		return fmt.Sprintf("Twitch polls have at most %d options", TwitchPollMaxChoices)
	case utf8.RuneCountInString(title) > TwitchPollMaxTitle:
		return fmt.Sprintf("Twitch poll questions are at most %d characters", TwitchPollMaxTitle)
	case seconds < TwitchPollMinDuration || seconds > TwitchPollMaxDuration:
		return fmt.Sprintf("Twitch polls run %d to %d seconds", TwitchPollMinDuration, TwitchPollMaxDuration)
	}
	for _, c := range choices {
		if utf8.RuneCountInString(c) > TwitchPollMaxChoice {
			return fmt.Sprintf("Twitch poll options are at most %d characters", TwitchPollMaxChoice)
		}
	}
	return ""
}

// CreatePoll starts a native poll on the broadcaster's channel. Channel-points
// voting is sent explicitly OFF: #14 leaves channel points out, and a viewer
// buying extra votes would break one vote per viewer.
func (t *Twitch) CreatePoll(ctx context.Context, clientID, accessToken, broadcasterID, title string, choices []string, seconds int) (*TwitchPoll, error) {
	type choice struct {
		Title string `json:"title"`
	}
	body := struct {
		BroadcasterID string   `json:"broadcaster_id"`
		Title         string   `json:"title"`
		Choices       []choice `json:"choices"`
		Duration      int      `json:"duration"`
		ChannelPoints bool     `json:"channel_points_voting_enabled"`
	}{BroadcasterID: broadcasterID, Title: title, Duration: seconds}
	for _, c := range choices {
		body.Choices = append(body.Choices, choice{Title: c})
	}
	var out struct {
		Data []twitchPollJSON `json:"data"`
	}
	if err := requestJSON(ctx, http.MethodPost, t.apiEndpoint()+twitchPollsPath, accessToken, body,
		helixHeaders(clientID), &out); err != nil {
		return nil, err
	}
	return firstPoll(out.Data)
}

// GetPoll reads one native poll's running count.
func (t *Twitch) GetPoll(ctx context.Context, clientID, accessToken, broadcasterID, id string) (*TwitchPoll, error) {
	q := url.Values{"broadcaster_id": {broadcasterID}, "id": {id}}
	var out struct {
		Data []twitchPollJSON `json:"data"`
	}
	if err := requestJSON(ctx, http.MethodGet, t.apiEndpoint()+twitchPollsPath+"?"+q.Encode(), accessToken, nil,
		helixHeaders(clientID), &out); err != nil {
		return nil, err
	}
	return firstPoll(out.Data)
}

// EndPoll ends a native poll early, leaving it visible (TERMINATED, not
// ARCHIVED: viewers who voted there can still see how it came out), and
// returns its final count.
func (t *Twitch) EndPoll(ctx context.Context, clientID, accessToken, broadcasterID, id string) (*TwitchPoll, error) {
	body := map[string]string{"broadcaster_id": broadcasterID, "id": id, "status": "TERMINATED"}
	var out struct {
		Data []twitchPollJSON `json:"data"`
	}
	if err := requestJSON(ctx, http.MethodPatch, t.apiEndpoint()+twitchPollsPath, accessToken, body,
		helixHeaders(clientID), &out); err != nil {
		return nil, err
	}
	return firstPoll(out.Data)
}
