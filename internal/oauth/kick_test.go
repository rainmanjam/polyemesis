package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// kickStub returns a provider pointed at a test server, plus the server.
//
// Both of Kick's base URLs are redirected because a single flow crosses
// id.kick.com and api.kick.com, and a stray real request would make the suite
// depend on the network -- WithBaseURL moves both at once, which is why it does
// not offer a way to move only one. It used to assign the two package vars and
// restore them in a t.Cleanup; the provider it returns now is the only one a
// caller should use.
func kickStub(t *testing.T, h http.HandlerFunc) (*Kick, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewKick(WithBaseURL(srv.URL)), srv
}

// --------------------------------------------------------- the missing key

func TestKickIngestNeverFabricatesAnIngestURL(t *testing.T) {
	// The original version of this test asserted Ingest could never succeed,
	// because Kick was believed to publish no stream key at all. That premise
	// was wrong -- the key is stream.key on the channels resource, behind
	// streamkey:read -- so the assertion has been replaced.
	//
	// What is KEPT is the concern the original was really protecting, and it
	// nearly bit during this very change: a first draft of Ingest defaulted the
	// missing-URL case to a hardcoded RTMPS host copied from memory. Kick fronts
	// its ingest with a CDN whose host has changed, so a stale constant would
	// publish to nowhere and read as a polyemesis bug. Never invent the URL:
	// return what the API returned, or fail saying so.
	//
	// Pointed at a stub that refuses the token. It previously ran against a bare
	// &Kick{}, i.e. the real api.kick.com, and "err != nil" was satisfied just as
	// well by having no network as by Kick's refusal.
	k, _ := kickStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"invalid token"}`)
	})
	ing, err := k.Ingest(context.Background(), "cid", "not-a-real-token")
	if err == nil {
		t.Fatal("Ingest succeeded with a bogus token; the caller would store an empty key")
	}
	if ing != nil {
		t.Fatalf("Ingest returned %#v alongside an error", ing)
	}

	msg := strings.ToLower(err.Error())
	// The error may legitimately name the dashboard path in prose, but it must
	// never contain something an operator could mistake for a real ingest URL.
	for _, forbidden := range []string{"://", "rtmp"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("Ingest error contains %q, which reads as a fabricated ingest endpoint: %s",
				forbidden, err)
		}
	}
}

// The positive case. Without it the suite above passes just as happily against
// an Ingest that refuses everything, which is exactly what it used to do.
func TestKickIngestReadsTheKeyOffTheChannelsResource(t *testing.T) {
	var gotPath string
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"data":[{"broadcaster_user_id":7,"slug":"chan",
			"stream":{"is_live":true,"url":"rtmps://ingest.example/app/","key":"sk_live_abc"}}]}`)
	})

	ing, err := k.Ingest(context.Background(), "cid", "token")
	if err != nil {
		t.Fatalf("Ingest failed against a channels response carrying a key: %v", err)
	}
	// The key must come from the resource we already fetch, not an invented
	// /streamkey path -- that was the false premise this whole change corrected.
	if gotPath != "/public/v1/channels" {
		t.Errorf("Ingest called %q, want /public/v1/channels", gotPath)
	}
	if ing.Key != "sk_live_abc" {
		t.Errorf("Key = %q, want the value from stream.key", ing.Key)
	}
	if ing.URL != "rtmps://ingest.example/app/" {
		t.Errorf("URL = %q, want the value from stream.url verbatim", ing.URL)
	}
}

