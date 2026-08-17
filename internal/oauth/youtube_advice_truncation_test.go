package oauth

import (
	"strings"
	"testing"
)

/* THE SAME TRUNCATION BUG AS fbAdvice, STILL LIVE IN THE YOUTUBE PATH.
 *
 * metadata.go's statusError says it in as many words: "Body is TRUNCATED, for
 * display. Use payload() to parse it." and "Never use Body for parsing." The
 * comment exists because parsing the truncated body was a live defect once
 * already -- a realistic Meta refusal is 362 bytes, arrived as 303 characters of
 * invalid JSON, decodeGraphError returned false, and every code-specific branch
 * was skipped. fbAdvice was fixed to read payload().
 *
 * Both YouTube advice functions still read err.Error(), which returns exactly
 * the truncated Body:
 *
 *   ytBroadcastCreateAdvice  (youtube_schedule.go)
 *   broadcastWriteAdvice     (youtube_broadcast.go)
 *
 * MEASURED, NOT SUPPOSED. The fixture below is a realistic YouTube 403: 552
 * bytes, with "liveStreamingNotEnabled" at index 448 -- 148 characters past
 * snippet()'s 300-character cut. YouTube puts the machine-readable `reason`
 * inside error.errors[0], AFTER a long human-readable `message`, so the useful
 * token is reliably the part that gets cut. That ordering is why this is not a
 * rare edge: it is the normal shape of a Google API error.
 *
 * WHAT IT COSTS. An operator whose channel is not enabled for live streaming
 * gets the raw API error instead of the sentence written for them -- the one
 * that says YouTube must verify the account first and that it can take a day.
 * Same for the two quota refusals, which are the errors most likely to arrive
 * when somebody is trying to go live and least useful raw.
 *
 * The bug is invisible in the existing tests because their fixtures are short
 * enough to survive the cut, which is exactly how the Facebook one hid.
 */

// A realistic YouTube 403, with the reason code past the truncation point.
func ytLiveStreamingNotEnabledBody(t *testing.T) string {
	t.Helper()
	// A RAW STRING, NOT json.Marshal OF A MAP, AND THE REASON IS THE WHOLE POINT
	// OF THE FIXTURE. Go sorts map keys when marshalling, which puts "errors"
	// before "message" and drags the reason code back before the cut -- turning
	// a test of truncation into a test that passes for the wrong reason. The
	// straddle guard below caught exactly that on the first attempt. YouTube's
	// real ordering is message first, errors[] after, and that ordering is why
	// the useful token is reliably the part that gets cut.
	s := `{"error":{"code":403,"message":"The user is not enabled for live ` +
		`streaming. Live streaming is not available to this channel because the ` +
		`account has not been verified, or a previous strike restricts access. ` +
		`Verify the account and enable live streaming in YouTube Studio, then wait ` +
		`up to 24 hours for the change to take effect.","errors":[{"message":"The ` +
		`user is not enabled for live streaming.","domain":"youtube.liveBroadcast",` +
		`"reason":"liveStreamingNotEnabled","location":"body"}],` +
		`"status":"PERMISSION_DENIED"}}`

	// The fixture is only meaningful if it actually straddles the cut. Asserted
	// rather than assumed, so a future edit that shortens the message turns this
	// into a test of nothing without saying so.
	at := strings.Index(s, "liveStreamingNotEnabled")
	if at <= 300 {
		t.Fatalf("fixture no longer straddles snippet()'s cut: reason at %d of %d. "+
			"Lengthen the message; a fixture that survives truncation cannot detect "+
			"truncation.", at, len(s))
	}
	return s
}

// statusErrorFromWire builds the error the way a real request does: Body cut by
// snippet(), full carrying everything.
func statusErrorFromWire(status int, url, body string) *statusError {
	return &statusError{Status: status, URL: url, Body: snippet([]byte(body)), full: body}
}

