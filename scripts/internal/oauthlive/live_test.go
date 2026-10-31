package oauthlive

// The credentialed step of acceptance-oauth.sh is run by hand, against a real
// account, by somebody who read its header and was told it "starts no
// broadcast". For Facebook that was false: the step ended by calling
// Provider.Ingest, and Facebook's Ingest is POST /{node}/live_videos with
// status=LIVE_NOW -- every run left a real live video on the account. YouTube's
// was false conditionally: a channel with no reusable stream gets one inserted.
//
// A comment cannot keep that promise, because the promise is about which
// methods are called and the comment is about what somebody believed when it
// was written. These tests keep it instead:
//
//   - a recording fake proves the DEFAULT run calls no method that can create
//     anything, on every platform, including ones the policy has never heard of;
//   - the same fake proves the opt-in run ends what it created, including when
//     the create half-succeeded;
//   - a wire-level stub proves the policy table describes the real providers:
//     the "read" platforms issue only GETs from Ingest, and the others do not;
//   - an AST read of the driver proves it cannot route around this package.

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// fakeProvider records every method called on it, by name, and answers with
// just enough for Run to reach the ingest step. It implements every capability
// Run might look for -- LiveStatter, StreamKeyReader and the Facebook
// create/end pair -- on EVERY platform, so the only thing deciding what gets
// called is Run's policy and not which interfaces happen to be satisfied.
type fakeProvider struct {
	platform db.Platform

	mu    sync.Mutex
	calls []string
	ended []string // live video ids passed to EndBroadcast

	keys         []string
	ingestForErr error
	ingestForURL string
}

func (f *fakeProvider) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeProvider) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeProvider) Platform() db.Platform { return f.platform }
func (f *fakeProvider) Scopes() []string      { return []string{"scope.a"} }
func (f *fakeProvider) ScopeVersion() int     { return 1 }
func (f *fakeProvider) PKCE() bool            { return false }
func (f *fakeProvider) AuthURL(_, _, _, _ string) string {
	f.record("AuthURL")
	return "https://example.invalid/authorize"
}
func (f *fakeProvider) Exchange(context.Context, string, string, string, string, string) (*oauth.Token, error) {
	f.record("Exchange")
	return nil, errors.New("not used")
}
func (f *fakeProvider) Refresh(context.Context, string, string, string) (*oauth.Token, error) {
	f.record("Refresh")
	return &oauth.Token{AccessToken: "fake-access", RefreshToken: "fake-refresh",
		ExpiresAt: time.Now().Add(time.Hour), Scopes: "scope.a"}, nil
}
func (f *fakeProvider) Account(context.Context, string, string) (*oauth.Account, error) {
	f.record("Account")
	return &oauth.Account{Name: "someone", Ref: "1"}, nil
}
func (f *fakeProvider) Ingest(context.Context, string, string) (*oauth.Ingest, error) {
	f.record("Ingest")
	return &oauth.Ingest{URL: "rtmps://ingest.example.invalid/app", Key: "fake-stream-key"}, nil
}
func (f *fakeProvider) Stats(context.Context, string, string) (*oauth.LiveStats, error) {
	f.record("Stats")
	return &oauth.LiveStats{Source: "fake"}, nil
}
func (f *fakeProvider) StreamKeys(context.Context, string, string) ([]string, error) {
	f.record("StreamKeys")
	return f.keys, nil
}
func (f *fakeProvider) IngestFor(_ context.Context, _, _, _ string, _ oauth.IngestOptions) (*oauth.Broadcast, error) {
	f.record("IngestFor")
	if f.ingestForErr != nil {
		return nil, f.ingestForErr
	}
	u := f.ingestForURL
	if u == "" {
		u = "rtmps://live-api-s.facebook.example.invalid:443/rtmp/"
	}
	return &oauth.Broadcast{ID: "1234567890", Target: "",
		Ingest: oauth.Ingest{URL: u, Key: "fake-fb-key"}}, nil
}
func (f *fakeProvider) EndBroadcast(_ context.Context, _, _, id string) (*oauth.BroadcastEnd, error) {
	f.record("EndBroadcast")
	f.mu.Lock()
	f.ended = append(f.ended, id)
	f.mu.Unlock()
	return &oauth.BroadcastEnd{Status: "VOD", Ended: true}, nil
}

