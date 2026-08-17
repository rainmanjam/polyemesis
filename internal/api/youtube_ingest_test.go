package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// WHICH DESTINATION KEEPS THE ACCOUNT'S SHARED INGEST STREAM.
//
// YouTube counts concurrent broadcasts per stream key as well as per channel,
// and the per-key ceiling is the smaller one -- so handing every YouTube
// destination the same key makes them one ingestion source and caps the install
// there. internal/oauth cannot decide who gets their own stream, because a
// provider has never heard of a destination; this layer owns the table, so this
// layer decides, and these are the tests of that decision.
//
// NOT ONE ASSERTION HERE IS ABOUT A NUMBER. YouTube publishes neither ceiling.
// What is asserted is which destination is nominated, that the nomination does
// not move under a race, and -- the hazard that decides whether this can ship
// at all -- that a destination already publishing with a key keeps it.

// ytDest creates a YouTube destination with a key already in it, which is what
// every destination in a running install looks like.
func ytDest(t *testing.T, store *db.DB, acctID int64, name, key string) *db.Destination {
	t.Helper()
	d, err := store.CreateDestination(&db.Destination{
		Name: name, Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live2", StreamKey: key, AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination %q: %v", name, err)
	}
	return d
}

// The common case, unchanged: one YouTube destination reuses the channel's
// existing stream, because that is the key an operator's Studio-scheduled
// events are already bound to. A fresh key here would break a working setup for
// a feature they never asked for.
//
// MUTATION, internal/api/oauth_handlers.go, in needsOwnIngestStream: return
// true unconditionally. Observed: FAIL -- "DedicatedIngest = true for the only
// destination on this account".
func TestTheFirstYouTubeDestinationOnAnAccountKeepsTheSharedStream(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	d := ytDest(t, store, acct, "Main channel", "key-abc")

	opts := s.ingestOptions(d, time.Time{})
	if opts.DedicatedIngest {
		t.Error("DedicatedIngest = true for the only destination on this account; it " +
			"would be handed a newly minted key its scheduled events are not bound to")
	}
	// The two fields that make a refresh idempotent and a Studio listing
	// readable. Neither is a decision -- they are what the destination already
	// is -- but a provider cannot see either one without them.
	if opts.HeldKey != "key-abc" {
		t.Errorf("HeldKey = %q, want the key this destination is already publishing "+
			"with; without it a refresh is a rotation", opts.HeldKey)
	}
	if opts.IngestLabel != "Main channel" {
		t.Errorf("IngestLabel = %q, want the destination's own name", opts.IngestLabel)
	}
}

// The fix: every destination after the first asks for a stream of its own, so
// each is its own ingestion source rather than the fourth tenant of one.
//
// MUTATION, internal/api/oauth_handlers.go, in needsOwnIngestStream: return
// false unconditionally. Observed: FAIL on both later destinations -- "Second
// show: DedicatedIngest = false", which is today's defect exactly.
func TestEveryYouTubeDestinationAfterTheFirstAsksForItsOwnStream(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")

	first := ytDest(t, store, acct, "First show", "key-abc")
	second := ytDest(t, store, acct, "Second show", "")
	third := ytDest(t, store, acct, "Third show", "")

	for _, tc := range []struct {
		dest *db.Destination
		want bool
	}{
		{first, false},
		{second, true},
		{third, true},
	} {
		if got := s.ingestOptions(tc.dest, time.Time{}).DedicatedIngest; got != tc.want {
			t.Errorf("%s: DedicatedIngest = %v, want %v", tc.dest.Name, got, tc.want)
		}
	}
}

// HAZARD 2, FIRST HALF: "first" must not be a race.
//
// Two destinations created in the same minute must not both decide they are
// first, whichever of them asks and however many ask at once. The decision is
// keyed on the row id -- assigned by the database at insert, distinct, and
// already settled before either refresh starts -- so it is not a question of
// timing at all. A count of siblings, or a "has anyone fetched a key yet"
// check, would both answer yes twice here.
//
// MUTATION, internal/api/oauth_handlers.go, in needsOwnIngestStream: replace
// the `other.ID < dest.ID` comparison with `other.StreamKey != ""`, which is
// the plausible-looking "is somebody already using the shared stream" rule.
// Observed: FAIL -- "2 destinations were nominated to keep the shared stream",
// because neither of these has fetched a key yet.
func TestTwoYouTubeDestinationsCreatedTogetherCannotBothBeFirst(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")

	a := ytDest(t, store, acct, "Show A", "")
	b := ytDest(t, store, acct, "Show B", "")

	// Concurrently, and in no fixed order: this is two refresh-key presses
	// landing together, which is exactly how the operator reaches this state.
	// -race is watching.
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		flags = map[string]bool{}
	)
	for _, d := range []*db.Destination{a, b} {
		wg.Add(1)
		go func(d *db.Destination) {
			defer wg.Done()
			got := s.ingestOptions(d, time.Time{}).DedicatedIngest
			mu.Lock()
			flags[d.Name] = got
			mu.Unlock()
		}(d)
	}
	wg.Wait()

	shared := 0
	for _, dedicated := range flags {
		if !dedicated {
			shared++
		}
	}
	if shared != 1 {
		t.Fatalf("%d destinations were nominated to keep the shared stream, want exactly "+
			"1 (%+v); more than one is the concurrency defect re-created by the fix", shared, flags)
	}
	if flags[a.Name] {
		t.Errorf("the LOWER id was given a dedicated stream (%+v); the nomination must "+
			"be the stable one so it does not move between refreshes", flags)
	}
}