// A key with no URL must fail rather than guess. See the comment on
// TestKickIngestNeverFabricatesAnIngestURL: a hardcoded host was nearly shipped.
func TestKickIngestRefusesToInventAURLWhenOnlyTheKeyArrives(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"broadcaster_user_id":7,"slug":"chan",
			"stream":{"is_live":true,"key":"sk_live_abc"}}]}`)
	})
	ing, err := k.Ingest(context.Background(), "cid", "token")
	if err == nil {
		t.Fatalf("Ingest returned %#v for a response with no stream.url; it must not "+
			"substitute a host of its own", ing)
	}
	if !strings.Contains(err.Error(), "no ingest URL") {
		t.Errorf("the error does not say the URL was missing: %v", err)
	}
}

// A token minted before streamkey:read gets neither field, and must be told to
// reconnect -- retrying forever is the failure mode this guards.
func TestKickIngestTellsAStaleTokenToReconnect(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"broadcaster_user_id":7,"slug":"chan",
			"stream":{"is_live":true}}]}`)
	})
	_, err := k.Ingest(context.Background(), "cid", "token")
	if !errors.Is(err, ErrNoStreamKeyAPI) {
		t.Fatalf("a response with no key must wrap ErrNoStreamKeyAPI so callers can "+
			"distinguish 'reconnect' from 'retry': %v", err)
	}
	if !strings.Contains(err.Error(), "connect it again") {
		t.Errorf("the error does not tell the operator to reconnect: %v", err)
	}
}

func TestKickAdvertisesTheManualKeyCapabilitySoTheUICanSayItUpFront(t *testing.T) {
	var k any = &Kick{}
	mk, ok := k.(ManualKey)
	if !ok {
		t.Fatal("Kick does not implement ManualKey, so the UI can only learn about the missing key by failing")
	}
	if strings.TrimSpace(mk.ManualKeyReason()) == "" {
		t.Fatal("ManualKeyReason is empty; the stream-key field would carry no explanation")
	}
}

func TestManualKeyForOnlyFlagsPlatformsThatAuthenticateButCannotFetchAKey(t *testing.T) {
	tests := []struct {
		name     string
		platform db.Platform
		want     bool
	}{
		{"youtube fetches its own key", db.PlatformYouTube, false},
		{"twitch fetches its own key", db.PlatformTwitch, false},
		{"an unknown platform needs no hint", db.Platform("mystery"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ManualKeyFor(tc.platform); ok != tc.want {
				t.Fatalf("ManualKeyFor(%q) = %v, want %v", tc.platform, ok, tc.want)
			}
		})
	}

	// Kick still carries manual-key advice, but it no longer means "this
	// platform can never fetch a key" -- Kick can, over streamkey:read. It now
	// covers the one case left: a token minted before that scope was requested,
	// which no amount of retrying fixes because granting a scope does not
	// upgrade a token already issued.
	t.Run("kick keeps reconnect advice for a token that predates the scope", func(t *testing.T) {
		if _, err := Get(db.PlatformKick); err != nil {
			t.Skip("Kick is not in Providers() yet")
		}
		mk, ok := ManualKeyFor(db.PlatformKick)
		if !ok {
			t.Fatal("Kick lost its manual-key advice; an operator whose token predates " +
				"streamkey:read would get a bare failure with nothing to act on")
		}
		if !strings.Contains(mk.ManualKeyReason(), "connect it again") {
			t.Errorf("the advice does not tell the operator to reconnect, which is the "+
				"only thing that fixes it: %q", mk.ManualKeyReason())
		}
	})
}

func TestGetKickResolvesToAProviderRatherThanAnUnsupportedPlatform(t *testing.T) {
	p, err := Get(db.PlatformKick)
	if err != nil {
		t.Skip("Kick is not in Providers() yet; apply the one-line registration from the Kick provider report")
	}
	if p.Platform() != db.PlatformKick {
		t.Fatalf("Get(kick) returned the %q provider", p.Platform())
	}
	// Ingest used to return ErrNoStreamKeyAPI unconditionally, on the belief
	// that Kick published no stream key at all. It does: stream.key on the
	// channels resource, behind streamkey:read. So the contract under test is
	// now that Ingest actually TRIES -- a bogus token must produce a transport
	// or auth failure, which proves a request was made, rather than the old
	// short-circuit that never touched the network.
	_, err = p.Ingest(context.Background(), "cid", "not-a-real-token")
	if err == nil {
		t.Fatal("Ingest succeeded with a bogus token")
	}
	if errors.Is(err, ErrNoStreamKeyAPI) {
		t.Fatalf("Ingest still short-circuits with ErrNoStreamKeyAPI instead of "+
			"calling the channels resource; the stream key IS fetchable over "+
			"streamkey:read: %v", err)
	}
}

