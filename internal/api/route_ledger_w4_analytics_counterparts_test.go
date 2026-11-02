package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Wave 4 #4's three analytics reads, each given a counterpart proof rather
// than a bare Want, so they add nothing to counterpartlessExcusedCeiling.
//
// A 403 observed today says nothing about what the route carries, which is
// the part that would leak if the 403 ever became a 200. Each proof plants the
// thing the route exists to hand out -- the operator's connected channel by
// name, the broadcast's title, a viewer's name among the top chatters --
// shows the admin receives it, and returns the read token's answer for the
// runner to scan.
//
// Ordinary English sentinels, for the reason chatFlagsSpoken gives: these are
// human words, and a high-entropy literal would be flagged by gitleaks for no
// benefit.
const (
	analyticsChannelSentinel = "the lighthouse keepers evening channel"
	analyticsTitleSentinel   = "the tide tables read aloud on a slow afternoon"
	analyticsChatterSentinel = "the ferryman who counts the gulls"
)

// analyticsProofFixture runs one stream through the real sampler: a Twitch
// account named for the channel sentinel, live with a title sentinel, and a
// chatter sentinel in the chat. The stream is left open (live) or closed.
func analyticsProofFixture(t *testing.T, closeIt bool) (*analyticsFixture, string, int64) {
	t.Helper()
	f := newAnalyticsFixture(t)
	n := 5
	f.fake.twitch["tok-proof"] = &n
	acct, err := f.store.UpsertPlatformAccount(f.s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: analyticsChannelSentinel, AccountRef: "proof-ref",
		AccessToken: "tok-proof", RefreshToken: "refresh-proof", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	d, err := f.store.CreateDestination(&db.Destination{
		Name: "proof", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://ingest.example/app", StreamKey: "key-proof", AccountID: &acct.ID,
	})
	if err != nil {
		t.Fatalf("destination: %v", err)
	}
	f.running[d.ID] = true
	f.fake.titleFor = map[string]string{"tok-proof": analyticsTitleSentinel}
	f.tick(t)
	if _, err := f.store.AppendChatMessages([]db.ChatMessage{{
		Platform: db.PlatformTwitch, Account: "proof-ref", MessageID: "p1", AuthorID: "ferry",
		AuthorName: analyticsChatterSentinel, Text: "ahoy", At: f.now,
	}}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	if closeIt {
		f.running = map[int64]bool{}
		f.now = f.now.Add(analytics.CloseGrace)
		f.tick(t)
	}
	read := createScopedToken(t, f.h, f.sign, "ledger-analytics", db.ScopeRead)
	streams, err := f.store.ListAnalyticsStreams(1)
	if err != nil || len(streams) != 1 {
		t.Fatalf("no stream recorded (%v)", err)
	}
	return f, read, streams[0].Stream.ID
}

func analyticsProof(t *testing.T, f *analyticsFixture, read, path, sentinel string) proofResult {
	t.Helper()
	code, high := rawBodyOf(t, f.h, f.sign, path)
	if code != http.StatusOK || !strings.Contains(high, sentinel) {
		t.Fatalf("the admin's %s answered %d without the planted %q, so the read principal would be "+
			"denied nothing: %s", path, code, sentinel, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, f.h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's %s answered %d, want 403", path, code)
	}
	return proofResult{
		Pattern:   patternDriven(t, f.h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{sentinel},
	}
}

// proveAnalyticsLive discharges GET /analytics/live: the live rows name the
// operator's connected channel.
func proveAnalyticsLive(t *testing.T) proofResult {
	f, read, _ := analyticsProofFixture(t, false)
	return analyticsProof(t, f, read, "/api/v1/analytics/live", analyticsChannelSentinel)
}

// proveAnalyticsStreams discharges GET /analytics/streams: the list carries
// each broadcast's title.
func proveAnalyticsStreams(t *testing.T) proofResult {
	f, read, _ := analyticsProofFixture(t, true)
	return analyticsProof(t, f, read, "/api/v1/analytics/streams", analyticsTitleSentinel)
}

// proveAnalyticsReport discharges GET /analytics/streams/{id}: the report
// names the stream's top chatters.
func proveAnalyticsReport(t *testing.T) proofResult {
	f, read, id := analyticsProofFixture(t, true)
	return analyticsProof(t, f, read, "/api/v1/analytics/streams/"+strconv.FormatInt(id, 10), analyticsChatterSentinel)
}
