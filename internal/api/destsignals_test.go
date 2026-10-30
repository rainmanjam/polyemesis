package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The api half of the Stream Deck's destination signals: the account health
// reader behind DestStatus.AccountHealth and the platform health read behind
// DestStatus.PlatformHealth.

// TestTheKeyAndTheDialogReadTheSameAccountHealth: the reader the engine gets
// answers with the state GET /platforms/accounts shows, for an account that
// cannot refresh and has run out, one that is close to it, and a healthy one.
func TestTheKeyAndTheDialogReadTheSameAccountHealth(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	seed := func(ref string, rt string, exp time.Time) int64 {
		a, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
			Platform: db.PlatformYouTube, AccountName: ref, AccountRef: ref,
			AccessToken: "at-" + ref, RefreshToken: rt, ExpiresAt: exp, ScopeVer: 8,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	expired := seed("gone", "", time.Now().Add(-time.Hour))
	expiring := seed("soon", "", time.Now().Add(2*time.Hour))
	fine := seed("fine", "rt", time.Now().Add(-time.Hour))

	r := s.AccountHealth()
	for id, want := range map[int64]string{expired: AccountHealthExpired, expiring: AccountHealthExpiring, fine: AccountHealthOK} {
		if got := r.AccountHealth(id); got != want {
			t.Errorf("account %d: AccountHealth = %q, want %q", id, got, want)
		}
	}
	s.acctHealth.fail(fine, errors.New("invalid_grant"), time.Now())
	// Cached for accountHealthTTL, so a fresh reader sees the failure at once.
	if got := s.AccountHealth().AccountHealth(fine); got != AccountHealthRefreshFailing {
		t.Errorf("after a failed refresh: AccountHealth = %q, want refreshFailing", got)
	}
}

// TestTheAccountHealthReaderReadsTheStoreOncePerTTL: the reader is called from
// Status, per push, per destination. Ten destinations on one push must cost one
// list, and a failed list keeps the last answer instead of clearing it.
func TestTheAccountHealthReaderReadsTheStoreOncePerTTL(t *testing.T) {
	var lists atomic.Int32
	fail := false
	now := time.Now()
	c := &accountHealthCache{
		list: func() ([]db.PlatformAccount, error) {
			lists.Add(1)
			if fail {
				return nil, errors.New("database is locked")
			}
			return []db.PlatformAccount{{ID: 1}}, nil
		},
		health: func(db.PlatformAccount, time.Time) AccountHealth { return AccountHealth{State: AccountHealthExpired} },
		now:    func() time.Time { return now },
	}
	for i := 0; i < 10; i++ {
		if got := c.AccountHealth(1); got != AccountHealthExpired {
			t.Fatalf("AccountHealth = %q", got)
		}
	}
	if n := lists.Load(); n != 1 {
		t.Fatalf("%d store reads for ten lookups inside the TTL, want 1", n)
	}
	fail = true
	now = now.Add(accountHealthTTL)
	if got := c.AccountHealth(1); got != AccountHealthExpired {
		t.Fatalf("a failed store read cleared the answer: %q", got)
	}
}

// healthFixture is a platformHealthCheck aimed at a stub YouTube that answers
// listed and counts requests, failing the test on anything but a GET.
func healthFixture(t *testing.T, rows map[int64]*db.Destination, listed string) (*platformHealthCheck, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("the health read sent %s %s; it must only read", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, listed)
	}))
	t.Cleanup(srv.Close)
	return &platformHealthCheck{
		dest: func(id int64) (*db.Destination, error) { return rows[id], nil },
		token: func(context.Context, int64) (*db.PlatformAccount, error) {
			return &db.PlatformAccount{ID: 5, Platform: db.PlatformYouTube, AccessToken: "tok"}, nil
		},
		clientID:  func(db.Platform) (string, error) { return "cid", nil },
		providers: oauth.NewSet(oauth.WithBaseURL(srv.URL)),
		now:       time.Now,
	}, &calls
}

const ytListedHealth = `{"items":[
 {"id":"s1","cdn":{"ingestionInfo":{"streamName":"key-a"}},"status":{"healthStatus":{"status":"bad",
   "configurationIssues":[{"type":"bitrateLow","severity":"error"}]}}},
 {"id":"s2","cdn":{"ingestionInfo":{"streamName":"key-b"}},"status":{"healthStatus":{"status":"good"}}}]}`