// HAZARD 2, SECOND HALF, AND THE ANSWER IS HONEST RATHER THAN FLATTERING.
//
// Deleting the first destination DOES change what needsOwnIngestStream answers
// for the second: the lowest surviving id becomes lowest, so the flag flips
// from true to false. What must not happen is the second destination's KEY
// moving, and the thing that prevents it is one layer down --
// IngestOptions.HeldKey is matched before DedicatedIngest is consulted, so a
// destination that already holds a stream keeps it whatever this layer decides.
// See TestYouTubeKeepsTheStreamADestinationAlreadyHolds in internal/oauth for
// the other half of this pair; neither test is worth much without the other.
//
// The promotion is harmless on its own terms too: the shared stream can only be
// claimed by the lowest id on the account, so a destination is only ever
// promoted onto it once the destination that was holding it is gone.
func TestDeletingTheFirstYouTubeDestinationDoesNotRotateTheSecondsKey(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")

	first := ytDest(t, store, acct, "First show", "key-abc")
	second := ytDest(t, store, acct, "Second show", "key-second")

	if s.ingestOptions(second, time.Time{}).DedicatedIngest != true {
		t.Fatal("the second destination did not start out dedicated; the rest of this " +
			"test is about what happens to that state")
	}
	if err := store.DeleteDestination(first.ID); err != nil {
		t.Fatalf("delete first: %v", err)
	}

	opts := s.ingestOptions(second, time.Time{})
	// THE ASSERTION THAT CARRIES THE HAZARD. Whatever the flag now says, the
	// key travels with the request, and internal/oauth returns the stream
	// behind it rather than the account's shared one.
	if opts.HeldKey != "key-second" {
		t.Fatalf("HeldKey = %q, want the key this destination is publishing with -- it is "+
			"the only thing keeping a promoted destination off the shared stream", opts.HeldKey)
	}
}

// The account is part of the question. Two Google accounts are two channels,
// each with its own shared stream, so a destination on one must not be
// nominated or demoted by a destination on the other.
//
// MUTATION, internal/api/oauth_handlers.go, in needsOwnIngestStream: drop the
// `*other.AccountID == *dest.AccountID` comparison. Observed: FAIL --
// "DedicatedIngest = true for the first destination on its own account".
func TestDestinationsOnAnotherAccountDoNotDecideThisOnesIngest(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	one := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	two := connectAccount(t, store, s.box, db.PlatformYouTube, "day-shift")

	ytDest(t, store, one, "Owl show", "key-abc")
	other := ytDest(t, store, two, "Day show", "")

	if s.ingestOptions(other, time.Time{}).DedicatedIngest {
		t.Error("DedicatedIngest = true for the first destination on its own account; " +
			"a second channel's shared stream is not contended for by the first channel's")
	}
}

