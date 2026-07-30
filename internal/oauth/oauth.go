// Package oauth implements per-platform sign-in and stream-key retrieval.
//
// polyemesis cannot ship client secrets, so the operator registers their own
// developer app and pastes the client ID/secret into Settings. Everything here
// runs the authorization-code flow against those credentials, stores the
// resulting tokens encrypted (see internal/secrets), and refreshes them
// automatically.
//
// On Kick: its stream key WAS recorded here as unavailable — "confirmed absent
// from Channels, Livestreams and Users" — and that was wrong. There is no
// /streamkey endpoint to find, so reading the endpoint list suggests the
// capability does not exist; the key in fact rides as stream.key on the same
// GET /public/v1/channels response the adapter already fetches, withheld unless
// streamkey:read was granted. Kick.Ingest reads it like any other provider.
//
// ErrNoStreamKeyAPI survives for exactly one case: a token minted before that
// scope was requested, which reads as an empty key and is fixed by reconnecting
// once. Granting a scope never upgrades a token already issued.
//
// The stale claim outlived the fix in three other places — the capability
// matrix, the setup guide, and this comment. guide_drift_test.go now pins the
// guide against the matrix; this paragraph is pinned by nothing, so re-read it
// against kick.go before trusting it.
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
	// ScopeVersion is bumped BY HAND whenever Scopes changes, and is stored
	// alongside the account it was granted under.
	//
	// This exists because an OAuth token carries exactly the scopes it was
	// issued with, and adding a scope to the application does NOT upgrade a
	// token somebody already holds. Without this, an operator who connected
	// before a feature shipped silently lacks permission for it and finds out
	// from a 401 during a broadcast. polyemesis previously handled that with a
	// line in the documentation, twice.
	//
	// A developer-bumped integer rather than a diff of the granted scope
	// strings, and that is deliberate. Diffing means comparing against what
	// the PLATFORM returned, and platforms rename scopes, grant supersets and
	// reorder them -- which produces spurious "reconnect" prompts, and a
	// prompt that cries wolf is a prompt operators learn to dismiss. The
	// version is cruder and always right about the only question that matters:
	// did WE change what we ask for.
	//
	// Keep the constant next to Scopes in each provider. The pairing is the
	// only thing that makes forgetting to bump it visible in review.
	ScopeVersion() int
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
		db.PlatformYouTube:  &YouTube{},
		db.PlatformTwitch:   &Twitch{},
		db.PlatformFacebook: &Facebook{},
		db.PlatformKick:     &Kick{},
	}
}

// Get returns a provider, or an error naming the platform.
func Get(p db.Platform) (Provider, error) {
	if pr, ok := Providers()[p]; ok {
		return pr, nil
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
	// Supported reports whether polyemesis can sign in to this platform at all.
	Supported bool `json:"supported"`
	// ManualStreamKey reports that the account connects, but the key is pasted
	// rather than fetched — the two are separate answers, and Kick is the reason
	// they had to be. Read it from ManualKeyFor rather than hard-coding it.
	ManualStreamKey bool   `json:"manualStreamKey,omitempty"`
	Note            string `json:"note,omitempty"`
}

// Guides returns the setup instructions rendered on the credentials page.
//
// ManualStreamKey is filled in from ManualKeyFor rather than written out per
// entry, so a provider that gains a key endpoint stops advertising the paste
// step the moment it drops the ManualKey interface.
func Guides() []SetupGuide {
	guides := guides()
	for i := range guides {
		if _, manual := ManualKeyFor(guides[i].Platform); manual {
			guides[i].ManualStreamKey = true
		}
	}
	return guides
}

func guides() []SetupGuide {
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
			Platform:     db.PlatformFacebook,
			Name:         "Facebook Live (Meta)",
			ConsoleURL:   "https://developers.facebook.com/apps",
			RedirectPath: "/api/v1/oauth/facebook/callback",
			Supported:    true,
			Scopes:       (&Facebook{}).Scopes(),
			Note: "Read this first: Meta will not let anyone but you use this app until it passes App Review. " +
				"Your own Facebook account works immediately as a developer/tester of the app, which is all a " +
				"self-hosted setup normally needs — but if anyone else is going to connect their account here, " +
				"the app has to be submitted for Advanced Access to publish_video (profiles) or " +
				"pages_manage_posts (Pages) first. Budget days, not minutes, and start it before you need it. " +
				"Facebook also issues a new ingest URL and key for every broadcast, so connecting the account " +
				"creates the broadcast: there is no permanent key to reuse.",
			Steps: []string{
				"Before anything else: decide who will connect accounts. Only people listed under Roles → Roles " +
					"(admins, developers, testers) can use an app in development mode. Anyone else requires App Review, " +
					"which is Meta's process, not something polyemesis can shortcut.",
				"Open the Meta app dashboard and click Create app. Pick the “Other” use case and the “Business” type — " +
					"that is the one that offers Facebook Login and the Live Video permissions.",
				"Add the “Facebook Login” product. Under Facebook Login → Settings, switch on “Client OAuth login” and " +
					"“Web OAuth login”, and add exactly the redirect URI shown below under “Valid OAuth Redirect URIs”.",
				"In App settings → Basic, copy the App ID and App Secret into the fields on this page. The App ID is " +
					"the client ID and the App Secret is the client secret.",
				"Decide the target. Streaming to your own profile needs publish_video. Streaming to a Page needs " +
					"pages_manage_posts and pages_read_engagement, plus pages_show_list so polyemesis can offer you " +
					"the Page to pick. polyemesis asks for all of them; you can decline the ones you do not want on " +
					"the consent screen.",
				"In App Review → Permissions and Features, request Advanced Access for the permissions you need. " +
					"Skip this only while you are the sole person connecting an account.",
				"Go to a destination and click Connect account, then choose your profile or one of your Pages. " +
					"polyemesis creates the broadcast and fills in the RTMPS URL and stream key for you.",
			},
		},
		{
			Platform:     db.PlatformKick,
			Name:         "Kick",
			ConsoleURL:   "https://kick.com/settings/developer",
			RedirectPath: "/api/v1/oauth/kick/callback",
			Supported:    true,
			Scopes:       (&Kick{}).Scopes(),
			Note: "Kick uses OAuth 2.1, so the consent step sends a PKCE challenge automatically — " +
				"there is nothing extra to configure for it. Grant every scope on the consent screen: " +
				"the stream key is withheld unless streamkey:read is among them, and an account " +
				"connected before that scope was requested has to be disconnected and reconnected once " +
				"before the key appears.",
			Steps: []string{
				"Open Kick → Settings → Developer and create an OAuth application.",
				"Set the Redirect URI to exactly the URI shown below.",
				"Copy the Client ID and Client Secret into the fields on this page and save.",
				"Click Connect account. Kick uses OAuth 2.1, so polyemesis sends a PKCE challenge automatically.",
				"Nothing to paste: polyemesis reads the ingest URL and stream key from the channels " +
					"resource over the streamkey:read scope, the same way it does for the other platforms.",
			},
		},
	}
}
