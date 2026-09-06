package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The disconnect guard (hazard 3) and the token compare-and-swap (hazard 5).
//
// Both are motion-step hazards: the steps are individually fine and the ORDER
// they can interleave in is the defect. The tests below are therefore about
// sequences rather than about values -- what a delete does to rows that already
// exist, and what a refresh does to a row somebody else wrote while it was away.

// ---------------------------------------------------------------- disconnect

// deleteAccount sends DELETE /platforms/accounts/{id} with the given body, or
// with no body at all when body is nil -- which is what every existing caller
// of this route sends and is the case the guard has to catch.
func deleteAccount(t *testing.T, h http.Handler, sign func(*http.Request), id int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, http.MethodDelete, "/api/v1/platforms/accounts/"+strconv.FormatInt(id, 10), body)
	sign(r)
	return do(t, h, r)
}

// accountDeleteBody is the shape both outcomes share, so one decoder reads the
// 409 and the 200.
type accountDeleteBody struct {
	Status       string               `json:"status"`
	Error        string               `json:"error"`
	Code         string               `json:"code"`
	Warnings     []string             `json:"warnings"`
	Destinations []accountDestination `json:"destinations"`
}

func decodeAccountDelete(t *testing.T, w *httptest.ResponseRecorder) accountDeleteBody {
	t.Helper()
	var got accountDeleteBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return got
}

func accountIsStillConnected(t *testing.T, store *db.DB, box *secrets.Box, id int64) bool {
	t.Helper()
	_, err := store.GetPlatformAccount(box, id)
	return err == nil
}

// TestDeleteAccountRefusesWhileAnEnabledDestinationIsAttached is the guard at
// its most ordinary: an operator disconnects an account that a running
// destination is publishing with.
//
// The delete used to succeed silently. ON DELETE SET NULL then rewrote the
// destination, which stayed enabled and lost every route to a stream key, and
// the only thing that said so was a dialog in one client with the wrong
// consequence text in it.
//
// Mutation: delete the `len(blocking) > 0 && !confirmed` branch from
// handleDeleteAccount. Observed FAIL ("status = 200, want 409").
func TestDeleteAccountRefusesWhileAnEnabledDestinationIsAttached(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformTwitch, "ada")
	if _, err := store.CreateDestination(&db.Destination{
		Name: "Twitch main", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://ingest.example/app", StreamKey: "sk", AccountID: &acctID, Enabled: true,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	w := deleteAccount(t, h, sign, acctID, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	got := decodeAccountDelete(t, w)
	if got.Code != codeAccountInUse {
		t.Errorf("code = %q, want %q -- a client must be able to branch without reading the sentence",
			got.Code, codeAccountInUse)
	}
	if got.Error == "" {
		t.Error(`the refusal carried no "error" field; the SPA's fetch wrapper reads that and nothing else`)
	}
	// The list is the whole reason this is not a bare 409: the confirmation a
	// client builds from it has to be able to name what is at stake.
	if len(got.Destinations) != 1 || got.Destinations[0].Name != "Twitch main" {
		t.Fatalf("destinations = %+v, want the one attached destination named", got.Destinations)
	}
	if !got.Destinations[0].Enabled {
		t.Error("the attached destination was not reported as enabled")
	}
	if !accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("the account was deleted despite the refusal -- a 409 that still deletes is worse than no guard")
	}
}

// TestDeleteAccountRefusesWhileABroadcastHasNotFinished is the half a count of
// ENABLED destinations would miss, and it is the expensive one.
//
// The destination is disabled -- the encoder has already stopped -- but the
// platform still has a broadcast open against it. Disconnecting takes the token
// away, the lifecycle coordinator untracks a destination with no AccountID by
// design, and the broadcast is never completed: it sits on the channel as live
// with no ingest, and the only remedy left is the platform's own studio.
//
// Mutation: drop the `|| d.Broadcasting` half of accountDestination.blocks.
// Observed FAIL ("status = 200, want 409").
func TestDeleteAccountRefusesWhileABroadcastHasNotFinished(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "ada")
	dest, err := store.CreateDestination(&db.Destination{
		Name: "YouTube main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: "sk", AccountID: &acctID, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if _, err := store.UpdateLifecycle(dest.ID, func(d *db.Destination) bool {
		d.Lifecycle.BroadcastID = "bcast-1"
		d.Lifecycle.Phase = phaseLive
		return true
	}); err != nil {
		t.Fatalf("record the broadcast: %v", err)
	}

	w := deleteAccount(t, h, sign, acctID, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a disabled destination still holding a live broadcast (body %s)",
			w.Code, w.Body.String())
	}
	got := decodeAccountDelete(t, w)
	if len(got.Destinations) != 1 || !got.Destinations[0].Broadcasting {
		t.Fatalf("destinations = %+v, want the destination reported as broadcasting", got.Destinations)
	}
	if got.Destinations[0].BroadcastID != "bcast-1" || got.Destinations[0].Phase != phaseLive {
		t.Errorf("broadcast id/phase = %q/%q, want %q/%q",
			got.Destinations[0].BroadcastID, got.Destinations[0].Phase, "bcast-1", phaseLive)
	}
	if !accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("the account was deleted despite the refusal")
	}
}