// A destination with no connected account has a key somebody typed in. There is
// no shared stream to contend for and nothing to provision.
func TestAManuallyKeyedDestinationIsNotGivenADedicatedStream(t *testing.T) {
	s, _, store := testServerWith(t, Options{})
	d, err := store.CreateDestination(&db.Destination{
		Name: "Pasted key", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.example/live2", StreamKey: "typed-by-hand",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if s.ingestOptions(d, time.Time{}).DedicatedIngest {
		t.Error("DedicatedIngest = true for a destination with no connected account")
	}
}

// ---------------------------------------------------------------- end to end

// ytRefreshStub is a YouTube Data API standing in for the real one: one
// channel, one reusable stream, one dedicated stream already provisioned for a
// second destination, and a create that answers with a third.
//
// Its own stub rather than platformStub, which maps GET /channels onto Twitch's
// answer -- YouTube's Account reads the same path and would get a 204.
type ytRefreshStub struct {
	URL string

	mu      sync.Mutex
	creates []map[string]any
}

func newYouTubeRefreshStub(t *testing.T) *ytRefreshStub {
	t.Helper()
	stub := &ytRefreshStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/channels":
			// The ref connectAccount stored, so AccountFor agrees this token
			// belongs to the channel the destination was set up against.
			io.WriteString(w, `{"items":[{"id":"night-owl-ref","snippet":{"title":"Night Owl"}}]}`)
		case r.URL.Path == "/liveStreams" && r.Method == http.MethodGet:
			io.WriteString(w, `{"items":[
				{"id":"stream-77","snippet":{"title":"polyemesis"},
					"cdn":{"ingestionType":"rtmp","ingestionInfo":{
						"streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2"}}},
				{"id":"stream-88","snippet":{"title":"polyemesis - Second show"},
					"cdn":{"ingestionType":"rtmp","ingestionInfo":{
						"streamName":"key-second","ingestionAddress":"rtmp://c.example/live2"}}}]}`)
		case r.URL.Path == "/liveStreams" && r.Method == http.MethodPost:
			body := map[string]any{}
			if raw, err := io.ReadAll(r.Body); err == nil {
				_ = json.Unmarshal(raw, &body)
			}
			stub.mu.Lock()
			stub.creates = append(stub.creates, body)
			stub.mu.Unlock()
			io.WriteString(w, `{"id":"stream-99","snippet":{"title":"polyemesis - Third show"},
				"cdn":{"ingestionType":"rtmp","ingestionInfo":{
					"streamName":"key-new","ingestionAddress":"rtmp://e.example/live2"}}}`)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	stub.URL = srv.URL
	return stub
}

func (s *ytRefreshStub) created() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.creates))
	copy(out, s.creates)
	return out
}

// refreshKey presses the button the way the UI does and returns the stored
// destination that came back.
func refreshKey(t *testing.T, h http.Handler, sign func(*http.Request), id int64) *db.Destination {
	t.Helper()
	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(id, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh-key: status %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Destination *db.Destination `json:"destination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return resp.Destination
}

// HAZARD 1, THROUGH THE REAL HANDLER: somebody is streaming right now with a
// key pasted into OBS, and pressing refresh must not take it away from them.
//
// The two established destinations are the two installs this change could
// break: the one holding the channel's shared stream, and the one that has
// already been given a stream of its own. Both press refresh; neither key moves
// and nothing is provisioned. The third destination has never fetched a key,
// and it is the one that gets a new stream -- named after itself, because these
// are listed in YouTube Studio and five called "polyemesis" is a channel nobody
// can manage.
//
// It goes through the HTTP handler rather than calling ingestOptions, because
// the mapping being right proves nothing about whether handleRefreshKey still
// uses it: reverting that one call site to ingestOptionsFor leaves every test
// above green.
//
// MUTATION, internal/api/oauth_handlers.go, in handleRefreshKey: revert
// `s.ingestOptions(dest, ...)` to `ingestOptionsFor(dest, ...)`. Observed:
// FAIL -- "Third show: key = key-abc", the stream the first destination
// publishes with.
func TestRefreshKeyDoesNotMoveAnEstablishedYouTubeKeyAndGivesANewOneItsOwnStream(t *testing.T) {
	stub := newYouTubeRefreshStub(t)
	s, h, store, sign := engineServer(t, defaultTools(),
		Options{Providers: oauth.NewSet(oauth.WithBaseURL(stub.URL))})

	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	first := ytDest(t, store, acct, "First show", "key-abc")
	second := ytDest(t, store, acct, "Second show", "key-second")
	third := ytDest(t, store, acct, "Third show", "")

	for _, tc := range []struct {
		dest *db.Destination
		want string
	}{
		{first, "key-abc"},
		{second, "key-second"},
		{third, "key-new"},
	} {
		got := refreshKey(t, h, sign, tc.dest.ID)
		if got.StreamKey != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.dest.Name, got.StreamKey, tc.want)
		}
	}

	// Exactly one stream provisioned, for exactly the destination that had none.
	// A second create here is a key rotated under a running encoder.
	creates := stub.created()
	if len(creates) != 1 {
		t.Fatalf("%d streams were created on this channel, want 1: %+v", len(creates), creates)
	}
	snip, _ := creates[0]["snippet"].(map[string]any)
	if title, _ := snip["title"].(string); title != "polyemesis - Third show" {
		t.Errorf("created stream title = %q, want the destination's own name in it -- an "+
			"operator reading YouTube Studio has nothing else to tell them apart", title)
	}
}
