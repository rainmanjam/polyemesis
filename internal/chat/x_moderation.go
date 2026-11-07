package chat

// X chat moderation beyond a single ban or delete: the list of muted viewers, mute-and-delete in one call, and the
// broadcaster's chat moderators.
//
// Every route here is documented at docs.x.com/livestream-api/chat and none has run against a live X account.
//
//	GET    /2/broadcasts/chat/mutes              list-muted-chat-users   broadcast.read
//	POST   /2/broadcasts/chat/mutes              mute-user (message_id)  broadcast.read + broadcast.write
//	GET    /2/broadcasts/chat/moderators         list-moderators         tweet.read + broadcast.read + users.read
//	POST   /2/broadcasts/chat/moderators         add-moderator           broadcast.read + broadcast.write
//	DELETE /2/broadcasts/chat/moderators/{id}    remove-moderator        broadcast.read + broadcast.write
//
// All five scopes are already in oauth.X.Scopes, so none of this needs a reconnect.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChatMute is one muted viewer. EndsAt is nil for an indefinite mute; X omits end_at_ms for those and leaves expired
// timed mutes out of the list altogether.
type ChatMute struct {
	UserID   string     `json:"userId"`
	Username string     `json:"username,omitempty"`
	Name     string     `json:"name,omitempty"`
	EndsAt   *time.Time `json:"endsAt,omitempty"`
}

// ChatModerator is one of the broadcaster's chat moderators.
type ChatModerator struct {
	UserID   string `json:"userId"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
}

// MuteLister is the optional capability to read who is muted and until when.
type MuteLister interface {
	Adapter
	ListMutes(ctx context.Context) ([]ChatMute, error)
}

// MessageBanner is the optional capability to mute a viewer AND remove one of their messages in a single call.
type MessageBanner interface {
	Adapter
	BanDeleting(ctx context.Context, userID string, d time.Duration, reason, messageID string) error
}

// ModeratorManager is the optional capability to manage the chat moderators who act across every broadcast the host
// runs.
type ModeratorManager interface {
	Adapter
	ListModerators(ctx context.Context) ([]ChatModerator, error)
	AddModerator(ctx context.Context, userID string) ([]string, error)
	RemoveModerator(ctx context.Context, userID string) ([]string, error)
}

// The X adapter takes all three, checked at compile time so a signature that drifts cannot silently drop a control.
var (
	_ MuteLister       = (*XAdapter)(nil)
	_ MessageBanner    = (*XAdapter)(nil)
	_ ModeratorManager = (*XAdapter)(nil)
)

// xMuteMessageID is the shape the mute route accepts for message_id: unlike delete's, no leading zero.
var xMuteMessageID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// xUserIDNoZero is the shape the mute and moderator routes accept for user_id.
var xUserIDNoZero = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// xMaxModerators is X's cap on a broadcaster's moderators.
const xMaxModerators = 20

// xMuteListPages bounds the pagination of the mute list. X sets next_token on any full page, so the loop also stops
// on an empty page; this is the backstop.
const xMuteListPages = 20

// xRefusal turns a failed X call into a sentence that carries X's own title and detail and the operator's next step
// for the statuses X documents. The raw body never reaches the sentence: only its title, detail and message fields.
func xRefusal(err error) string {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	var env struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal([]byte(ae.Body), &env)
	words := strings.TrimSpace(strings.Join(nonEmpty(env.Title, env.Detail), ": "))
	if words == "" && len(env.Errors) > 0 {
		words = env.Errors[0].Message
	}
	var hint string
	switch ae.Status {
	case http.StatusUnauthorized:
		hint = "reconnect the X account in Settings, Platforms"
	case http.StatusForbidden:
		hint = "this account is not allowed to do that on X (a moderator can mute but cannot list the owner's mutes)"
	case http.StatusTooManyRequests:
		hint = "X is rate limiting this account; wait a few minutes"
	}
	switch {
	case words != "" && hint != "":
		return words + " (" + hint + ")"
	case words != "":
		return words
	case hint != "":
		return hint
	}
	return "X answered " + strconv.Itoa(ae.Status)
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// ------------------------------------------------------------ list mutes

// ListMutes reads the broadcaster's muted viewers with the end of each timed mute. end_at_ms is selected by
// broadcast_chat_mute.fields; without it every timed mute would read as indefinite. Only the broadcaster's own mutes
// are listed, so a moderator's token cannot use this for another host.
func (x *XAdapter) ListMutes(ctx context.Context) ([]ChatMute, error) {
	var (
		out   []ChatMute
		token string
	)
	for page := 0; page < xMuteListPages; page++ {
		q := url.Values{
			"max_results":                {"100"},
			"expansions":                 {"user_id"},
			"user.fields":                {"username,name"},
			"broadcast_chat_mute.fields": {"end_at_ms"},
		}
		if token != "" {
			q.Set("pagination_token", token)
		}
		var resp struct {
			Data []struct {
				UserID  string `json:"user_id"`
				EndAtMS string `json:"end_at_ms"`
			} `json:"data"`
			Includes struct {
				Users []struct {
					ID       string `json:"id"`
					Username string `json:"username"`
					Name     string `json:"name"`
				} `json:"users"`
			} `json:"includes"`
			Meta struct {
				NextToken string `json:"next_token"`
			} `json:"meta"`
		}
		if err := x.xCall(ctx, http.MethodGet, xBroadcastsPath+"chat/mutes?"+q.Encode(), false, nil, &resp); err != nil {
			return nil, fmt.Errorf("X would not list the muted viewers: %s", xRefusal(err))
		}
		users := make(map[string]int, len(resp.Includes.Users))
		for i, u := range resp.Includes.Users {
			users[u.ID] = i
		}
		for _, m := range resp.Data {
			row := ChatMute{UserID: m.UserID}
			if i, ok := users[m.UserID]; ok {
				row.Username, row.Name = resp.Includes.Users[i].Username, resp.Includes.Users[i].Name
			}
			if ms, err := strconv.ParseInt(strings.TrimSpace(m.EndAtMS), 10, 64); err == nil && ms > 0 {
				t := time.UnixMilli(ms).UTC()
				row.EndsAt = &t
			}
			out = append(out, row)
		}
		// X sets next_token on every full page, so a last page can be followed by an empty one.
		if len(resp.Data) == 0 || resp.Meta.NextToken == "" {
			break
		}
		token = resp.Meta.NextToken
	}
	return out, nil
}

// ------------------------------------------------------- mute and delete

// BanDeleting mutes a viewer and removes one message of theirs in the same call: POST /2/broadcasts/chat/mutes with
// broadcast_id and message_id. X requires broadcast_id whenever message_id is set, and that broadcast must be live,
// so unlike a plain mute this needs the running broadcast. A zero duration is an indefinite mute.
func (x *XAdapter) BanDeleting(ctx context.Context, userID string, d time.Duration, _, messageID string) error {
	userID = strings.TrimSpace(userID)
	if !xUserIDNoZero.MatchString(userID) {
		return fmt.Errorf("X user id %q is not digits, so this mute cannot be addressed", userID)
	}
	messageID = strings.TrimSpace(messageID)
	if !xMuteMessageID.MatchString(messageID) {
		return fmt.Errorf("X message id %q is not the digits the mute call takes, so the message cannot be removed with it", messageID)
	}
	id, err := x.currentBroadcastID(ctx)
	if err != nil {
		return err
	}
	body := map[string]string{"user_id": userID, "broadcast_id": id, "message_id": messageID}
	if d > 0 {
		body["end_at_ms"] = strconv.FormatInt(x.cfg.Now().Add(d).UnixMilli(), 10)
	}
	var out struct {
		Data struct {
			Muted bool `json:"muted"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, xBroadcastsPath+"chat/mutes", false, body, &out); err != nil {
		return fmt.Errorf("X refused to mute and remove: %s", xRefusal(err))
	}
	if !out.Data.Muted {
		return errors.New("X did not confirm the viewer was muted")
	}
	return nil
}

