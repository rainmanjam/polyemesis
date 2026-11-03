package publish

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

func xEnv(t *testing.T, tweetStatus int) (*env, *counting) {
	e := newEnv(t)
	srv, cnt := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2/media/upload/initialize":
			fmt.Fprint(w, `{"data":{"id":"m1"}}`)
		case r.URL.Path == "/2/media/upload/m1/append":
			fmt.Fprint(w, `{"data":{"expires_at":1}}`)
		case r.URL.Path == "/2/media/upload/m1/finalize":
			fmt.Fprint(w, `{"data":{"id":"m1"}}`)
		case r.URL.Path == "/2/media/upload":
			fmt.Fprint(w, `{"data":{"processing_info":{"state":"succeeded"}}}`)
		case r.URL.Path == "/2/tweets":
			if tweetStatus != 200 {
				w.WriteHeader(tweetStatus)
				return
			}
			fmt.Fprint(w, `{"data":{"id":"tw1"}}`)
		default:
			http.NotFound(w, r)
		}
	})
	e.ad[PlatformX] = &X{Base: srv.URL, PollEvery: time.Millisecond}
	e.cred.Scopes = "tweet.write media.write users.read"
	return e, cnt
}

// X IS GATED. With the switch closed, nothing is sent -- not even a token fetch.
func TestXPublishingIsOffByDefaultAndSendsNothing(t *testing.T) {
	t.Setenv(XPublishEnv, "")
	e, cnt := xEnv(t, 200)
	e.gates = GatesFromEnv()
	p, params := e.claim(PlatformX)
	_, err := e.run(params, 1)
	if !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
	if cnt.n() != 0 || e.tokens != 0 {
		t.Fatalf("a gated platform made %d requests and %d token fetches", cnt.n(), e.tokens)
	}
	if row := e.row(p.ID); row.FailCause != string(CauseGated) {
		t.Fatalf("cause = %q", row.FailCause)
	}
}

func TestTheXGateReadsItsOwnSwitch(t *testing.T) {
	t.Setenv(XPublishEnv, "")
	if GatesFromEnv().XPublish {
		t.Fatal("empty env opened the gate")
	}
	t.Setenv(XPublishEnv, "0")
	if GatesFromEnv().XPublish {
		t.Fatal("0 opened the gate")
	}
	// The Livestream switch is a different thing and must not open it.
	t.Setenv(XPublishEnv, "")
	t.Setenv("POLYEMESIS_X_LIVESTREAM", "1")
	if GatesFromEnv().XPublish {
		t.Fatal("the Livestream switch opened publishing")
	}
	t.Setenv(XPublishEnv, "true")
	if !GatesFromEnv().XPublish {
		t.Fatal("true did not open the gate")
	}
}

func TestXPublishesOnceWhenSwitchedOn(t *testing.T) {
	e, cnt := xEnv(t, 200)
	e.gates = Gates{XPublish: true}
	p, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if row := e.row(p.ID); row.State != db.PublicationPosted || row.PlatformPostID != "tw1" {
		t.Fatalf("row = %+v", row)
	}
	if cnt.count("POST /2/tweets") != 1 {
		t.Fatalf("tweets = %d", cnt.count("POST /2/tweets"))
	}
}

// The post call cannot be asked about afterwards, so a retry after an
// unconfirmed attempt must not repeat it.
func TestXNeverRepeatsAnUnconfirmedPost(t *testing.T) {
	e, cnt := xEnv(t, http.StatusBadGateway)
	e.gates = Gates{XPublish: true}
	p, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("an unconfirmed X post must stop, got %v", err)
	}
	if row := e.row(p.ID); row.FailCause != string(CauseOutcomeUnknown) {
		t.Fatalf("cause = %q", row.FailCause)
	}
	// Re-armed by a click and run again: the stamp from the first attempt is
	// cleared by the re-arm (a human is acknowledging), but within ONE arming a
	// second run must not post again.
	if cnt.count("POST /2/tweets") != 1 {
		t.Fatalf("tweets = %d", cnt.count("POST /2/tweets"))
	}
}

func TestXRetryWithinOneArmingDoesNotPostAgain(t *testing.T) {
	e, cnt := xEnv(t, 200)
	e.gates = Gates{XPublish: true}
	p, params := e.claim(PlatformX)
	// An earlier attempt already stamped intent and then lost its answer.
	if _, err := e.d.StampPublicationFinalizing(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(params, 2); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a permanent outcome-unknown", err)
	}
	if cnt.count("POST /2/tweets") != 0 {
		t.Fatalf("a retry after a stamped attempt posted again")
	}
	if row := e.row(p.ID); row.FailCause != string(CauseOutcomeUnknown) {
		t.Fatalf("cause = %q", row.FailCause)
	}
}

func TestXNeedsPostingScopesTheSignInGrantLacks(t *testing.T) {
	e, cnt := xEnv(t, 200)
	e.gates = Gates{XPublish: true}
	e.cred.Scopes = "broadcast.read broadcast.write offline.access users.read tweet.read"
	_, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) || cnt.n() != 0 {
		t.Fatalf("err = %v requests = %d", err, cnt.n())
	}
}

func TestNormalizeRules(t *testing.T) {
	m, err := Normalize(PlatformYouTube, Meta{Title: " Hi ", Tags: []string{"#a", "a", " b c ", ""}})
	if err != nil || m.Title != "Hi" || len(m.Tags) != 2 || m.Tags[1] != "bc" || m.Privacy != PrivacyPrivate {
		t.Fatalf("Normalize = %+v %v", m, err)
	}
	if _, err := Normalize(PlatformYouTube, Meta{}); err == nil {
		t.Error("an empty YouTube title was accepted")
	}
	long := make([]byte, 101)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Normalize(PlatformYouTube, Meta{Title: string(long)}); err == nil {
		t.Error("a 101-character title was accepted")
	}
	if _, err := Normalize(PlatformYouTube, Meta{Title: "x", Privacy: "everyone"}); err == nil {
		t.Error("an unknown privacy was accepted")
	}
	if _, err := Normalize("myspace", Meta{Title: "x"}); err == nil {
		t.Error("an unknown platform was accepted")
	}
}
