package engine

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The per-destination signals the Stream Deck's destination key shows beside
// the state it already had: a restart storm, dropped frames, the connected
// account's health and the platform's own verdict on the stream. Each is
// derived here, once, from what the supervisor, the account book and the
// platform already say, so the key never has to infer one.

// TestARestartStormIsMoreThanFiveRestartsInTheWindow: the threshold is the
// Prometheus alert's, `> 5`, so five is not a storm and six is.
func TestARestartStormIsMoreThanFiveRestartsInTheWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status *supervisor.Status
		want   int
	}{
		{"no process", nil, 0},
		{"five restarts is not a storm", &supervisor.Status{State: supervisor.StateRunning, RecentRestarts: 5}, 0},
		{"six restarts while running is a storm", &supervisor.Status{State: supervisor.StateRunning, RecentRestarts: 6}, 6},
		{"a storm is reported between runs too", &supervisor.Status{State: supervisor.StateReconnecting, RecentRestarts: 9}, 9},
		{"a stopped destination is not storming, whatever its history", &supervisor.Status{State: supervisor.StateStopped, RecentRestarts: 9}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := destinationRestartStorm(tc.status); got != tc.want {
				t.Fatalf("destinationRestartStorm = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTheRestartStormMatchesThePrometheusAlert reads the alert rule the
// threshold copies, so the key and the alert cannot drift apart in silence.
func TestTheRestartStormMatchesThePrometheusAlert(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/prometheus/alerts.yml")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`increase\(polyemesis_destination_restarts_total\[(\d+)m\]\) > (\d+)`)
	m := re.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("PolyemesisDestinationRestartStorm's expression was not found in alerts.yml")
	}
	mins, _ := strconv.Atoi(m[1])
	above, _ := strconv.Atoi(m[2])
	if time.Duration(mins)*time.Minute != supervisor.RestartStormWindow || above != supervisor.RestartStormAbove {
		t.Fatalf("alerts.yml judges > %d in %dm; the key judges > %d in %s",
			above, mins, supervisor.RestartStormAbove, supervisor.RestartStormWindow)
	}
}

// TestDroppingFramesIsJudgedOnTheWindowNotTheRun: the table of what counts as
// dropping frames, and what does not.
func TestDroppingFramesIsJudgedOnTheWindowNotTheRun(t *testing.T) {
	running := func(dropped, frames int64, known bool) *supervisor.Status {
		return &supervisor.Status{State: supervisor.StateRunning, DroppedRecent: dropped, FramesRecent: frames, DropsKnown: known}
	}
	for _, tc := range []struct {
		name       string
		status     *supervisor.Status
		sourceLive time.Duration
		want       int64
	}{
		{"no process", nil, time.Minute, 0},
		{"no window yet", running(50, 300, false), time.Minute, 0},
		{"clean", running(0, 300, true), time.Minute, 0},
		{"a stray frame under one percent", running(2, 300, true), time.Minute, 0},
		{"three percent dropped", running(9, 300, true), time.Minute, 9},
		{"drops with no frames output", running(4, 0, true), time.Minute, 4},
		{"the source has only just returned", running(9, 300, true), supervisor.RateWindow / 2, 0},
		{"not running", &supervisor.Status{State: supervisor.StateReconnecting, DroppedRecent: 9, FramesRecent: 300, DropsKnown: true}, time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := destinationDropping(tc.status, true, tc.sourceLive); got != tc.want {
				t.Fatalf("destinationDropping = %d, want %d", got, tc.want)
			}
		})
	}
}

type stubAccountHealth map[int64]string

func (s stubAccountHealth) AccountHealth(id int64) string { return s[id] }

// TestAccountHealthIsReportedOnlyWhenItIsNotOK: an ok account is the normal
// case and says nothing; a destination with no account has nothing to say.
func TestAccountHealthIsReportedOnlyWhenItIsNotOK(t *testing.T) {
	e := &Engine{log: testLogger()}
	e.SetAccountHealth(stubAccountHealth{1: "ok", 2: "expired", 3: "expiring", 4: "refreshFailing"})
	acct := func(id int64) *db.Destination { return &db.Destination{ID: 9, AccountID: &id} }
	for id, want := range map[int64]string{1: "", 2: "expired", 3: "expiring", 4: "refreshFailing", 5: ""} {
		if got := e.accountHealth(acct(id)); got != want {
			t.Errorf("account %d: accountHealth = %q, want %q", id, got, want)
		}
	}
	if got := e.accountHealth(&db.Destination{ID: 9}); got != "" {
		t.Errorf("a destination with no account read %q", got)
	}
	if got := (&Engine{log: testLogger()}).accountHealth(acct(2)); got != "" {
		t.Errorf("an engine with no reader read %q", got)
	}
}

// TestTheAccountHealthOnTheStatusCarriesNoAccountData is the leak test for the
// field: GET /status is read-scope, so the destination's status may carry the
// state word and nothing else about the account -- not the refresh error, not a
// token, not the account's name.
func TestTheAccountHealthOnTheStatusCarriesNoAccountData(t *testing.T) {
	ds := DestStatus{ID: 9, AccountHealth: "refreshFailing"}
	raw, err := json.Marshal(ds)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["accountHealth"] != "refreshFailing" {
		t.Fatalf("accountHealth = %v, want the state word", m["accountHealth"])
	}
	for k := range m {
		if strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "lasterror") ||
			k == "accountId" || k == "account" {
			t.Fatalf("DestStatus carries %q, which is account data a read token must not see", k)
		}
	}
}

// stubPlatformHealth answers with a fixed reading after release is closed.
type stubPlatformHealth struct {
	calls   atomic.Int32
	release chan struct{}
	reading PlatformHealth
}

func (s *stubPlatformHealth) StreamHealth(ctx context.Context, destID int64) (PlatformHealth, bool, error) {
	s.calls.Add(1)
	<-s.release
	return s.reading, true, nil
}

func liveYouTube() (*db.Destination, *supervisor.Status) {
	acct := int64(5)
	return &db.Destination{ID: 9, Platform: db.PlatformYouTube, StreamKey: "k", AccountID: &acct},
		&supervisor.Status{State: supervisor.StateRunning, UptimeSec: 30}
}

// TestPlatformHealthNeverBlocksStatusAndAsksOnceAtATime: the reading is a
// platform API call against a daily quota, made from Status, which runs per
// WebSocket push. It must return at once, and ask once per platformHealthEvery.
func TestPlatformHealthNeverBlocksStatusAndAsksOnceAtATime(t *testing.T) {
	k := &stubPlatformHealth{release: make(chan struct{}), reading: PlatformHealth{Platform: db.PlatformYouTube, Status: "bad"}}
	e := &Engine{log: testLogger()}
	e.SetPlatformHealth(k)
	row, proc := liveYouTube()
	for i := 0; i < 50; i++ {
		if e.platformHealth(row, proc) != nil {
			t.Fatal("reported a platform reading before any answer arrived")
		}
	}
	close(k.release)
	waitUntil(t, func() bool { return e.platformHealth(row, proc) != nil }, "the platform's answer")
	if got := k.calls.Load(); got != 1 {
		t.Fatalf("the platform was asked %d times, want 1", got)
	}
}

// TestOnlyAPoorReadingIsReported: good and ok say nothing, and neither does
// noData -- YouTube reports it for a stream it has not started judging, which
// is not a fault of the stream. bad, or an error-severity configuration issue,
// is reported.
func TestOnlyAPoorReadingIsReported(t *testing.T) {
	for _, tc := range []struct {
		reading PlatformHealth
		want    bool
	}{
		{PlatformHealth{Status: "good"}, false},
		{PlatformHealth{Status: "ok"}, false},
		{PlatformHealth{Status: "noData"}, false},
		{PlatformHealth{Status: "bad"}, true},
		{PlatformHealth{Status: "ok", Issues: []string{"audioBitrateMismatch"}}, true},
	} {
		k := &stubPlatformHealth{release: make(chan struct{}), reading: tc.reading}
		close(k.release)
		e := &Engine{log: testLogger()}
		e.SetPlatformHealth(k)
		row, proc := liveYouTube()
		e.platformHealth(row, proc)
		waitUntil(t, func() bool {
			e.healthMu.Lock()
			defer e.healthMu.Unlock()
			st := e.healthChecks[row.ID]
			return st != nil && !st.inflight && !st.at.IsZero()
		}, "the answer")
		if got := e.platformHealth(row, proc) != nil; got != tc.want {
			t.Errorf("%+v: reported = %v, want %v", tc.reading, got, tc.want)
		}
	}
}

// TestPlatformHealthIsAskedOnlyForALiveYouTubeDestinationWithAnAccount: no
// account means no token, Twitch has no stream-health API at all, and a
// destination not delivering has nothing for YouTube to judge. None of these
// spends quota, and a reading left from an earlier run is not carried onto a
// destination that has stopped.
func TestPlatformHealthIsAskedOnlyForALiveYouTubeDestinationWithAnAccount(t *testing.T) {
	k := &stubPlatformHealth{release: make(chan struct{}), reading: PlatformHealth{Status: "bad"}}
	close(k.release)
	e := &Engine{log: testLogger()}
	e.SetPlatformHealth(k)

	row, proc := liveYouTube()
	noAcct := *row
	noAcct.AccountID = nil
	twitch := *row
	twitch.Platform = db.PlatformTwitch
	waiting := &supervisor.Status{State: supervisor.StateRunning}
	e.platformHealth(&noAcct, proc)
	e.platformHealth(&twitch, proc)
	e.platformHealth(row, waiting)
	e.platformHealth(row, nil)
	time.Sleep(20 * time.Millisecond)
	if n := k.calls.Load(); n != 0 {
		t.Fatalf("the platform was asked %d times for destinations it cannot judge", n)
	}

	waitUntil(t, func() bool { return e.platformHealth(row, proc) != nil }, "the reading for the live destination")
	if e.platformHealth(row, &supervisor.Status{State: supervisor.StateStopped}) != nil {
		t.Fatal("a reading from the last run was reported on a stopped destination")
	}
	e.healthMu.Lock()
	_, kept := e.healthChecks[row.ID]
	e.healthMu.Unlock()
	if kept {
		t.Fatal("the stopped destination's reading was kept, so the next run would start with a stale verdict")
	}
}
