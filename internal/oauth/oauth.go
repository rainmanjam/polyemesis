// Package oauth implements per-platform sign-in and stream-key retrieval.
//
// polyemesis cannot ship client secrets, so the operator registers their own
// developer app and pastes the client ID/secret into Settings. Everything here
// runs the authorization-code flow against those credentials, stores the
// resulting tokens encrypted (see internal/secrets), and refreshes them
// automatically.
//
// On Kick: as of this build, Kick's public API does not document an endpoint
// that returns a channel's stream key or ingest endpoint. Rather than ship an
// OAuth flow against an endpoint we cannot verify, Kick is implemented as a
// branded destination preset where the user pastes their RTMPS URL and key.
// See Presets() below. If Kick publishes such an endpoint, adding a Provider
// here is the only change required.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Token is the result of an authorization-code exchange or a refresh.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       string
}

// Account identifies the connected channel.
type Account struct {
	Name string
	Ref  string
}

// Ingest is where to publish for this account.
type Ingest struct {
	URL string
	Key string
}

// Provider is one platform integration.
type Provider interface {
	Platform() db.Platform
	// Scopes are requested at authorization time and shown in the UI so the
	// user knows what they are granting.
	Scopes() []string
	// PKCE reports whether this platform accepts RFC 7636 parameters. It is
	// opt-in per provider rather than on by default: a platform whose
	// /authorize endpoint validates its query string strictly rejects an
	// unknown code_challenge outright, which locks every user out of sign-in.
	// That is a far worse outcome than doing without a defence-in-depth
	// measure on a confidential client that never exposes the code.
	PKCE() bool
	// AuthURL builds the consent URL. challenge is the S256 code_challenge,
	// and is empty whenever PKCE reports false.
	AuthURL(clientID, redirectURI, state, challenge string) string
	// Exchange trades the authorization code for tokens. verifier is the
	// code_verifier matching the challenge given to AuthURL, empty when the
	// provider does not do PKCE.
	Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error)
	Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error)
	Account(ctx context.Context, clientID, accessToken string) (*Account, error)
	// Ingest fetches the ingest endpoint and stream key, so the user never
	// copy-pastes a key.
	Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error)
}

// Providers returns every implemented provider, keyed by platform.
func Providers() map[db.Platform]Provider {
	return map[db.Platform]Provider{
		db.PlatformYouTube: &YouTube{},
		db.PlatformTwitch:  &Twitch{},
	}
}

// Get returns a provider, or an error naming the platform.
func Get(p db.Platform) (Provider, error) {
	if pr, ok := Providers()[p]; ok {
		return pr, nil
	}
	if p == db.PlatformKick {
		return nil, fmt.Errorf("kick does not expose a stream key over its public API; " +
			"add a Kick destination and paste your RTMPS URL and key from the Kick creator dashboard")
	}
	return nil, fmt.Errorf("no OAuth provider for platform %q", p)
}

// httpClient is shared; the timeout keeps a hung platform API from wedging a
// request handler.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// postForm performs an OAuth token request and decodes the standard response.
func postForm(ctx context.Context, endpoint string, form url.Values, headers map[string]string) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}

	var out struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    int             `json:"expires_in"`
		Scope        json.RawMessage `json:"scope"`
		Error        string          `json:"error"`
		ErrorDesc    string          `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s: %s", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access_token")
	}

	t := &Token{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Scopes:       decodeScope(out.Scope),
	}
	if out.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return t, nil
}

// decodeScope handles both spellings: Google returns a space-delimited string,
// Twitch returns an array.
func decodeScope(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, " ")
	}
	return ""
}

// getJSON performs an authenticated GET and decodes into out.
func getJSON(ctx context.Context, endpoint, accessToken string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("the platform rejected the access token (401); reconnect the account")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the platform refused the request (403): %s", snippet(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, snippet(body))
	}
	return json.Unmarshal(body, out)
}

func postJSON(ctx context.Context, endpoint, accessToken string, payload any, headers map[string]string, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, snippet(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// SetupGuide is the in-UI instructions for registering a developer app.
type SetupGuide struct {
	Platform db.Platform `json:"platform"`
	Name     string      `json:"name"`
	// ConsoleURL is where the user registers the app.
	ConsoleURL string `json:"consoleUrl"`
	// RedirectPath is appended to the server's origin to form the redirect URI
	// the user must whitelist.
	RedirectPath string   `json:"redirectPath"`
	Steps        []string `json:"steps"`
	Scopes       []string `json:"scopes"`
	// Supported reports whether polyemesis can fetch the stream key
	// automatically for this platform.
	Supported bool   `json:"supported"`
	Note      string `json:"note,omitempty"`
}

// Guides returns the setup instructions rendered on the credentials page.
func Guides() []SetupGuide {
	return []SetupGuide{
		{
			Platform:     db.PlatformYouTube,
			Name:         "YouTube (Google)",
			ConsoleURL:   "https://console.cloud.google.com/apis/credentials",
			RedirectPath: "/api/v1/oauth/youtube/callback",
			Supported:    true,
			Scopes:       (&YouTube{}).Scopes(),
			Steps: []string{
				"Open the Google Cloud Console and create a project (or pick an existing one).",
				"In APIs & Services → Library, enable the “YouTube Data API v3”.",
				"In APIs & Services → OAuth consent screen, choose External, fill in the app name and your email, and add your own Google account under Test users. You do not need to publish the app.",
				"In APIs & Services → Credentials, click Create Credentials → OAuth client ID → Web application.",
				"Under “Authorised redirect URIs”, add exactly the redirect URI shown below.",
				"Copy the Client ID and Client secret into the fields on this page and save.",
				"Go to a destination and click Connect account. YouTube will ask you to grant access, then polyemesis fetches your ingest URL and stream key automatically.",
			},
		},
		{
			Platform:     db.PlatformTwitch,
			Name:         "Twitch",
			ConsoleURL:   "https://dev.twitch.tv/console/apps",
			RedirectPath: "/api/v1/oauth/twitch/callback",
			Supported:    true,
			Scopes:       (&Twitch{}).Scopes(),
			Steps: []string{
				"Open the Twitch Developer Console and click Register Your Application.",
				"Give it any name, set the OAuth Redirect URL to exactly the URI shown below, and pick category “Broadcasting Suite”.",
				"Set Client Type to Confidential — polyemesis exchanges the code server-side and needs a client secret.",
				"Click Manage on the new app, then New Secret, and copy both the Client ID and the secret into the fields on this page.",
				"Go to a destination and click Connect account. polyemesis reads your stream key via the Helix API.",
			},
		},
		{
			Platform:   db.PlatformKick,
			Name:       "Kick",
			ConsoleURL: "https://kick.com/dashboard/settings/stream",
			Supported:  false,
			Note: "Kick's public API does not expose stream keys, so there is nothing to connect. " +
				"Add a Kick destination and paste the RTMPS ingest URL and stream key from your Kick creator dashboard " +
				"(Settings → Stream). Everything else — routing, meters, reconnect — works identically.",
			Steps: []string{
				"Open your Kick creator dashboard → Settings → Stream.",
				"Copy the “Stream URL” (it starts with rtmps://) and the “Stream Key”.",
				"In polyemesis, add a destination, choose the Kick preset, and paste both values.",
			},
		},
	}
}
