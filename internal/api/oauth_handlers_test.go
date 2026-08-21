package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider is the
// regression this fix round exists for: refresh-key's mapping from a stored
// destination to oauth.IngestOptions had no test at all, so a hardcoded
// db.FBPrivacyEveryone -- the most-exposing value Facebook offers -- or the
// mapping being deleted outright both left the whole suite green. This pins
// the mapping at the one function that produces it, which is also the one the
// handler actually calls, so there is no second copy to drift from it.
func TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider(t *testing.T) {
	dest := &db.Destination{
		Platform: db.PlatformFacebook,
		Compliance: db.Compliance{
			FacebookPrivacy: db.FBPrivacySelf,
		},
		Facebook: db.FacebookSettings{
			Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
			DonateCharityID: "999",
		},
	}

	got := ingestOptionsFor(dest, time.Time{})
	want := oauth.IngestOptions{
		Privacy:         db.FBPrivacySelf,
		Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
		DonateCharityID: "999",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ingestOptionsFor = %+v, want %+v", got, want)
	}
}

// TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing is the other
// half: a destination that never touched Facebook's declarative fields must
// produce the zero IngestOptions, which is what tells IngestFor to leave
// every field alone rather than sending an empty-but-present value.
func TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing(t *testing.T) {
	dest := &db.Destination{Platform: db.PlatformFacebook}

	got := ingestOptionsFor(dest, time.Time{})
	if !reflect.DeepEqual(got, oauth.IngestOptions{}) {
		t.Errorf("ingestOptionsFor(unconfigured) = %+v, want the zero value", got)
	}
}

// TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider closes
// the gap ingestOptionsFor's own test could not: that function being correct
// proves nothing about whether handleRefreshKey still calls it.
//
// It drives the real HTTP handler, through the real ingestOptionsFor(dest),
// into the real Facebook provider, and reads the Graph request that came out
// the far side. It used to replace an ingestForFn closure on Server and assert
// on the oauth.IngestOptions it captured, because internal/oauth's graph base
// was unexported and there was nowhere else to stand. There is now:
// oauth.WithBaseURL points the whole provider at this test's own server, so the
// assertion is on what Facebook would have received rather than on what the
// handler intended to send.
//
// Mutation to run against it: in ingestOptionsFor, replace
// `Privacy: dest.Compliance.FacebookPrivacy` with
// `Privacy: db.FBPrivacyEveryone` -- the most-exposing value Facebook offers.
// Observed FAIL ("privacy = EVERYONE, want SELF").
func TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider(t *testing.T) {
	// stubbedServer plus a real engine manager: refresh-key carries
	// requireSource now, so a manager-less server answers 503 without calling
	// Facebook at all and there would be no Graph request to read.
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})

	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Facebook", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-old", AccountID: &acctID,
		Compliance: db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
		Facebook: db.FacebookSettings{
			Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
			DonateCharityID: "999",
		},
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	// Graph refuses the create so the handler exits before it reconciles.
	// Nothing here depends on the reconcile succeeding -- the fixture's FFmpeg
	// path cannot exec -- and the request is recorded before the refusal is
	// written, so what Graph answers has no bearing on what this test reads.
	stub.setCreateErr("stub: no real ingest in this test")

	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the stub's error (body %s)", w.Code, w.Body.String())
	}

	create := stub.first(http.MethodPost, "/me/live_videos")
	if create == nil {
		t.Fatalf("no broadcast create reached Facebook; refresh-key made these calls instead: %v",
			stub.calls())
	}
	// Graph's own wire form for each stored choice. Asserting the encoded
	// parameter rather than the oauth.IngestOptions struct is the point of the
	// rewrite: a mapping that is correct and a provider that drops the field on
	// the way out are different bugs, and only this catches the second.
	for _, want := range []struct{ key, value string }{
		{"status", "LIVE_NOW"},
		{"privacy", `{"value":"SELF"}`},
		{"crossposting_actions", `[{"page_id":"1234","action":"enable_crossposting_and_create_post"}]`},
		{"donate_button_charity_id", "999"},
	} {
		if got := create.Query.Get(want.key); got != want.value {
			t.Errorf("%s = %q, want %q (whole request %s)", want.key, got, want.value, create)
		}
	}
	// The destination never asked for backup ingest, and Facebook treats a
	// present-but-empty parameter as a value, so "not chosen" has to mean an
	// ABSENT key rather than an empty one.
	if _, ok := create.Query["enable_backup_ingest"]; ok {
		t.Errorf("enable_backup_ingest was sent for a destination that never asked for it: %s", create)
	}
}

// TestTokenForSerializesConcurrentRefreshesOfTheSameAccount is finding #6:
// RefreshLoop's ticker and an on-demand tokenFor call (or two on-demand calls
// from two publishes in flight) can both see the same account as expired and
// both hit the platform's refresh endpoint. UpsertPlatformAccount has no
// compare-and-swap, so the loser's write can land after and store a token the
// platform has already invalidated -- surfacing as a refresh failure
// mid-broadcast.
//
// A dedicated single-endpoint stub replaces the shared platformStub here
// because the shared one has no case for a token endpoint and would answer
// with a Facebook live-video body carrying no access_token, which fails the
// refresh outright and would make both callers legitimately re-attempt --
// masking exactly the race this test exists to catch.
//
// Two goroutines both call tokenFor for one already-expired account. The
// stub blocks the FIRST refresh mid-flight and only then releases it, so a
// second, unserialized caller would have every opportunity to race in and
// hit the endpoint too. Mutation: comment out the `unlock := refreshLocks...`
// line and its lock/defer in tokenFor (leaving the re-read in place so it
// still compiles) -- observed FAIL, "provider.Refresh reached Twitch 2 times".
func TestTokenForSerializesConcurrentRefreshesOfTheSameAccount(t *testing.T) {
	var calls int32
	entered := make(chan struct{}, 1)
	proceed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// Hold the first refresh open long enough that a second,
			// unserialized caller would reach this handler too.
			entered <- struct{}{}
			<-proceed
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"refreshed-%d","refresh_token":"rt-%d","expires_in":3600}`, n, n)
	}))
	t.Cleanup(srv.Close)

	s, _, store, _ := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})

	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]*db.PlatformAccount, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.tokenFor(context.Background(), acct.ID)
		}()
	}

	<-entered
	close(proceed)
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("provider.Refresh reached Twitch %d times for one account refreshed by two concurrent callers; want 1 -- "+
			"the second caller should have waited for the first and reused its result instead of racing it to UpsertPlatformAccount (#6)", n)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("tokenFor[%d]: %v", i, err)
		}
	}
	if results[0].AccessToken != "refreshed-1" || results[1].AccessToken != "refreshed-1" {
		t.Fatalf("both callers should observe the single refreshed token; got %q and %q",
			results[0].AccessToken, results[1].AccessToken)
	}
}