// TestOneChannelReadAnswersForEveryDestinationOnIt is the quota argument: two
// destinations on one channel spend one liveStreams.list, not two.
func TestOneChannelReadAnswersForEveryDestinationOnIt(t *testing.T) {
	acct := int64(5)
	rows := map[int64]*db.Destination{
		1: {ID: 1, Platform: db.PlatformYouTube, StreamKey: "key-a", AccountID: &acct},
		2: {ID: 2, Platform: db.PlatformYouTube, StreamKey: "key-b", AccountID: &acct},
	}
	k, calls := healthFixture(t, rows, ytListedHealth)
	a, ok, err := k.StreamHealth(context.Background(), 1)
	if err != nil || !ok || a.Status != "bad" || len(a.Issues) != 1 || a.Issues[0] != "bitrateLow" {
		t.Fatalf("destination 1 = %+v ok=%v err=%v, want bad with bitrateLow", a, ok, err)
	}
	b, ok, err := k.StreamHealth(context.Background(), 2)
	if err != nil || !ok || b.Status != "good" {
		t.Fatalf("destination 2 = %+v ok=%v err=%v, want good", b, ok, err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d platform reads for two destinations on one channel, want 1", n)
	}
}

// TestNoHealthReadForAPastedKeyOrAPlatformWithoutOne: a pasted key has no
// token, and Twitch has no health API. Neither spends a request.
func TestNoHealthReadForAPastedKeyOrAPlatformWithoutOne(t *testing.T) {
	acct := int64(5)
	rows := map[int64]*db.Destination{
		1: {ID: 1, Platform: db.PlatformYouTube, StreamKey: "key-a"},
		2: {ID: 2, Platform: db.PlatformTwitch, StreamKey: "live_x", AccountID: &acct},
	}
	k, calls := healthFixture(t, rows, ytListedHealth)
	for id := range rows {
		if _, ok, err := k.StreamHealth(context.Background(), id); ok || err != nil {
			t.Fatalf("destination %d: ok=%v err=%v, want not asked", id, ok, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d platform reads, want 0", n)
	}
}

// TestNoHealthReadWithAnotherPlatformsToken: a row whose account is on another
// platform is one dropUnsendableSettings has not repaired yet; asking YouTube
// with a Twitch token can only fail, or answer about the wrong channel.
func TestNoHealthReadWithAnotherPlatformsToken(t *testing.T) {
	acct := int64(5)
	rows := map[int64]*db.Destination{1: {ID: 1, Platform: db.PlatformYouTube, StreamKey: "key-a", AccountID: &acct}}
	k, calls := healthFixture(t, rows, ytListedHealth)
	k.token = func(context.Context, int64) (*db.PlatformAccount, error) {
		return &db.PlatformAccount{ID: 5, Platform: db.PlatformTwitch, AccessToken: "tok"}, nil
	}
	if _, ok, err := k.StreamHealth(context.Background(), 1); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want not asked", ok, err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d platform reads with another platform's token, want 0", n)
	}
}

// TestAKeyTheChannelDoesNotListIsNotJudged: no stream with this key on the
// channel means YouTube has said nothing about this destination.
func TestAKeyTheChannelDoesNotListIsNotJudged(t *testing.T) {
	acct := int64(5)
	rows := map[int64]*db.Destination{1: {ID: 1, Platform: db.PlatformYouTube, StreamKey: "key-elsewhere", AccountID: &acct}}
	k, _ := healthFixture(t, rows, ytListedHealth)
	if _, ok, err := k.StreamHealth(context.Background(), 1); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want unjudged", ok, err)
	}
}

// TestAReadTokensStatusCarriesTheAccountStateAndNothingBehindIt is the leak
// test for DestStatus.AccountHealth, end to end through GET /status with a
// read-scoped token: the state word arrives, and none of what it was derived
// from does -- not the refresh error, not the account's name, not its tokens,
// not the destination's stream key. Every sentinel is built at runtime.
//
// MUTATION: blank `ds.AccountHealth = e.accountHealth(row)` in the engine's
// Status. Observed: FAIL here, "the status does not carry the account's state".
// The whitelist's half is TestOnlyAStateWordCanReachTheStatus: with
// engine.accountHealth's default arm returning h, it FAILs with the refresh
// error on the status.
func TestAReadTokensStatusCarriesTheAccountStateAndNothingBehindIt(t *testing.T) {
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	mark := strconv.FormatInt(time.Now().UnixNano(), 36)
	refreshErr := "refresh-refused-" + mark
	acctName := "channel-" + mark
	access := "access-" + mark
	refresh := "refresh-" + mark
	streamKey := "streamkey-" + mark
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformYouTube, AccountName: acctName, AccountRef: "ref-" + mark,
		AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour), ScopeVer: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.acctHealth.fail(acct.ID, errors.New(refreshErr), time.Now())
	one := int64(1)
	if _, err := store.CreateDestination(&db.Destination{
		Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube, URL: "rtmp://a.rtmp.youtube.com/live2",
		StreamKey: streamKey, AccountID: &acct.ID, SourceID: &one,
	}); err != nil {
		t.Fatal(err)
	}
	// What main.go does; the test server is built without it.
	s.mgr.SetAccountHealth(s.AccountHealth())
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatal(err)
	}
	read := createScopedToken(t, h, sign, "deck", db.ScopeRead)

	r := jsonRequest(t, http.MethodGet, "/api/v1/status", nil)
	bearer(read)(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /status = %d to a read token", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"accountHealth":"refreshFailing"`) {
		t.Fatalf("the status does not carry the account's state: %s", body)
	}
	for _, secret := range []string{refreshErr, acctName, access, refresh, streamKey} {
		if strings.Contains(body, secret) {
			t.Fatalf("GET /status handed a read token %q", secret)
		}
	}
}

// leakyAccountHealth answers with whatever it is given, to prove the engine's
// whitelist is what keeps a non-state value off the status.
type leakyAccountHealth string

func (l leakyAccountHealth) AccountHealth(int64) string { return string(l) }

// TestOnlyAStateWordCanReachTheStatus: a reader that returned something other
// than a state word -- a refresh error, say -- is not passed through.
func TestOnlyAStateWordCanReachTheStatus(t *testing.T) {
	s, _, store, _ := engineServer(t, defaultTools(), Options{})
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformYouTube, AccountName: "n", AccountRef: "r", AccessToken: "a", ScopeVer: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	if _, err := store.CreateDestination(&db.Destination{
		Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube, URL: "rtmp://a.rtmp.youtube.com/live2",
		StreamKey: "k", AccountID: &acct.ID, SourceID: &one,
	}); err != nil {
		t.Fatal(err)
	}
	s.mgr.SetAccountHealth(leakyAccountHealth("invalid_grant: token revoked"))
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatal(err)
	}
	for _, d := range s.mgr.DestinationStatuses() {
		if d.AccountHealth != "" {
			t.Fatalf("accountHealth = %q: a non-state value reached the status", d.AccountHealth)
		}
	}
}
