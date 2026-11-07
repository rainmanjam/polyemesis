package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// How X's refusals reach the operator. X answers in two ways: a non-2xx status with RFC 7807 problem objects, and a
// 2xx whose body carries an "errors" array beside (or instead of) the data. Both must come out as X's own words,
// and neither may carry the raw body, because a body can hold a stream key and these errors are logged.
//
// PROVISIONAL like the rest of the X provider: the shapes are from X's served OpenAPI spec, X is gated off, and no
// live call has run.

const xGateProblem = `{"errors":[{"title":"Forbidden","detail":"Livestream access is not enabled for this app","type":"about:blank"}]}`

// A 2xx with no data and an errors array is a refusal in X's words, on every read that can get one.
func TestXReadsThatAnswerOKWithOnlyErrorsAreRefusalsInXsWords(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, xGateProblem)
	})
	ctx := context.Background()

	if _, err := x.Broadcasts(ctx, "client", "tok"); err == nil ||
		!strings.Contains(err.Error(), "would not list broadcasts: Forbidden: Livestream access is not enabled") {
		t.Errorf("Broadcasts err = %v, want X's problem in its words", err)
	}
	if _, err := x.BroadcastByID(ctx, "client", "tok", "1ynJOZVeajOJR"); err == nil ||
		!strings.Contains(err.Error(), "would not read broadcast 1ynJOZVeajOJR: Forbidden") {
		t.Errorf("BroadcastByID err = %v, want X's problem in its words", err)
	}
	if _, err := x.Account(ctx, "client", "tok"); err == nil ||
		!strings.Contains(err.Error(), "would not say which account this token belongs to: Forbidden") {
		t.Errorf("Account err = %v, want X's problem in its words", err)
	}
}

// A non-2xx is reported with its status and X's problem text, and a body that is not JSON contributes nothing: the
// status alone is reported rather than a snippet of whatever came back.
func TestXNonOKAnswersCarryTheStatusAndXsWordsButNeverARawBody(t *testing.T) {
	leaky := "rtmp_stream_key=" + xSecret("abcd", "efgh", "ijkl")
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"problem body", http.StatusForbidden, xGateProblem, "X returned 403: Forbidden: Livestream access is not enabled"},
		{"top-level problem", http.StatusTooManyRequests, `{"title":"Too Many Requests","detail":"slow down"}`, "X returned 429: Too Many Requests: slow down"},
		{"non-JSON body", http.StatusBadGateway, "<html>" + leaky + "</html>", "X returned 502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := x.Broadcasts(context.Background(), "client", "tok")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), leaky) || strings.Contains(err.Error(), "<html>") {
				t.Fatalf("the error carried the raw body: %v", err)
			}
		})
	}
}

// users/me with neither a user nor an error is still a failure: an account keyed by an empty id would collide with
// every other such account.
func TestXAccountWithNoUserIDIsRefused(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{}}`)
	})
	if _, err := x.Account(context.Background(), "client", "tok"); err == nil ||
		!strings.Contains(err.Error(), "no user id") {
		t.Fatalf("Account err = %v, want the missing user id named", err)
	}
}

// An errors array with no message in it still says something, rather than an empty refusal.
func TestXProblemsWithNoWordsStillSaySomething(t *testing.T) {
	if got := xProblems([]xProblem{{Status: 400}}); got != "X reported an error with no message" {
		t.Fatalf("xProblems = %q", got)
	}
	if got := (&xStatusError{Status: 500}).Error(); got != "X returned 500" {
		t.Fatalf("xStatusError = %q", got)
	}
}

// A go-live that took the source live but came back with X's errors keeps them as a warning: the broadcast is on air, so
// it is not a failure, and the operator still reads what X said went wrong with it. (X's Go Live answer has no
// tweet_error; a missing announcement shows as a missing tweet_id on a later read.)
func TestXGoLiveKeepsXsErrorsAsAWarningWhenTheBroadcastIsLive(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated) // a new broadcast; a 200 would be the source's existing one
		_, _ = io.WriteString(w, `{"data":{"id":"1ynJOZVeajOJR","state":"RUNNING"},`+
			`"errors":[{"title":"Partial","detail":"the title was not applied"}]}`)
	})
	res, err := x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{Title: "Sunday"})
	if err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "the title was not applied") {
		t.Fatalf("Warnings = %v, want X's error as the one warning", res.Warnings)
	}
}
