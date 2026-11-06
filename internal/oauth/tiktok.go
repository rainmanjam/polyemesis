package oauth

// TikTok sign-in, for ONE capability: posting a finished clip with the Content
// Posting API (Wave 4 #5c). There is no TikTok destination, no stream key and no
// live integration here -- TikTok LIVE stays a pasted key (see the "tiktok" row
// in capabilities.go, which this deliberately leaves alone). This provider
// exists so an operator can connect the account a short will be posted to,
// through the same OAuth flow, settings page and token refresh as every other
// platform.
//
// Endpoints and fields are from TikTok's Login Kit reference (read 2026-11-02;
// docs/evidence/clip-publishing-apis-2026-11-02.md):
//
//	authorize  https://www.tiktok.com/v2/auth/authorize/   client_key, scope (comma separated)
//	token      POST https://open.tiktokapis.com/v2/oauth/token/   form encoded, client_key + client_secret
//	user info  GET  https://open.tiktokapis.com/v2/user/info/?fields=open_id,display_name
//
// The posting scope is video.publish. user.info.basic is what lets us read
// which TikTok account the token belongs to.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// TikTok implements Provider for sign-in only.
type TikTok struct {
	endpoints
}

const (
	tikTokAuthBase = "https://www.tiktok.com"
	tikTokAPIBase  = platform.TikTokAPIHost
)

var (
	_ Provider  = (*TikTok)(nil)
	_ ManualKey = (*TikTok)(nil)
)

// NewTikTok builds the provider. With no options it talks to TikTok's hosts.
func NewTikTok(opts ...ProviderOption) *TikTok {
	return &TikTok{endpoints: newEndpoints(opts)}
}

func (t *TikTok) apiEndpoint() string  { return t.apiBase(tikTokAPIBase) }
func (t *TikTok) authEndpoint() string { return t.authBase(tikTokAuthBase) }

func (t *TikTok) Platform() db.Platform { return db.PlatformTikTok }

// Scopes: user.info.basic to name the account, video.publish to post.
func (t *TikTok) Scopes() []string { return []string{"user.info.basic", platform.TikTokPublishScope} }

// ScopeVersion is 1: no TikTok account existed before this.
func (t *TikTok) ScopeVersion() int { return 1 }

// PKCE is off. TikTok's web flow does not require it, and a strict authorize
// endpoint that rejects an unknown parameter would lock sign-in out.
func (t *TikTok) PKCE() bool { return false }

func (t *TikTok) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_key", clientID)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(t.Scopes(), ","))
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	_ = challenge
	return t.authEndpoint() + "/v2/auth/authorize/?" + q.Encode()
}

func (t *TikTok) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	_ = verifier
	return postForm(ctx, t.apiEndpoint()+"/v2/oauth/token/", url.Values{
		"client_key":    {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}, nil)
}

func (t *TikTok) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	return postForm(ctx, t.apiEndpoint()+"/v2/oauth/token/", url.Values{
		"client_key":    {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}, nil)
}

type tikTokUser struct {
	Data struct {
		User struct {
			OpenID      string `json:"open_id"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (t *TikTok) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var u tikTokUser
	if err := t.getJSON(ctx, t.apiEndpoint()+"/v2/user/info/?fields=open_id,display_name", accessToken, nil, &u); err != nil {
		return nil, err
	}
	if c := u.Error.Code; c != "" && c != "ok" {
		return nil, fmt.Errorf("TikTok refused the account read (%s): %s", c, u.Error.Message)
	}
	ref := strings.TrimSpace(u.Data.User.OpenID)
	name := strings.TrimSpace(u.Data.User.DisplayName)
	if name == "" {
		name = ref
	}
	if ref == "" {
		return nil, errors.New("TikTok returned no identity for this token; make sure the app was granted user.info.basic")
	}
	return &Account{Name: name, Ref: ref}, nil
}

// Ingest: TikTok has no stream-key API for this integration.
func (t *TikTok) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	return nil, fmt.Errorf("%w. TikTok is connected here only to post finished clips; TikTok LIVE stays a pasted server URL and key", ErrNoStreamKeyAPI)
}

// ManualKeyReason: a TikTok account here posts clips and nothing else.
func (t *TikTok) ManualKeyReason() string {
	return "TikTok issues the LIVE server URL and key per broadcast, and only to accounts granted LIVE access, " +
		"so polyemesis cannot fetch one. Connecting a TikTok account is for posting finished clips; " +
		"copy the URL and key from TikTok LIVE Studio or LIVE Center and paste them into the destination."
}
