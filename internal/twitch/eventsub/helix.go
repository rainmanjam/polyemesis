package eventsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// API is the two Helix calls EventSub needs. Token supplies the user access
// token; Refresh, when set, is called once with the token Twitch just refused
// and must return a fresh one.
type API struct {
	Base     string
	ClientID string
	HTTP     *http.Client
	Token    func(ctx context.Context) (string, error)
	Refresh  func(ctx context.Context, rejected string) (string, error)
}

// APIError is Helix refusing a call. It never carries the response body: the
// status is all anyone acts on, and a body can echo a request.
type APIError struct {
	Status int
	// RetryAt is when a 429 says the limit resets, from Ratelimit-Reset.
	RetryAt time.Time
	What    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Twitch answered %d for %s", e.Status, e.What)
}

// IsStatus reports whether err is Helix answering with that status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// SubResult is a created subscription's cost accounting.
type SubResult struct {
	ID           string
	Cost         int
	TotalCost    int
	MaxTotalCost int
}

func (a *API) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (a *API) base() string {
	if a.Base != "" {
		return a.Base
	}
	return DefaultHelixBase
}

// do sends one authenticated request, refreshing the token once on a 401.
func (a *API) do(ctx context.Context, method, path string, body []byte, what string) (*http.Response, []byte, error) {
	tok, err := a.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, a.base()+path, rd)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Client-Id", a.ClientID)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.client().Do(req)
		if err != nil {
			// The transport error can carry the URL; the path is all we name.
			return nil, nil, fmt.Errorf("could not reach Twitch for %s", what)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && a.Refresh != nil {
			fresh, rerr := a.Refresh(ctx, tok)
			if rerr != nil {
				return nil, nil, rerr
			}
			tok = fresh
			continue
		}
		if resp.StatusCode/100 != 2 {
			ae := &APIError{Status: resp.StatusCode, What: what}
			if v := resp.Header.Get("Ratelimit-Reset"); v != "" {
				if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
					ae.RetryAt = time.Unix(n, 0)
				}
			}
			return resp, b, ae
		}
		return resp, b, nil
	}
}

// Subscribe creates one subscription on a WebSocket session.
func (a *API) Subscribe(ctx context.Context, sessionID string, s Spec, broadcasterID string) (SubResult, error) {
	body, err := json.Marshal(map[string]any{
		"type":      s.Type,
		"version":   s.Version,
		"condition": s.Condition(broadcasterID),
		"transport": map[string]string{"method": "websocket", "session_id": sessionID},
	})
	if err != nil {
		return SubResult{}, err
	}
	_, b, err := a.do(ctx, http.MethodPost, "/eventsub/subscriptions", body, s.Type)
	if err != nil {
		return SubResult{}, err
	}
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Cost int    `json:"cost"`
		} `json:"data"`
		TotalCost    int `json:"total_cost"`
		MaxTotalCost int `json:"max_total_cost"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Data) == 0 {
		return SubResult{}, fmt.Errorf("Twitch answered %s with something unreadable", s.Type)
	}
	return SubResult{ID: out.Data[0].ID, Cost: out.Data[0].Cost, TotalCost: out.TotalCost, MaxTotalCost: out.MaxTotalCost}, nil
}

// BroadcasterType reads the user's broadcaster_type: "affiliate", "partner"
// or "" (Get Users). It needs no scope.
func (a *API) BroadcasterType(ctx context.Context, userID string) (string, error) {
	_, b, err := a.do(ctx, http.MethodGet, "/users?id="+url.QueryEscape(userID), nil, "the channel's account type")
	if err != nil {
		return "", err
	}
	var out struct {
		Data []struct {
			ID              string `json:"id"`
			BroadcasterType string `json:"broadcaster_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Data) == 0 {
		return "", fmt.Errorf("Twitch answered the account lookup with something unreadable")
	}
	return out.Data[0].BroadcasterType, nil
}