// ------------------------------------------------------------------- oauth

func TestKickAuthURLCarriesPKCEAndEveryRequestedScope(t *testing.T) {
	k := &Kick{}
	if !k.PKCE() {
		t.Fatal("PKCE() is false; Kick speaks OAuth 2.1, which refuses an authorization code without a verifier")
	}

	tests := []struct {
		name          string
		challenge     string
		wantChallenge bool
	}{
		{"a challenge is forwarded with S256", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", true},
		{"no challenge sends no empty parameter", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := k.AuthURL("client-1", "https://polyemesis.test/api/v1/oauth/kick/callback", "state-1", tc.challenge)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("AuthURL is unparseable %q: %v", raw, err)
			}
			if u.Host != "id.kick.com" && !strings.HasPrefix(raw, kickIDBase) {
				t.Fatalf("AuthURL host = %q, want Kick's authorization server", u.Host)
			}
			if u.Path != "/oauth/authorize" {
				t.Fatalf("AuthURL path = %q, want /oauth/authorize", u.Path)
			}

			q := u.Query()
			if got := q.Get("response_type"); got != "code" {
				t.Errorf("response_type = %q, want code", got)
			}
			if got := q.Get("state"); got != "state-1" {
				t.Errorf("state = %q, want state-1", got)
			}
			if got := q.Get("client_id"); got != "client-1" {
				t.Errorf("client_id = %q, want client-1", got)
			}
			if !tc.wantChallenge {
				if q.Has("code_challenge") || q.Has("code_challenge_method") {
					t.Fatalf("an empty challenge still produced PKCE parameters: %s", u.RawQuery)
				}
				return
			}
			if got := q.Get("code_challenge"); got != tc.challenge {
				t.Errorf("code_challenge = %q, want %q", got, tc.challenge)
			}
			if got := q.Get("code_challenge_method"); got != "S256" {
				t.Errorf("code_challenge_method = %q, want S256", got)
			}
		})
	}

	scope := ""
	if u, err := url.Parse(k.AuthURL("c", "r", "s", "")); err == nil {
		scope = u.Query().Get("scope")
	}
	granted := strings.Fields(scope)
	for _, want := range k.Scopes() {
		found := false
		for _, g := range granted {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("scope %q is declared but not requested at authorization time: %q", want, scope)
		}
	}
	// This used to assert the OPPOSITE: that moderation:ban was absent, because
	// nothing here banned a viewer and asking for the power to do so made the
	// consent screen scarier than the feature warranted.
	//
	// The maintainer reversed that decision and banning is implemented, so the
	// assertion is inverted rather than deleted. The property worth guarding is
	// unchanged and is the reason this block exists at all: the scopes asked for
	// on the consent screen must match what the code actually does. A scope
	// requested and unused makes an operator grant power nobody exercises; a
	// scope used and unrequested is a 401 they cannot fix without reconnecting.
	var hasBan bool
	for _, g := range granted {
		if g == "moderation:ban" {
			hasBan = true
		}
	}
	if !hasBan {
		t.Error("moderation:ban is not requested, but KickAdapter.Ban exists and will 401 without it")
	}
	// The other half of the rule -- that the implementation is really there --
	// is asserted in internal/chat, where the interface lives. Reaching for it
	// from here would make the OAuth package depend on the chat package for a
	// compile-time check.
}

