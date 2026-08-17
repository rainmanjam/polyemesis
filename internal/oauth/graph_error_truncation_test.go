package oauth

import (
	"strings"
	"testing"
)

// A REAL META ERROR IS LONGER THAN THE BUFFER THAT WAS PARSING IT, AND THAT
// SILENTLY DISABLED EVERY PIECE OF GRAPH-CODE-SPECIFIC ADVICE.
//
// statusError.Body is set from snippet(), which cuts at 300 characters so that
// a platform answering with an HTML error page cannot dump the page into a log
// line. fbAdvice then read the Graph error code out of that same field. Meta's
// refusals do not fit: the message text alone runs past 250 characters before
// the type, code, error_subcode and fbtrace_id are appended, so the body
// arrived as 303 characters of invalid JSON, decodeGraphError returned false,
// and the advice keyed on the code was skipped.
//
// It was invisible for the reason these things usually are: every fixture in
// the test suite was hand-written short enough to survive the truncation. The
// tests proved the parser works on bodies Meta does not send.
//
// The fix separates the two jobs -- Body truncates for display, payload()
// returns what was actually received for parsing -- and this test pins it with
// a body of realistic length rather than a convenient one.
func TestGraphAdviceSurvivesARealisticallyLongMetaError(t *testing.T) {
	// Shaped like Graph's own: a full sentence of message, the documentation
	// URL Meta appends, and the four envelope fields it always includes.
	const realistic = `{"error":{"message":"(#100) Unsupported post request. Object with ID '1234567890' ` +
		`does not exist, cannot be loaded due to missing permissions, or does not support this ` +
		`operation. Please read the Graph API documentation at ` +
		`https://developers.facebook.com/docs/graph-api","type":"OAuthException","code":100,` +
		`"error_subcode":33,"fbtrace_id":"AbCdEfGhIjKlMnOpQrStUv"}}`

	if len(realistic) <= 300 {
		t.Fatalf("this fixture is %d bytes and no longer exercises the truncation it exists for; "+
			"a shorter body would pass whether or not the bug is present", len(realistic))
	}

	// Built the way requestJSON builds it: Body truncated, full retained.
	se := &statusError{Status: 400, URL: "https://graph.example/me/live_videos",
		Body: snippet([]byte(realistic)), full: realistic}

	if !strings.HasSuffix(se.Body, "...") {
		t.Fatal("Body was not truncated, so this test is not exercising the bug")
	}

	// DRIVEN THROUGH fbAdvice, NOT THROUGH decodeGraphError. An earlier version
	// of this test called the decoder directly and passed while the call site
	// in fbAdvice was reverted to the truncated field -- it proved the parser
	// works on a string, which was never in doubt. What matters is that the
	// ADVICE reaches the operator, so the assertion has to run the function
	// that produces it.
	advised := fbAdvice(se, "create the broadcast", []string{"publish_video"})
	if advised == se {
		t.Fatalf("fbAdvice returned the error untouched for a realistic %d-byte Meta refusal. "+
			"That is what happens when the body it parses has been truncated to %d: the code "+
			"is unreadable, every code-specific branch is skipped, and the operator sees the "+
			"raw truncated body instead of advice.", len(realistic), len(se.Body))
	}
	if !strings.Contains(advised.Error(), "Facebook") {
		t.Errorf("advice does not read as Facebook's refusal: %q", advised.Error())
	}
}

// The same property for the token family, because 190 is the code whose advice
// an operator most needs and most often earns -- and its message is long.
func TestExpiredTokenAdviceSurvivesALongBody(t *testing.T) {
	const long = `{"error":{"message":"Error validating access token: Session has expired on ` +
		`Tuesday, 12-Aug-25 03:00:00 PDT. The current time is Saturday, 16-Aug-25 09:14:22 PDT. ` +
		`Please log in again to continue using the application.","type":"OAuthException",` +
		`"code":190,"error_subcode":463,"fbtrace_id":"A1b2C3d4E5f6G7h8I9j0Kl"}}`
	if len(long) <= 300 {
		t.Fatalf("fixture is %d bytes and does not exercise truncation", len(long))
	}
	se := &statusError{Status: 400, URL: "u", Body: snippet([]byte(long)), full: long}
	advised := fbAdvice(se, "push the title", []string{"publish_video"})
	if advised == se {
		t.Fatal("the expired-token advice did not fire for a realistic 190 body; " +
			"an operator whose token expired would be shown raw JSON and no instruction")
	}
}

// The other half of the contract: truncation still happens where it was meant
// to. Removing it to fix the parse would trade a silent bug for a loud one --
// an HTML error page in a log line.
func TestTheDisplayBodyIsStillTruncated(t *testing.T) {
	huge := strings.Repeat("A", 5000)
	se := &statusError{Status: 500, URL: "u", Body: snippet([]byte(huge)), full: huge}

	if len(se.Body) > 400 {
		t.Errorf("Body is %d characters; snippet() is what keeps a platform's HTML error "+
			"page out of a log line and it must still apply", len(se.Body))
	}
	if !strings.Contains(se.Error(), "...") {
		t.Error("Error() should render the truncated body, not the whole response")
	}
	if len(se.payload()) != len(huge) {
		t.Errorf("payload() = %d characters, want the whole %d: parsing needs what was "+
			"received, which is the entire point of the split", len(se.payload()), len(huge))
	}
}

// A statusError built without full -- by a test, or by a call site that predates
// the field -- must still parse rather than returning nothing at all.
func TestAnErrorWithNoCapturedBodyFallsBackToTheTruncatedOne(t *testing.T) {
	se := &statusError{Status: 400, URL: "u", Body: `{"error":{"code":190,"message":"expired"}}`}
	ge, ok := decodeGraphError(se.payload())
	if !ok || ge.Code != 190 {
		t.Fatalf("fallback failed: ok=%v code=%d. Every fixture in this package builds "+
			"statusError by hand, so a payload() that ignored Body would break them all.", ok, ge.Code)
	}
}

// The eligibility note is built on the same decode and inherited the same bug:
// it was added on a branch that forked before the fix, so the merge reintroduced
// a truncated parse on a second call site. A realistic refusal is the only
// fixture that catches it -- the one shipped with the feature was hand-written
// at 74 bytes, short enough to survive the cut that was the whole problem.
func TestTheEligibilityNoteReachesARealisticRefusal(t *testing.T) {
	const realistic = `{"error":{"message":"(#100) Unsupported post request. Object with ID '1234567890' ` +
		`does not exist, cannot be loaded due to missing permissions, or does not support this ` +
		`operation. Please read the Graph API documentation at ` +
		`https://developers.facebook.com/docs/graph-api","type":"OAuthException","code":100,` +
		`"error_subcode":33,"fbtrace_id":"AbCdEfGhIjKlMnOpQrStUv"}}`
	if len(realistic) <= 300 {
		t.Fatalf("fixture is %d bytes and does not exercise truncation", len(realistic))
	}
	se := &statusError{Status: 400, URL: "u", Body: snippet([]byte(realistic)), full: realistic}

	got := fbCreateAdvice(se, "create the broadcast", []string{"publish_video"}).Error()
	if !strings.Contains(got, "60 days") || !strings.Contains(got, "100 followers") {
		t.Errorf("the eligibility note did not reach a realistic refusal, which is the only "+
			"kind Meta sends. An operator with a 40-day-old account gets raw truncated JSON "+
			"and no hint that account age is why.\ngot: %s", got)
	}
}