// creates is THE TEST'S OWN copy of which methods can create something on the
// platform, written from the provider sources rather than from policyFor. If
// the two were the same table a wrong entry would agree with itself.
//
// EndBroadcast is listed everywhere because a default run has nothing to end;
// calling it would mean a create happened that this test did not see.
func creates(p db.Platform) []string {
	always := []string{"Exchange", "IngestFor", "EndBroadcast"}
	switch p {
	case db.PlatformTwitch, db.PlatformKick:
		// Twitch: GET /users then GET /streams/key. Kick: GET /channels.
		return always
	default:
		// Facebook: POST /{node}/live_videos, every call. YouTube: liveStreams
		// insert when the channel has no reusable stream. Anything else: not
		// audited, so assumed to create.
		return append(always, "Ingest")
	}
}

func creds() Creds {
	return Creds{ClientID: "fake-client", ClientSecret: "fake-secret", RefreshToken: "fake-refresh"}
}

// collect returns an Emit that keeps the last value per key.
func collect() (Emit, map[string]string) {
	out := map[string]string{}
	var mu sync.Mutex
	return func(k string, v any) {
		mu.Lock()
		defer mu.Unlock()
		out[k] = fmt.Sprint(v)
	}, out
}

func TestTheDefaultLiveRunCallsNoProviderMethodThatCanCreateABroadcastOrStreamOnAnyPlatform(t *testing.T) {
	platforms := []db.Platform{
		db.PlatformYouTube, db.PlatformTwitch, db.PlatformFacebook, db.PlatformKick,
		// Never audited: the policy must fail closed for these, not fall
		// through to Ingest because nobody wrote a case for them.
		db.PlatformTrovo, db.PlatformVimeo,
	}
	for _, p := range platforms {
		t.Run(string(p), func(t *testing.T) {
			f := &fakeProvider{platform: p, keys: []string{"fake-yt-key"}}
			emit, out := collect()
			Run(context.Background(), f, creds(), Options{}, emit)

			for _, m := range creates(p) {
				if f.called(m) {
					t.Errorf("the default run called %s on %s, which can create something on the account; calls: %v",
						m, p, f.calls)
				}
			}
			// Non-vacuous: the run got as far as the ingest step, so the
			// absence above is a decision and not an early exit.
			if !f.called("Refresh") || !f.called("Account") {
				t.Fatalf("the run never reached the account step; calls: %v", f.calls)
			}
			if out["ingestMode"] == "" {
				t.Fatalf("the run reported no ingest mode, so it never reached the ingest step; out: %v", out)
			}
		})
	}
}

func TestTheDefaultLiveRunStillReadsTheIngestWhereReadingItHasNoSideEffect(t *testing.T) {
	// The fix must not buy safety by checking nothing: #312's field is still
	// read on every platform where a read exists.
	for _, tc := range []struct {
		p    db.Platform
		via  string
		mode string
	}{
		{db.PlatformTwitch, "Ingest", ModeRead},
		{db.PlatformKick, "Ingest", ModeRead},
		{db.PlatformYouTube, "StreamKeys", ModeKeys},
	} {
		t.Run(string(tc.p), func(t *testing.T) {
			f := &fakeProvider{platform: tc.p, keys: []string{"fake-yt-key"}}
			emit, out := collect()
			Run(context.Background(), f, creds(), Options{}, emit)
			if !f.called(tc.via) {
				t.Errorf("%s: expected the ingest to be read through %s; calls: %v", tc.p, tc.via, f.calls)
			}
			if out["ingestMode"] != tc.mode || out["ingestOK"] != "true" {
				t.Errorf("%s: ingestMode=%q ingestOK=%q, want %q/true", tc.p, out["ingestMode"], out["ingestOK"], tc.mode)
			}
			if out["ingestKeyClean"] != "true" {
				t.Errorf("%s: a clean fake key was not reported clean: %v", tc.p, out)
			}
		})
	}
}

func TestTheDefaultFacebookRunSkipsTheIngestAndNamesTheFlagThatWouldCreateABroadcast(t *testing.T) {
	f := &fakeProvider{platform: db.PlatformFacebook}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{}, emit)
	if out["ingestOK"] != "skipped" || out["ingestMode"] != ModeSkipped {
		t.Fatalf("ingestOK=%q ingestMode=%q, want skipped/skipped", out["ingestOK"], out["ingestMode"])
	}
	if !strings.Contains(out["ingestSkipReason"], "-"+AllowBroadcastFlag) {
		t.Errorf("the skip reason does not tell the operator how to opt in: %q", out["ingestSkipReason"])
	}
}

func TestAYouTubeChannelWithNoStreamIsReportedAsSkippedRatherThanHavingOneCreated(t *testing.T) {
	// Exactly the case where the real Ingest would insert a liveStream.
	f := &fakeProvider{platform: db.PlatformYouTube}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{AllowBroadcast: true}, emit)
	if f.called("Ingest") || f.called("IngestFor") {
		t.Fatalf("YouTube was asked for an ingest that would create a stream; calls: %v", f.calls)
	}
	if out["ingestOK"] != "skipped" {
		t.Errorf("ingestOK=%q, want skipped for a channel with no stream", out["ingestOK"])
	}
}