func TestKickTokenRequestsSendTheGrantKickExpects(t *testing.T) {
	tests := []struct {
		name     string
		call     func(k *Kick) (*Token, error)
		wantForm map[string]string
		absent   []string
	}{
		{
			name: "authorization code exchange carries the verifier",
			call: func(k *Kick) (*Token, error) {
				return k.Exchange(context.Background(), "cid", "secret", "https://p.test/cb", "the-code", "the-verifier")
			},
			wantForm: map[string]string{
				"grant_type":    "authorization_code",
				"code":          "the-code",
				"code_verifier": "the-verifier",
				"redirect_uri":  "https://p.test/cb",
				"client_id":     "cid",
				"client_secret": "secret",
			},
		},
		{
			name: "an exchange with no stored verifier omits the parameter",
			call: func(k *Kick) (*Token, error) {
				return k.Exchange(context.Background(), "cid", "secret", "https://p.test/cb", "the-code", "")
			},
			wantForm: map[string]string{"grant_type": "authorization_code"},
			absent:   []string{"code_verifier"},
		},
		{
			name: "refresh sends the refresh grant",
			call: func(k *Kick) (*Token, error) {
				return k.Refresh(context.Background(), "cid", "secret", "old-refresh")
			},
			wantForm: map[string]string{
				"grant_type":    "refresh_token",
				"refresh_token": "old-refresh",
			},
			absent: []string{"code"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got url.Values
			var path string
			k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				raw, _ := io.ReadAll(r.Body)
				got, _ = url.ParseQuery(string(raw))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"scope":"user:read"}`))
			})

			if _, err := tc.call(k); err != nil {
				t.Fatalf("token request: %v", err)
			}
			if path != "/oauth/token" {
				t.Fatalf("token path = %q, want /oauth/token", path)
			}
			for key, want := range tc.wantForm {
				if g := got.Get(key); g != want {
					t.Errorf("form[%q] = %q, want %q", key, g, want)
				}
			}
			for _, key := range tc.absent {
				if got.Has(key) {
					t.Errorf("form carried %q = %q, which Kick did not ask for", key, got.Get(key))
				}
			}
		})
	}
}

func TestKickRefreshKeepsTheOldRefreshTokenWhenTheResponseOmitsOne(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
	})
	tok, err := k.Refresh(context.Background(), "cid", "secret", "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "old-refresh" {
		t.Fatalf("RefreshToken = %q, want the old one carried forward; the account would die at the next expiry",
			tok.RefreshToken)
	}
}

// ----------------------------------------------------------------- account

func TestKickAccountReadsTheCurrentUsersChannelWithNoParameters(t *testing.T) {
	var log []capture
	var auth string
	k, _ := kickStub(t, recordAll(t, &log, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"broadcaster_user_id":4242,"slug":"nightowl","stream_title":"late"}]}`))
	}))

	acct, err := k.Account(context.Background(), "cid", "the-token")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.Name != "nightowl" || acct.Ref != "4242" {
		t.Fatalf("Account = %#v, want the slug and broadcaster id", acct)
	}
	c := find(log, http.MethodGet, "/public/v1/channels")
	if c == nil {
		t.Fatalf("no GET /public/v1/channels was made: %#v", log)
	}
	// No parameters is what makes this the token's own channel; a broadcaster
	// id here would read someone else's.
	if c.Query != "" {
		t.Errorf("channels query = %q, want none", c.Query)
	}
	if auth != "Bearer the-token" {
		t.Errorf("Authorization = %q, want a bearer token", auth)
	}
}

func TestKickAccountSaysWhichScopeIsMissingWhenNoChannelComesBack(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	_, err := k.Account(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("an empty channel list was accepted; the account would save with no name")
	}
	if !strings.Contains(err.Error(), "channel:read") {
		t.Errorf("error does not name the scope that fixes it: %v", err)
	}
}

// -------------------------------------------------------------- categories

