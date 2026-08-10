package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The handler layer for broadcast settings and the reconnect verdict.
//
// internal/oauth covers the write shapes and the verdict logic; this covers the
// three branches ON TOP of them, which nothing did: whether the endpoint
// distinguishes "this platform has no broadcast resource" from "this account
// could not be read", whether the account list actually SURFACES the verdict,
// and whether a platform without broadcast settings reports them as skipped
// rather than swallowing them.

// stampAccount connects an account with an explicit scope version and grant, so
// a test can produce the "connected before the scopes changed" state without an
// OAuth round trip.
func stampAccount(t *testing.T, store *db.DB, box *secrets.Box, p db.Platform, name string, ver int, scopes string) int64 {
	t.Helper()
	a, err := store.UpsertPlatformAccount(box, &db.PlatformAccount{
		Platform:    p,
		AccountName: name,
		AccountRef:  name + "-ref",
		AccessToken: "at",
		Scopes:      scopes,
		ScopeVer:    ver,
	})
	if err != nil {
		t.Fatalf("connect %s: %v", p, err)
	}
	return a.ID
}

// The account list must SURFACE the verdict, not merely compute it.
//
// oauth.AccountNeedsReconnect is well covered on its own. Nothing asserted that
// the handler puts its answer in the response, which is the only reason the
// badge can appear at all.
func TestAccountListSurfacesTheReconnectVerdict(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	tw := &oauth.Twitch{}
	// Current: stamped with the version this build asks for.
	stampAccount(t, store, s.box, db.PlatformTwitch, "current", tw.ScopeVersion(), "")
	// Stale: a legacy row whose grant is missing a scope we now request.
	all := tw.Scopes()
	// Was a t.Skip, and the same self-silencing shape as the two in
	// internal/oauth: it fires when the scope list -- the thing this fixture is
	// built out of -- has shrunk, which is a real change somebody should read.
	// The list is pinned in internal/oauth/testdata/provider-scopes.json now,
	// so drift lands as a golden diff and this can simply insist.
	if len(all) < 2 {
		t.Fatalf("Twitch requests %d scope(s), and this case needs at least two so the "+
			"stale account can be missing exactly one. See "+
			"internal/oauth/testdata/provider-scopes.json.", len(all))
	}
	stampAccount(t, store, s.box, db.PlatformTwitch, "stale", 0, joinScopes(all[1:]))

	var got []struct {
		AccountName string                `json:"accountName"`
		ScopeVer    int                   `json:"scopeVer"`
		Reconnect   oauth.ReconnectReason `json:"reconnect"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts", nil, http.StatusOK), &got)
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2", len(got))
	}

	byName := map[string]bool{}
	for _, a := range got {
		byName[a.AccountName] = a.Reconnect.Needed
		if a.AccountName == "stale" {
			if !a.Reconnect.Needed {
				t.Error("an account missing a scope we now request was not flagged")
			}
			if len(a.Reconnect.Missing) == 0 {
				t.Error("the verdict named no missing scope, so the UI cannot say what changed")
			}
		}
	}
	if byName["current"] {
		t.Error("an account stamped with the current version was told to reconnect")
	}
	if !byName["stale"] {
		t.Error("the stale account's verdict did not reach the response")
	}
}

// The window endpoint must tell "no broadcast resource" apart from "could not
// read this account". They look identical in a UI that only checks for an
// error, and the first is not a fault.
func TestBroadcastWindowSeparatesUnsupportedFromFailed(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	// Twitch has no broadcast resource at all.
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	// A YouTube account whose token is already expired, with no developer
	// credentials configured. tokenFor fails before any network call, which is
	// what keeps this test offline while still exercising the error branch.
	if _, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformYouTube, AccountName: "chan", AccountRef: "chan-ref",
		AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Accounts []struct {
			Platform    db.Platform `json:"platform"`
			AccountName string      `json:"accountName"`
			Supported   bool        `json:"supported"`
			Error       string      `json:"error"`
			Window      *struct {
				LifeCycleStatus string `json:"lifeCycleStatus"`
			} `json:"window"`
		} `json:"accounts"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/metadata/broadcast-window", nil, http.StatusOK), &got)
	if len(got.Accounts) != 2 {
		t.Fatalf("got %d rows, want one per account", len(got.Accounts))
	}

	for _, row := range got.Accounts {
		switch row.Platform {
		case db.PlatformTwitch:
			// Not an error. Twitch simply has no DVR toggle to lock.
			if row.Supported {
				t.Error("Twitch was reported as supporting broadcast settings")
			}
			if row.Error != "" {
				t.Errorf("an unsupported platform reported an error: %q", row.Error)
			}
			if row.Window != nil {
				t.Error("an unsupported platform returned a window")
			}
		case db.PlatformYouTube:
			// Supported, but this account could not be read. The distinction
			// is the point: the controls stay enabled, and the write's own
			// refusal remains the authority.
			if !row.Supported {
				t.Error("YouTube was reported as not supporting broadcast settings")
			}
			if row.Error == "" {
				t.Error("an unreadable account reported no error, so a failure looks like success")
			}
			if row.Window != nil {
				t.Error("a failed read still returned a window")
			}
		}
	}
}

// An operator who sets a DVR toggle on a platform that has none must be TOLD,
// not silently ignored.
func TestABroadcastOnlyPushReportsWhatThePlatformCannotDo(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	// Credentials must exist or pushOne fails before it reaches the broadcast
	// branch, and the test would pass for the wrong reason.
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")

	job := pushAndSettle(t, h, sign, map[string]any{
		"broadcast": map[string]any{"enableDvr": false, "scheduledStart": "2026-08-01T20:00:00Z"},
	})
	if len(job.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(job.Results))
	}
	skipped := map[oauth.MetadataField]bool{}
	for _, f := range job.Results[0].Skipped {
		skipped[f] = true
	}
	for _, want := range []oauth.MetadataField{
		oauth.FieldScheduledStart, oauth.FieldContentDetails, oauth.FieldTags,
	} {
		if !skipped[want] {
			t.Errorf("%s has no Twitch equivalent and was not reported as skipped; "+
				"got %v", want, job.Results[0].Skipped)
		}
	}
}

// joinScopes is the space-separated form platforms hand back.
func joinScopes(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}
