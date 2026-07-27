package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Twitch implements Twitch OAuth plus the Helix stream-key lookup.
type Twitch struct{}

func (t *Twitch) Platform() db.Platform { return db.PlatformTwitch }

func (t *Twitch) Scopes() []string {
	// The minimum for what polyemesis actually does with a Twitch account: read
	// the stream key, and write the title/category before going live. Helix
	// Modify Channel Information refuses the second without its own scope, and
	// the failure is a 401 the operator cannot fix by reconnecting — the scope
	// has to be in the consent they granted.
	//
	// user:read:email is still not requested: we do not need it, and asking
	// would make the consent screen scarier than the feature warrants.
	return []string{"channel:read:stream_key", "channel:manage:broadcast"}
}

// PKCE is off, and the challenge/verifier arguments below are deliberately
// discarded. Twitch's authorization-code documentation enumerates an exact
// parameter set (client_id, force_verify, redirect_uri, response_type, scope,
// state) and says nothing about RFC 7636; nothing published tells us whether
// its /authorize endpoint tolerates an unknown code_challenge or rejects the
// request. Sending one on a hunch would break Twitch sign-in for everyone, so
// this stays off until Twitch documents support. The flow is still a
// confidential client: the secret never leaves the server, the code is bound to
// a whitelisted redirect URI, and the state is single-use.
func (t *Twitch) PKCE() bool { return false }

func (t *Twitch) AuthURL(clientID, redirectURI, state, _ string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(t.Scopes(), " "))
	q.Set("state", state)
	// force_verify makes account switching possible: without it Twitch silently
	// reuses the browser's logged-in account, so connecting a second channel is
	// impossible.
	q.Set("force_verify", "true")
	return "https://id.twitch.tv/oauth2/authorize?" + q.Encode()
}

func (t *Twitch) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, _ string) (*Token, error) {
	return postForm(ctx, "https://id.twitch.tv/oauth2/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}, nil)
}

func (t *Twitch) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, "https://id.twitch.tv/oauth2/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}, nil)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// helixHeaders: Helix requires the Client-Id alongside the bearer token, which
// is why every provider method takes clientID.
func helixHeaders(clientID string) map[string]string {
	return map[string]string{"Client-Id": clientID}
}

func (t *Twitch) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://api.twitch.tv/helix/users", accessToken, helixHeaders(clientID), &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("Twitch returned no user for this token")
	}
	name := out.Data[0].DisplayName
	if name == "" {
		name = out.Data[0].Login
	}
	return &Account{Name: name, Ref: out.Data[0].ID}, nil
}

// twitchIngestURL is Twitch's global ingest hostname, which resolves to the
// nearest PoP. The /ingests endpoint offers a ranked list, but the automatic
// endpoint is what Twitch itself recommends and avoids pinning a user to a
// server that is nearest to the *polyemesis host* rather than to them.
const twitchIngestURL = "rtmp://live.twitch.tv/app"

func (t *Twitch) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	acct, err := t.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			StreamKey string `json:"stream_key"`
		} `json:"data"`
	}
	err = getJSON(ctx,
		"https://api.twitch.tv/helix/streams/key?broadcaster_id="+url.QueryEscape(acct.Ref),
		accessToken, helixHeaders(clientID), &out)
	if err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || out.Data[0].StreamKey == "" {
		return nil, fmt.Errorf("Twitch returned no stream key; make sure the app requested the %s scope",
			strings.Join(t.Scopes(), " "))
	}
	return &Ingest{URL: twitchIngestURL, Key: out.Data[0].StreamKey}, nil
}