func TestKickCategorySearchQueriesTheDirectory(t *testing.T) {
	var log []capture
	k, _ := kickStub(t, recordAll(t, &log, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":15,"name":"Just Chatting"},{"id":28,"name":"Just Dance"}]}`))
	}))

	got, err := k.SearchCategories(context.Background(), "cid", "tok", "just ch")
	if err != nil {
		t.Fatalf("SearchCategories: %v", err)
	}
	if len(got) != 2 || got[0].ID != 15 || got[0].Name != "Just Chatting" {
		t.Fatalf("categories = %#v", got)
	}
	c := find(log, http.MethodGet, "/public/v1/categories")
	if c == nil {
		t.Fatalf("no category search was made: %#v", log)
	}
	if q, _ := url.ParseQuery(c.Query); q.Get("q") != "just ch" {
		t.Errorf("category query = %q, want the search term", c.Query)
	}
}

// --------------------------------------------------------------- metadata

func TestKickPushMetadataWritesWhatKickAcceptsAndWarnsAboutTheRest(t *testing.T) {
	tests := []struct {
		name         string
		meta         Metadata
		categories   string
		wantBody     map[string]any
		wantApplied  []MetadataField
		wantSkipped  []MetadataField
		wantCategory string
		wantWarning  string
		wantNoWrite  bool
	}{
		{
			name:        "a title alone is written alone",
			meta:        Metadata{Title: "Late night build"},
			wantBody:    map[string]any{"stream_title": "Late night build"},
			wantApplied: []MetadataField{FieldTitle},
		},
		{
			name:         "a category name is resolved to its numeric id",
			meta:         Metadata{Title: "t", Category: "just chatting"},
			categories:   `{"data":[{"id":15,"name":"Just Chatting"}]}`,
			wantBody:     map[string]any{"stream_title": "t", "category_id": float64(15)},
			wantApplied:  []MetadataField{FieldTitle, FieldCategory},
			wantCategory: "Just Chatting",
		},
		{
			name: "a numeric category is taken at its word without a lookup",
			// The escape hatch: an operator who knows the id is not blocked by
			// a directory search that cannot match their category.
			meta:        Metadata{Category: "753"},
			categories:  `{"data":[]}`,
			wantBody:    map[string]any{"category_id": float64(753)},
			wantApplied: []MetadataField{FieldCategory},
		},
		{
			name:        "an unknown category costs a warning, not the title",
			meta:        Metadata{Title: "t", Category: "Underwater Basketweaving"},
			categories:  `{"data":[{"id":15,"name":"Just Chatting"}]}`,
			wantBody:    map[string]any{"stream_title": "t"},
			wantApplied: []MetadataField{FieldTitle},
			wantSkipped: []MetadataField{FieldCategory},
			wantWarning: "Did you mean",
		},
		{
			name:        "a description is reported as skipped rather than failing the push",
			meta:        Metadata{Title: "t", Description: "a paragraph"},
			wantBody:    map[string]any{"stream_title": "t"},
			wantApplied: []MetadataField{FieldTitle},
			wantSkipped: []MetadataField{FieldDescription},
			wantWarning: "no description",
		},
		{
			name:        "nothing writable means no request at all",
			meta:        Metadata{Description: "only a description"},
			wantSkipped: []MetadataField{FieldDescription},
			wantNoWrite: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			k, _ := kickStub(t, recordAll(t, &log, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/public/v1/categories") {
					body := tc.categories
					if body == "" {
						body = `{"data":[]}`
					}
					_, _ = w.Write([]byte(body))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))

			res, err := k.PushMetadata(context.Background(), "cid", "tok", "4242", tc.meta)
			if err != nil {
				t.Fatalf("PushMetadata: %v", err)
			}

			patch := find(log, http.MethodPatch, "/public/v1/channels")
			if tc.wantNoWrite {
				if patch != nil {
					t.Fatalf("a write was made with nothing writable: %#v", patch.Body)
				}
			} else {
				if patch == nil {
					t.Fatalf("no PATCH /public/v1/channels: %#v", log)
				}
				if len(patch.Body) != len(tc.wantBody) {
					t.Errorf("body = %#v, want exactly %#v", patch.Body, tc.wantBody)
				}
				for k, want := range tc.wantBody {
					if got := patch.Body[k]; got != want {
						t.Errorf("body[%q] = %#v, want %#v", k, got, want)
					}
				}
			}

			assertFields(t, "applied", res.Applied, tc.wantApplied)
			assertFields(t, "skipped", res.Skipped, tc.wantSkipped)
			if tc.wantCategory != "" && res.Category != tc.wantCategory {
				t.Errorf("category = %q, want %q", res.Category, tc.wantCategory)
			}
			if tc.wantWarning != "" && !strings.Contains(strings.Join(res.Warnings, " | "), tc.wantWarning) {
				t.Errorf("warnings %v do not mention %q", res.Warnings, tc.wantWarning)
			}
		})
	}
}

