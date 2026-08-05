package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube implements Google OAuth 2.0 plus the Live Streaming API lookup that
// turns a connected account into an ingest URL and stream key.
type YouTube struct {
	// endpoints carries the base URLs; zero value is production. See
	// endpoints.go.
	endpoints
}

// Google splits what other platforms combine: consent is granted on
// accounts.google.com, tokens are minted on oauth2.googleapis.com, and the data
// API is a third host. All three are the authorization/data split endpoints.go
// describes, so WithBaseURL moves all three together.
const (
	ytConsentBase = "https://accounts.google.com"
	ytTokenBase   = "https://oauth2.googleapis.com"
)

// apiEndpoint is the YouTube Data API base for THIS provider. Account and
// Ingest previously wrote https://www.googleapis.com/youtube/v3 inline while
// the rest of the package went through the ytAPIBase var, so a test that
// redirected the var still sent those two calls to Google.
func (y *YouTube) apiEndpoint() string { return y.apiBase(ytAPIBase) }

func (y *YouTube) Platform() db.Platform { return db.PlatformYouTube }

// Scopes: youtube.readonly is not enough, because creating a liveStream (which
// we do when the channel has none) is a write.
func (y *YouTube) Scopes() []string {
	return []string{"https://www.googleapis.com/auth/youtube"}
}

// PKCE is on: Google documents code_challenge/code_challenge_method for web
// server applications, i.e. exactly this flow, and enforces the verifier at the
// token endpoint. It costs nothing and it means a leaked authorization code —
// through a proxy log, a referrer header, a shared machine's history — is
// useless to anyone but this process.
// ScopeVersion 1 is the single youtube scope above. Bump whenever Scopes
// changes; see the Provider interface for why this is a hand-bumped integer
// rather than a diff of what the platform granted.
func (y *YouTube) ScopeVersion() int { return 1 }

func (y *YouTube) PKCE() bool { return true }

func (y *YouTube) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(y.Scopes(), " "))
	q.Set("state", state)
	// access_type=offline is what gets us a refresh token at all; without it
	// the connection dies an hour later and the user has to reconnect.
	q.Set("access_type", "offline")
	// Google only re-issues a refresh token when consent is re-granted, so a
	// user reconnecting an account would otherwise end up with an access token
	// and no way to renew it.
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return y.authBase(ytConsentBase) + "/o/oauth2/v2/auth?" + q.Encode()
}

func (y *YouTube) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return postForm(ctx, y.authBase(ytTokenBase)+"/token", form, nil)
}

func (y *YouTube) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	t, err := postForm(ctx, y.authBase(ytTokenBase)+"/token", url.Values{
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
	}, nil)
	if err != nil {
		return nil, err
	}
	// A refresh response omits refresh_token; carrying the old one forward is
	// what keeps the connection alive indefinitely.
	if t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, nil
}

func (y *YouTube) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := getJSON(ctx,
		y.apiEndpoint()+"/channels?part=snippet&mine=true",
		accessToken, nil, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, fmt.Errorf("this Google account has no YouTube channel; create one and reconnect")
	}
	return &Account{Name: out.Items[0].Snippet.Title, Ref: out.Items[0].ID}, nil
}

type ytLiveStream struct {
	ID      string `json:"id"`
	Snippet struct {
		Title string `json:"title"`
	} `json:"snippet"`
	CDN struct {
		IngestionType string `json:"ingestionType"`
		Resolution    string `json:"resolution"`
		FrameRate     string `json:"frameRate"`
		IngestionInfo struct {
			StreamName          string `json:"streamName"`
			IngestionAddress    string `json:"ingestionAddress"`
			BackupIngestionAddr string `json:"backupIngestionAddress"`
		} `json:"ingestionInfo"`
	} `json:"cdn"`
}

// Ingest returns the channel's reusable RTMP ingest, creating one if the
// channel has never streamed.
//
// This is the payoff of the OAuth flow: the user never sees, copies or
// mistypes a stream key.
func (y *YouTube) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	var list struct {
		Items []ytLiveStream `json:"items"`
	}
	err := getJSON(ctx,
		y.apiEndpoint()+"/liveStreams?part=snippet,cdn&mine=true&maxResults=50",
		accessToken, nil, &list)
	if err != nil {
		return nil, err
	}

	// Prefer an existing reusable RTMP stream; that is the one the creator's
	// scheduled broadcasts are already bound to.
	for _, s := range list.Items {
		if strings.EqualFold(s.CDN.IngestionType, "rtmp") && s.CDN.IngestionInfo.StreamName != "" {
			return &Ingest{
				URL: s.CDN.IngestionInfo.IngestionAddress,
				Key: s.CDN.IngestionInfo.StreamName,
			}, nil
		}
	}

	// None exists: create a reusable variable-resolution stream. variable/variable
	// is what lets polyemesis pass through whatever OBS is sending without
	// YouTube rejecting a resolution mismatch.
	created := ytLiveStream{}
	payload := map[string]any{
		"snippet": map[string]any{"title": "polyemesis"},
		"cdn": map[string]any{
			"ingestionType": "rtmp",
			"resolution":    "variable",
			"frameRate":     "variable",
		},
	}
	err = postJSON(ctx,
		y.apiEndpoint()+"/liveStreams?part=snippet,cdn",
		accessToken, payload, nil, &created)
	if err != nil {
		return nil, fmt.Errorf("could not create a YouTube ingest stream: %w", err)
	}
	if created.CDN.IngestionInfo.StreamName == "" {
		return nil, fmt.Errorf("YouTube created a stream but returned no stream key")
	}
	return &Ingest{
		URL: created.CDN.IngestionInfo.IngestionAddress,
		Key: created.CDN.IngestionInfo.StreamName,
	}, nil
}