// ------------------------------------------------------------ moderators

// ListModerators reads the broadcaster's chat moderators. They are tied to the account, not to a broadcast, so the
// list is the same for every current and future broadcast of the host.
func (x *XAdapter) ListModerators(ctx context.Context) ([]ChatModerator, error) {
	var resp struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"data"`
	}
	path := xBroadcastsPath + "chat/moderators?" + url.Values{"user.fields": {"username,name"}}.Encode()
	if err := x.xCall(ctx, http.MethodGet, path, false, nil, &resp); err != nil {
		return nil, fmt.Errorf("X would not list the chat moderators: %s", xRefusal(err))
	}
	out := make([]ChatModerator, 0, len(resp.Data))
	for _, u := range resp.Data {
		out = append(out, ChatModerator{UserID: u.ID, Username: u.Username, Name: u.Name})
	}
	return out, nil
}

// AddModerator appoints a viewer, by X user id, as a chat moderator on every broadcast the account hosts, and returns
// the ids of all moderators afterwards. X allows 20; the 21st is a 400, which is reported as the limit.
func (x *XAdapter) AddModerator(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if !xUserIDNoZero.MatchString(userID) {
		return nil, fmt.Errorf("X user id %q is not digits, so this moderator cannot be addressed", userID)
	}
	var out struct {
		Data struct {
			IDs []string `json:"moderator_user_ids"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, xBroadcastsPath+"chat/moderators", false,
		map[string]string{"user_id": userID}, &out); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
			return nil, fmt.Errorf("X refused the moderator: %s (an account can have at most %d chat moderators; "+
				"remove one first if that is the limit)", xRefusal(err), xMaxModerators)
		}
		return nil, fmt.Errorf("X refused the moderator: %s", xRefusal(err))
	}
	return out.Data.IDs, nil
}

// RemoveModerator takes the moderator role away from a viewer on every broadcast the account hosts, and returns the
// ids of the moderators that remain.
func (x *XAdapter) RemoveModerator(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if !xUserIDNoZero.MatchString(userID) {
		return nil, fmt.Errorf("X user id %q is not digits, so this moderator cannot be addressed", userID)
	}
	var out struct {
		Data struct {
			IDs []string `json:"moderator_user_ids"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodDelete, xBroadcastsPath+"chat/moderators/"+userID, false, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not remove the moderator: %s", xRefusal(err))
	}
	return out.Data.IDs, nil
}