func assertFields(t *testing.T, label string, got, want []MetadataField) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestKickMetadataScopeFailureNamesTheReconnect(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})
	_, err := k.PushMetadata(context.Background(), "cid", "tok", "4242", Metadata{Title: "t"})
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !strings.Contains(err.Error(), "channel:write") {
		t.Errorf("error does not name the scope that fixes it: %v", err)
	}
}

func TestKickUpdateChannelOmitsFieldsTheCallerDidNotSet(t *testing.T) {
	// A PATCH that sent zero values would blank a live title or reset the
	// category to nothing — the one failure mode worse than not writing at all.
	var log []capture
	k, _ := kickStub(t, recordAll(t, &log, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	err := k.UpdateChannel(context.Background(), "tok", KickChannelUpdate{
		CustomTags: []string{"speedrun", "no-commentary"},
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	patch := find(log, http.MethodPatch, "/public/v1/channels")
	if patch == nil {
		t.Fatalf("no PATCH was made: %#v", log)
	}
	if _, ok := patch.Body["stream_title"]; ok {
		t.Errorf("an unset title was sent, which would blank the live title: %#v", patch.Body)
	}
	if _, ok := patch.Body["category_id"]; ok {
		t.Errorf("an unset category was sent: %#v", patch.Body)
	}
	tags, ok := patch.Body["custom_tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Errorf("custom_tags = %#v, want the two tags given", patch.Body["custom_tags"])
	}
}

func TestKickMetadataCapsDescribeWhatKickActuallyAccepts(t *testing.T) {
	caps := (&Kick{}).MetadataCaps()
	tests := []struct {
		name  string
		field MetadataField
		want  bool
	}{
		{"kick takes a title", FieldTitle, true},
		{"kick takes a category", FieldCategory, true},
		{"kick has no description on a broadcast", FieldDescription, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := caps.Accepts(tc.field); got != tc.want {
				t.Fatalf("Accepts(%q) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
	if caps.Scope != "channel:write" {
		t.Errorf("Scope = %q, want channel:write", caps.Scope)
	}
	// Kick publishes no title limit; inventing one here would truncate a title
	// Kick would have accepted.
	if caps.TitleMax != 0 {
		t.Errorf("TitleMax = %d, want 0 until Kick publishes a limit", caps.TitleMax)
	}
}

// ------------------------------------------------------------------- stats

func TestKickStatsReadsTheLiveChannelAndFallsBackWhenAnEndpointIsUnavailable(t *testing.T) {
	const live = `{"data":[{"slug":"nightowl","stream_title":"late","language":"en",` +
		`"started_at":"2026-07-27T20:00:00Z","viewer_count":417,"category":{"id":15,"name":"Just Chatting"}}]}`

	tests := []struct {
		name        string
		users       func(w http.ResponseWriter)
		stats       func(w http.ResponseWriter)
		wantLive    bool
		wantViewers int
		wantTitle   string
		wantSource  string
		wantErr     bool
	}{
		{
			name:        "the user livestream carries the whole picture",
			users:       func(w http.ResponseWriter) { _, _ = w.Write([]byte(live)) },
			stats:       func(w http.ResponseWriter) { t.Error("the stats endpoint was called needlessly") },
			wantLive:    true,
			wantViewers: 417,
			wantTitle:   "late",
			wantSource:  kickUserLivestreamsPath,
		},
		{
			name:  "an offline channel is an answer, not an error",
			users: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[]}`)) },
			stats: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":{"viewer_count":0}}`)) },
		},
		{
			name:  "a single stats object is accepted as readily as a list",
			users: func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[]}`)) },
			stats: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"data":{"viewer_count":88}}`))
			},
			wantLive:    true,
			wantViewers: 88,
			wantSource:  kickLivestreamStatsPath,
		},
		{
			name:        "a scope-refused user list still yields the aggregate count",
			users:       func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
			stats:       func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[{"viewers":12}]}`)) },
			wantLive:    true,
			wantViewers: 12,
			wantSource:  kickLivestreamStatsPath,
		},
		{
			name:    "both endpoints failing is a real error",
			users:   func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
			stats:   func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case kickUserLivestreamsPath:
					tc.users(w)
				case kickLivestreamStatsPath:
					tc.stats(w)
				default:
					t.Errorf("unexpected request to %s", r.URL.Path)
				}
			})

			got, err := k.Stats(context.Background(), "cid", "tok")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("both endpoints failed but Stats returned %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stats: %v", err)
			}
			if got.Live != tc.wantLive {
				t.Errorf("Live = %v, want %v", got.Live, tc.wantLive)
			}
			if got.ViewerCount != tc.wantViewers {
				t.Errorf("ViewerCount = %d, want %d", got.ViewerCount, tc.wantViewers)
			}
			if tc.wantTitle != "" && got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
			if tc.wantSource != "" && got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

func TestKickStatsParsesTheStartTimeAndSurvivesOneItCannotRead(t *testing.T) {
	tests := []struct {
		name      string
		startedAt string
		wantZero  bool
	}{
		{"rfc 3339", "2026-07-27T20:00:00Z", false},
		{"kick's spaced form", "2026-07-27 20:00:00", false},
		{"an unreadable stamp costs the timestamp, not the read", "last tuesday", true},
		{"an absent stamp is not an error", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == kickUserLivestreamsPath {
					_, _ = w.Write([]byte(`{"data":[{"viewer_count":3,"started_at":"` + tc.startedAt + `"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":[]}`))
			})
			got, err := k.Stats(context.Background(), "cid", "tok")
			if err != nil {
				t.Fatalf("Stats: %v", err)
			}
			if got.StartedAt.IsZero() != tc.wantZero {
				t.Fatalf("StartedAt = %v (zero=%v), want zero=%v", got.StartedAt, got.StartedAt.IsZero(), tc.wantZero)
			}
			if got.ViewerCount != 3 {
				t.Errorf("ViewerCount = %d, want the read to survive the timestamp", got.ViewerCount)
			}
		})
	}
}