// TestDeleteAccountProceedsWhenNothingIsInFlight is the POSITIVE CONTROL for
// both refusals above.
//
// A handler that answered 409 unconditionally would satisfy every assertion in
// them. This is the assertion it could not pass: a disabled destination whose
// broadcast the platform has already called complete blocks nothing, the
// account goes, and the operator is still told which destination was unlinked.
func TestDeleteAccountProceedsWhenNothingIsInFlight(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformYouTube, "ada")
	dest, err := store.CreateDestination(&db.Destination{
		Name: "YouTube main", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: "sk", AccountID: &acctID, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if _, err := store.UpdateLifecycle(dest.ID, func(d *db.Destination) bool {
		d.Lifecycle.BroadcastID = "bcast-1"
		d.Lifecycle.Phase = phaseComplete
		return true
	}); err != nil {
		t.Fatalf("record the broadcast: %v", err)
	}

	w := deleteAccount(t, h, sign, acctID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a finished broadcast on a disabled destination blocks nothing (body %s)",
			w.Code, w.Body.String())
	}
	got := decodeAccountDelete(t, w)
	if got.Status != "disconnected" {
		t.Errorf("status = %q, want %q", got.Status, "disconnected")
	}
	// Attached but not blocking, so it is still reported: the operator has to
	// know the destination just lost its account.
	if len(got.Destinations) != 1 {
		t.Fatalf("destinations = %+v, want the unlinked destination reported", got.Destinations)
	}
	if len(got.Warnings) == 0 {
		t.Error("no warning was returned for a destination that was unlinked by the disconnect")
	}
	if accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("the account was not deleted")
	}
}

// TestDeleteAccountWithNoDestinationsIsUntouched is the second positive
// control, and the one that pins that ordinary disconnects did not become
// harder. This is the overwhelmingly common case: an account nothing uses.
func TestDeleteAccountWithNoDestinationsIsUntouched(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformTwitch, "ada")

	w := deleteAccount(t, h, sign, acctID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	got := decodeAccountDelete(t, w)
	if got.Status != "disconnected" {
		t.Errorf("status = %q, want %q", got.Status, "disconnected")
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for an account nothing was using", got.Warnings)
	}
	if accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("the account was not deleted")
	}
}

// TestDeleteAccountProceedsOnConfirmation is the escape hatch: the guard is a
// confirmation, not a prohibition, and an operator who has read the list must
// be able to go ahead.
//
// Mutation: make deleteConfirmed always return false. Observed FAIL
// ("status = 409, want 200").
func TestDeleteAccountProceedsOnConfirmation(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformTwitch, "ada")
	if _, err := store.CreateDestination(&db.Destination{
		Name: "Twitch main", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://ingest.example/app", StreamKey: "sk", AccountID: &acctID, Enabled: true,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	w := deleteAccount(t, h, sign, acctID, map[string]any{"confirm": true})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a confirmed disconnect (body %s)", w.Code, w.Body.String())
	}
	got := decodeAccountDelete(t, w)
	if len(got.Destinations) != 1 || got.Destinations[0].Name != "Twitch main" {
		t.Fatalf("destinations = %+v, want the unlinked destination named even on the confirmed path",
			got.Destinations)
	}
	if len(got.Warnings) == 0 {
		t.Error("a confirmed disconnect returned no warning; the operator still has to be told what happened")
	}
	if accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("a confirmed disconnect did not delete the account")
	}
}

// TestDeleteAccountRefusesAConfirmationItCannotUnderstand covers the client
// that meant to confirm and got the shape wrong. Reading that as "not
// confirmed" would be defensible; reading it as CONFIRMED would not, and a
// body decoded with DisallowUnknownFields cannot do either silently.
func TestDeleteAccountRefusesAConfirmationItCannotUnderstand(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformTwitch, "ada")
	if _, err := store.CreateDestination(&db.Destination{
		Name: "Twitch main", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://ingest.example/app", StreamKey: "sk", AccountID: &acctID, Enabled: true,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	// "confirmed", not "confirm".
	w := deleteAccount(t, h, sign, acctID, map[string]any{"confirmed": true})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a misspelt confirmation (body %s)", w.Code, w.Body.String())
	}
	if !accountIsStillConnected(t, store, s.box, acctID) {
		t.Fatal("the account was deleted on a body that did not confirm anything")
	}
}

// ------------------------------------------------------- the refresh/consent race