func TestTheOptedInFacebookRunEndsTheLiveVideoItCreatedAndReportsIt(t *testing.T) {
	f := &fakeProvider{platform: db.PlatformFacebook}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{AllowBroadcast: true}, emit)

	if f.called("Ingest") {
		t.Errorf("Ingest discards the live video id, so what it creates cannot be ended; IngestFor must be used")
	}
	if !f.called("IngestFor") {
		t.Fatalf("the opted-in run did not create the broadcast it was allowed to; calls: %v", f.calls)
	}
	if len(f.ended) != 1 || f.ended[0] != "1234567890" {
		t.Fatalf("EndBroadcast got %v, want exactly the created id", f.ended)
	}
	if out["createdLiveVideo"] != "1234567890" || out["cleanupOK"] != "true" || out["cleanupEnded"] != "true" {
		t.Errorf("the create and the cleanup were not both reported: %v", out)
	}
	if out["ingestOK"] != "true" || out["ingestMode"] != ModeBroadcast || out["ingestHost"] == "" {
		t.Errorf("the ingest itself was not reported: %v", out)
	}
}

func TestALiveVideoCreatedByAFailedIngestForIsStillEnded(t *testing.T) {
	// IngestFor's own wording when the POST succeeded and the answer was
	// unusable. The id survives only in the error text, and a run that took
	// the error at face value would leave the live video behind.
	f := &fakeProvider{platform: db.PlatformFacebook,
		ingestForErr: errors.New("Facebook created live video 987654321 but returned no ingest URL")}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{AllowBroadcast: true}, emit)
	if len(f.ended) != 1 || f.ended[0] != "987654321" {
		t.Fatalf("EndBroadcast got %v, want the id named in the error", f.ended)
	}
	if out["ingestOK"] != "false" || out["cleanupOK"] != "true" {
		t.Errorf("want a failed ingest with a successful cleanup: %v", out)
	}
}

func TestAFailedIngestForThatNamesNoIdIsReportedAsAPossibleStray(t *testing.T) {
	f := &fakeProvider{platform: db.PlatformFacebook,
		ingestForErr: errors.New("Facebook accepted the broadcast but returned no live video id")}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{AllowBroadcast: true}, emit)
	if len(f.ended) != 0 {
		t.Fatalf("ended %v with no id to end", f.ended)
	}
	if out["possibleStray"] != "true" || out["cleanupOK"] != "false" {
		t.Errorf("a create that may have happened was not flagged for a human: %v", out)
	}
}

func TestAnIngestThatFailsItsChecksAfterTheCreateStillEndsTheLiveVideo(t *testing.T) {
	// An unparseable ingest URL is a failure of the step AFTER the create.
	// The cleanup is deferred, so the order of what goes wrong cannot matter.
	f := &fakeProvider{platform: db.PlatformFacebook, ingestForURL: "::not a url"}
	emit, out := collect()
	Run(context.Background(), f, creds(), Options{AllowBroadcast: true}, emit)
	if len(f.ended) != 1 {
		t.Fatalf("EndBroadcast got %v, want the created id", f.ended)
	}
	if out["cleanupOK"] != "true" {
		t.Errorf("cleanup not reported: %v", out)
	}
}

func TestTheCleanupIsNotBoundToTheRunsOwnDeadline(t *testing.T) {
	// The run's 60-second context can be spent by the time the create returns.
	// A cleanup on that context would fail instantly and leave the video up.
	f := &endCtxProvider{fakeProvider: fakeProvider{platform: db.PlatformFacebook}}
	ctx, cancel := context.WithCancel(context.Background())
	f.onCreate = cancel // the run's deadline passes during the create
	emit, out := collect()
	Run(ctx, f, creds(), Options{AllowBroadcast: true}, emit)
	if f.endCtxErr != nil {
		t.Fatalf("EndBroadcast ran on a dead context (%v)", f.endCtxErr)
	}
	if out["cleanupOK"] != "true" {
		t.Errorf("cleanup not reported: %v", out)
	}
}

type endCtxProvider struct {
	fakeProvider
	onCreate  func()
	endCtxErr error
}

func (e *endCtxProvider) IngestFor(ctx context.Context, a, b, c string, o oauth.IngestOptions) (*oauth.Broadcast, error) {
	br, err := e.fakeProvider.IngestFor(ctx, a, b, c, o)
	e.onCreate()
	return br, err
}