func TestGuidesSeparateSigningInFromFetchingAKey(t *testing.T) {
	// Supported used to mean both at once, which was fine until Kick, where
	// connecting works and the key is pasted. A guide that answers only one of
	// the two questions sends the operator looking for a Fetch key button that
	// can never work.
	tests := []struct {
		name            string
		platform        db.Platform
		supported       bool
		manualStreamKey bool
	}{
		{"youtube signs in and fetches", db.PlatformYouTube, true, false},
		{"twitch signs in and fetches", db.PlatformTwitch, true, false},
		{"facebook signs in and creates the broadcast", db.PlatformFacebook, true, false},
		{"kick signs in but the key is pasted", db.PlatformKick, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var guide *SetupGuide
			for i, g := range Guides() {
				if g.Platform == tc.platform {
					guide = &Guides()[i]
					break
				}
			}
			if guide == nil {
				t.Fatalf("no setup guide for %s, so the credentials page cannot render it", tc.platform)
			}
			if guide.Supported != tc.supported {
				t.Errorf("Supported = %v, want %v", guide.Supported, tc.supported)
			}
			if guide.ManualStreamKey != tc.manualStreamKey {
				t.Errorf("ManualStreamKey = %v, want %v", guide.ManualStreamKey, tc.manualStreamKey)
			}
			// Derived from ManualKeyFor rather than written out per entry, so
			// the two can never drift.
			_, manual := ManualKeyFor(tc.platform)
			if manual != guide.ManualStreamKey {
				t.Errorf("guide says ManualStreamKey=%v but ManualKeyFor says %v", guide.ManualStreamKey, manual)
			}
		})
	}
}