func TestYouTubeCreateAdviceReadsTheWholeBodyNotTheDisplaySnippet(t *testing.T) {
	body := ytLiveStreamingNotEnabledBody(t)
	err := statusErrorFromWire(403, "https://www.googleapis.com/youtube/v3/liveBroadcasts", body)

	// The premise, stated as an assertion: the truncated form really does lose
	// the token. If this ever stops being true the test below proves nothing.
	if strings.Contains(err.Error(), "liveStreamingNotEnabled") {
		t.Fatal("the truncated error still carries the reason code, so this test is " +
			"no longer exercising truncation")
	}

	got := ytBroadcastCreateAdvice(err)
	if !strings.Contains(got.Error(), "YouTube verifies the account first") {
		t.Fatalf("ytBroadcastCreateAdvice gave no advice for a liveStreamingNotEnabled "+
			"refusal, because the reason code sits past snippet()'s 300-character cut "+
			"and it parses the DISPLAY body.\n\n"+
			"statusError says it outright: \"Body is TRUNCATED, for display. Use "+
			"payload() to parse it.\" fbAdvice was fixed for exactly this; the YouTube "+
			"path was not.\n\ngot: %s", got)
	}
}

// The quota refusals matter most: they are what an operator hits at the moment
// they are trying to go live, and the raw form tells them nothing about the 3
// concurrent / 10 per channel limits the advice explains.
func TestYouTubeQuotaAdviceSurvivesTruncation(t *testing.T) {
	for _, reason := range []string{
		"concurrentBroadcastsExceedLimit",
		"sharedIngestionBroadcastsExceedLimit",
	} {
		t.Run(reason, func(t *testing.T) {
			body := `{"error":{"code":403,"message":"The request cannot be completed ` +
				`because this channel has reached the maximum number of broadcasts that ` +
				`may be active or scheduled at one time. End or delete an existing ` +
				`broadcast before creating another one, and note that limits differ ` +
				`between broadcasts bound to a reusable stream key and those with their ` +
				`own ingestion.","errors":[{"message":"Too many broadcasts.",` +
				`"domain":"youtube.liveBroadcast","reason":"` + reason + `"}]}}`
			if at := strings.Index(body, reason); at <= 300 {
				t.Fatalf("fixture does not straddle the cut (reason at %d)", at)
			}

			err := statusErrorFromWire(403, "https://www.googleapis.com/youtube/v3/liveBroadcasts", body)
			got := ytBroadcastCreateAdvice(err)
			if got.Error() == err.Error() {
				t.Fatalf("no advice for %s: the refusal an operator is most likely to hit "+
					"while going live arrives raw, because the reason code is past the "+
					"display cut", reason)
			}
		})
	}
}

// broadcastWriteAdvice has the same defect and a different trigger: it matches
// "ModificationNotAllowed", which YouTube also reports as a reason code inside
// errors[], after the long message.
func TestYouTubeWriteAdviceReadsTheWholeBodyNotTheDisplaySnippet(t *testing.T) {
	body := `{"error":{"code":403,"message":"The request is not allowed because ` +
		`the broadcast has already started or completed. Properties that control ` +
		`the DVR window, automatic start and stop behaviour, and the monitor stream ` +
		`are fixed once a broadcast leaves the created or ready state, and cannot ` +
		`be changed for the remainder of its lifetime.","errors":[{"message":"DVR ` +
		`modification is not allowed.","domain":"youtube.liveBroadcast",` +
		`"reason":"enableDvrModificationNotAllowed"}]}}`
	if at := strings.Index(body, "ModificationNotAllowed"); at <= 300 {
		t.Fatalf("fixture does not straddle the cut (reason at %d)", at)
	}

	err := statusErrorFromWire(403, "https://www.googleapis.com/youtube/v3/liveBroadcasts", body)
	// EnableDvr set, so TouchesContentDetails is true and the advice is due.
	dvr := true
	got := broadcastWriteAdvice(err, "live", BroadcastSettings{EnableDvr: &dvr})
	if !strings.Contains(got.Error(), "freezes DVR") {
		t.Fatalf("broadcastWriteAdvice gave no advice for a ModificationNotAllowed "+
			"refusal whose reason code sits past the display cut.\ngot: %s", got)
	}
}