func (e *endCtxProvider) EndBroadcast(ctx context.Context, a, b, id string) (*oauth.BroadcastEnd, error) {
	e.endCtxErr = ctx.Err()
	return e.fakeProvider.EndBroadcast(ctx, a, b, id)
}

func TestARunWithoutACompleteCredentialSetCallsNothing(t *testing.T) {
	f := &fakeProvider{platform: db.PlatformFacebook}
	emit, out := collect()
	Run(context.Background(), f, Creds{ClientID: "x", ClientSecret: "y"}, Options{AllowBroadcast: true}, emit)
	if out["skipped"] != "true" || len(f.calls) != 0 {
		t.Errorf("skipped=%q calls=%v", out["skipped"], f.calls)
	}
}

func TestTheAllowBroadcastHelpSaysWhatItCreatesThatItIsPublicAndThatItIsEnded(t *testing.T) {
	for _, want := range []string{"live video", "public", "ends", "not deleting"} {
		if !strings.Contains(AllowBroadcastUsage, want) {
			t.Errorf("the -%s help does not say %q: %s", AllowBroadcastFlag, want, AllowBroadcastUsage)
		}
	}
}

// ------------------------------------------------------- the real providers

// TestThePolicyTableMatchesWhatEachRealProviderSendsOnTheWire aims the real
// internal/oauth providers at one stub and records every method they send from
// Ingest. The "read" platforms must send only GETs; the others must be seen
// sending a write, which is the positive control -- a stub that could not see
// a POST would pass the first half for nothing.
func TestThePolicyTableMatchesWhatEachRealProviderSendsOnTheWire(t *testing.T) {
	for _, p := range []db.Platform{db.PlatformTwitch, db.PlatformKick, db.PlatformFacebook, db.PlatformYouTube} {
		t.Run(string(p), func(t *testing.T) {
			var mu sync.Mutex
			var writes []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mu.Lock()
					writes = append(writes, r.Method+" "+r.URL.Path)
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				// One body every provider can decode its own fields out of.
				// No "items", so YouTube sees a channel with no stream.
				fmt.Fprint(w, `{"id":"1","data":[{"id":"1","login":"someone","display_name":"someone",`+
					`"stream_key":"fake-key-one","broadcaster_user_id":1,"slug":"someone",`+
					`"stream":{"url":"rtmps://ingest.example.invalid/app","key":"fake-key-two"}}]}`)
			}))
			defer srv.Close()

			prov := oauth.ProvidersWith(oauth.WithBaseURL(srv.URL))[p]
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, ingErr := prov.Ingest(ctx, "fake-client", "fake-access")

			mu.Lock()
			defer mu.Unlock()
			if policyFor(p) == ModeRead {
				if ingErr != nil {
					t.Fatalf("the stub did not satisfy %s's Ingest, so its GETs prove nothing: %v", p, ingErr)
				}
				if len(writes) != 0 {
					t.Errorf("%s is classed as read-only but its Ingest sent %v", p, writes)
				}
			} else if len(writes) == 0 {
				t.Errorf("%s is classed as able to create, but its Ingest sent no write against a channel with nothing on it; "+
					"either the class is wrong or the stub cannot see writes (err: %v)", p, ingErr)
			}
		})
	}
}

// --------------------------------------------------------------- the driver

// TestTheDriverCannotCallAnIngestMethodExceptThroughThisPackage reads the
// driver's source. It is `//go:build ignore`, so no test can link it; but the
// guarantee above is only worth anything if the driver's live command is Run,
// and a later edit that called p.Ingest directly beside it would bring the
// stray live video back with every test here still green.
func TestTheDriverCannotCallAnIngestMethodExceptThroughThisPackage(t *testing.T) {
	const path = "../../acceptance_oauth_driver.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	forbidden := map[string]bool{
		"Ingest": true, "IngestFor": true, "EndBroadcast": true, "Exchange": true,
		"StreamKeys": true, "AddBackupIngest": true, "UpdateLiveVideo": true,
		"PushMetadata": true, "PushCompliance": true, "RescheduleBroadcast": true,
	}
	sawRun := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "oauthlive" && sel.Sel.Name == "Run" {
			sawRun = true
			return true
		}
		if forbidden[sel.Sel.Name] {
			t.Errorf("%s: the driver calls .%s directly; route it through oauthlive so the read-only default covers it",
				fset.Position(call.Pos()), sel.Sel.Name)
		}
		return true
	})
	if !sawRun {
		t.Errorf("the driver never calls oauthlive.Run, so nothing here governs its live command")
	}
}