func TestEveryRegisteredProviderHasASetupGuide(t *testing.T) {
	// A provider nobody can configure is a Connect button with no credentials
	// form behind it.
	guides := map[db.Platform]bool{}
	for _, g := range Guides() {
		guides[g.Platform] = true
	}
	for platform := range Providers() {
		if !guides[platform] {
			t.Errorf("provider %q has no setup guide", platform)
		}
	}
}

// ------------------------------------------------------- broadcast settings

// Kick's whole metadata surface is three fields, and tags is the one it shares
// with YouTube. Everything else on BroadcastSettings has no Kick equivalent and
// must be REPORTED rather than silently ignored: an operator who set a DVR
// toggle and saw nothing happen deserves to know the platform has no such
// thing.
func TestKickTakesTagsAndSaysWhatItCannotDo(t *testing.T) {
	var body map[string]any
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/public/v1/channels" {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	tags := []string{"speedrun", "chill"}
	dvr := false
	when := "2026-08-01T20:00:00Z"
	res, err := k.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{Tags: &tags, EnableDvr: &dvr, ScheduledStart: &when})
	if err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}

	got, _ := body["custom_tags"].([]any)
	if len(got) != 2 || got[0] != "speedrun" {
		t.Errorf("custom_tags = %v, want the two requested", body["custom_tags"])
	}
	// The title and category must NOT be sent. This call is about tags, and a
	// stream_title key here would blank the operator's title.
	if _, ok := body["stream_title"]; ok {
		t.Errorf("a tags-only write sent a stream_title: %v", body)
	}
	if _, ok := body["category_id"]; ok {
		t.Errorf("a tags-only write sent a category_id: %v", body)
	}

	if !containsField(res.Applied, FieldTags) {
		t.Errorf("tags were written but not reported as applied: %v", res.Applied)
	}
	for _, want := range []MetadataField{FieldScheduledStart, FieldContentDetails} {
		if !containsField(res.Skipped, want) {
			t.Errorf("%s has no Kick equivalent and was not reported as skipped: %v", want, res.Skipped)
		}
	}
	if len(res.Warnings) < 2 {
		t.Errorf("skipped fields carried no explanation: %v", res.Warnings)
	}
}

// Clearing every tag is a real edit, and it is the case an empty-versus-nil
// slip hides: a nil slice means "not mentioned" and must send nothing, while an
// EMPTY one means "remove them all" and must send [].
func TestKickCanClearEveryTag(t *testing.T) {
	var body map[string]any
	var patched bool
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/public/v1/channels" {
			patched = true
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	empty := []string{}
	if _, err := k.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{Tags: &empty}); err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}
	if !patched {
		t.Fatal("clearing the tags sent no request at all, so they can never be removed")
	}
	got, ok := body["custom_tags"].([]any)
	if !ok || len(got) != 0 {
		t.Errorf("custom_tags = %v, want an empty list", body["custom_tags"])
	}
}

// A block with nothing Kick handles must not write at all.
func TestKickWritesNothingWhenThereAreNoTags(t *testing.T) {
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			t.Errorf("a settings block with no tags still wrote to %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	dvr := true
	res, err := k.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{EnableDvr: &dvr})
	if err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Errorf("nothing was written but %v was reported as applied", res.Applied)
	}
}

func containsField(list []MetadataField, want MetadataField) bool {
	for _, f := range list {
		if f == want {
			return true
		}
	}
	return false
}