// TestTokenForYieldsToAnAccountReconnectedMidRefresh is hazard 5 end to end,
// and it is the loop rather than the single overwrite that makes it worth a
// test: the account asks to be reconnected, reconnecting loses this race, the
// pre-consent scope stamp comes back, and the account asks to be reconnected
// again. The remedy is the trigger.
//
// The stub's token endpoint IS the interleaving. It runs the reconnect --
// through the real UpsertPlatformAccount, which is what handleOAuthCallback and
// the device flow both call and what neither of them holds a lock for -- and
// only then answers the refresh. So by the time tokenFor comes back from the
// platform, the row it read no longer exists in the form it read it.
//
// Mutations run against it:
//   - restore `s.store.UpsertPlatformAccount(s.box, acct)` as the write in
//     tokenFor: observed FAIL, "scopes = ... want the reconnect's".
//   - relax UpdatePlatformAccountTokens' WHERE clause to `WHERE id=?`: observed
//     FAIL, same assertion.
func TestTokenForYieldsToAnAccountReconnectedMidRefresh(t *testing.T) {
	const (
		beforeScopes = "channel:manage:broadcast"
		afterScopes  = "channel:manage:broadcast channel:read:stream_key"
	)

	var store *db.DB
	var box *secrets.Box
	var acctRef string
	reconnected := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		// Consent completes WHILE the refresh is in flight. This is the whole
		// scenario: the operator saw a reconnect prompt and acted on it.
		_, err := store.UpsertPlatformAccount(box, &db.PlatformAccount{
			Platform:     db.PlatformTwitch,
			AccountName:  "ada (reconnected)",
			AccountRef:   acctRef,
			AccessToken:  "consent-access",
			RefreshToken: "consent-refresh",
			ExpiresAt:    time.Now().Add(2 * time.Hour),
			Scopes:       afterScopes,
			ScopeVer:     9,
		})
		reconnected <- err
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"refreshed","refresh_token":"refreshed-rt","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	s, _, st := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	store, box = st, s.box

	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: time.Now().Add(-time.Hour),
		Scopes:    beforeScopes, ScopeVer: 8,
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	acctRef = acct.AccountRef
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	got, err := s.tokenFor(t.Context(), acct.ID)
	if err != nil {
		t.Fatalf("tokenFor: %v", err)
	}
	if err := <-reconnected; err != nil {
		t.Fatalf("the reconnect this test depends on did not happen: %v", err)
	}

	// The consent facts are what the loop was made of. A refresh that wrote the
	// whole struct back puts beforeScopes and ScopeVer 8 here, and
	// oauth.AccountNeedsReconnect goes on asking for a reconnect that cannot
	// succeed.
	if got.Scopes != afterScopes {
		t.Errorf("scopes = %q, want the reconnect's %q -- a refresh that started before consent must not "+
			"write pre-consent scopes back over it", got.Scopes, afterScopes)
	}
	if got.ScopeVer != 9 {
		t.Errorf("scope_ver = %d, want 9", got.ScopeVer)
	}
	if got.AccountName != "ada (reconnected)" {
		t.Errorf("account name = %q, want %q", got.AccountName, "ada (reconnected)")
	}
	// And the loser yields the token as well: the reconnect's token is the one
	// minted against the grant the operator just gave, so it is the one to keep.
	if got.AccessToken != "consent-access" {
		t.Errorf("access token = %q, want the reconnect's %q", got.AccessToken, "consent-access")
	}

	// The store agrees with what the caller was handed. A tokenFor that returned
	// the right struct while having written the wrong row would pass everything
	// above and fail in production on the next read.
	stored, err := store.GetPlatformAccount(s.box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if stored.Scopes != afterScopes || stored.AccessToken != "consent-access" {
		t.Errorf("stored scopes/token = %q/%q, want %q/%q",
			stored.Scopes, stored.AccessToken, afterScopes, "consent-access")
	}
}

// TestTokenForStillRefreshesWhenNobodyRacesIt is the POSITIVE CONTROL for the
// test above.
//
// A tokenFor that always yielded -- or an UpdatePlatformAccountTokens that
// always answered ErrAccountRewritten -- would satisfy every assertion there,
// while quietly meaning that no token is ever renewed and every long broadcast
// dies at the first expiry. This is the assertion neither could pass.
func TestTokenForStillRefreshesWhenNobodyRacesIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"refreshed","refresh_token":"refreshed-rt","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	s, _, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})

	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: time.Now().Add(-time.Hour),
		Scopes:    "channel:manage:broadcast", ScopeVer: 8,
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	got, err := s.tokenFor(t.Context(), acct.ID)
	if err != nil {
		t.Fatalf("tokenFor: %v", err)
	}
	if got.AccessToken != "refreshed" {
		t.Errorf("access token = %q, want %q -- an uncontended refresh must still land", got.AccessToken, "refreshed")
	}
	if got.RefreshToken != "refreshed-rt" {
		t.Errorf("refresh token = %q, want %q", got.RefreshToken, "refreshed-rt")
	}
	if got.Expired() {
		t.Error("the refreshed account still reads as expired")
	}
	stored, err := store.GetPlatformAccount(s.box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if stored.AccessToken != "refreshed" {
		t.Errorf("stored access token = %q, want %q -- the refresh was returned but never persisted",
			stored.AccessToken, "refreshed")
	}
	if stored.ScopeVer != 8 || stored.Scopes != "channel:manage:broadcast" {
		t.Errorf("stored scope_ver/scopes = %d/%q, want 8/%q -- an uncontended refresh must not disturb them",
			stored.ScopeVer, stored.Scopes, "channel:manage:broadcast")
	}
}
