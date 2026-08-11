package mqtt

import (
	"net/url"
	"strings"
	"testing"
)

// brokerPassword is the sentinel every assertion in this file hunts for.
//
// Deliberately low-entropy and self-describing: gitleaks scans this PR's own
// commits, and a realistic-looking credential in a table literal is
// indistinguishable to a scanner from a real one that leaked. What is under
// test is whether a string SURVIVES into an error message, and that question
// does not care how random the string is.
const brokerPassword = "pw-not-a-real-secret"

// TestBrokerURLErrorsNeverEchoThePassword pins the promise parseBroker's own
// error message makes.
//
// The credentials branch tells the operator to move the password into its own
// field "so the password is sealed and NEVER LOGGED". That sentence was true
// only for a broker URL that was otherwise well formed. Two branches run BEFORE
// it, both reachable with a credential in the URL, and both echoed the URL:
//
//	mqtt://user:PW@              -> parses fine, u.User set, u.Host == ""
//	                                the no-host branch fired first and formatted
//	                                %q of the RAW URL.
//	mqtt://user:PW@ho st:1883    -> url.Parse fails. The error was wrapped with
//	                                %w, and url.Error.Error() renders as
//	                                `parse "<the whole URL>": <reason>`.
//
// Both land in Connect's return, which cmd/polyemesis logs at Error level
// ("cannot start the MQTT publisher", "err", err) on every settings poll --
// so a bad paste writes the password to the log once every five seconds.
//
// THE SECOND ROW IS THE ONE THAT MATTERS FOR THE SHAPE OF THE FIX. Reordering
// the checks cures the first row and does nothing for the second: in the parse
// -failure branch `u` is nil, so there is no u.User to consult and the
// credentials guard is unreachable by construction. The url.Error has to be
// unwrapped, or the raw string dropped. A reorder-only fix passes row one and
// fails here.
func TestBrokerURLErrorsNeverEchoThePassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		// wantContains keeps this from being satisfiable by returning a blank
		// or generic error. Dropping the diagnostic is not a fix for leaking
		// it: the operator still has to be told what is wrong.
		wantContains string
	}{
		{
			name: "credential, and no host at all",
			raw:  "mqtt://user:" + brokerPassword + "@",
			// The credentials guard, not the no-host one: a URL carrying a
			// password is a credential problem first. The operator must be told
			// the password was exposed, which the no-host message never said.
			wantContains: "credentials",
		},
		{
			name: "credential, and a host url.Parse refuses",
			raw:  "mqtt://user:" + brokerPassword + "@ho st:1883",
			// Only the fixed prefix is pinned. The reason itself comes from
			// net/url ("invalid character \" \" in host name") and is asserted
			// to be PRESENT below rather than matched, so a stdlib rewording
			// does not fail this test while a dropped reason does.
			wantContains: "unparseable",
		},
		{
			name: "credential on an otherwise perfectly good URL",
			raw:  "mqtt://user:" + brokerPassword + "@broker.example:1883",
			// The branch that was always correct. It is here as the control: if
			// a future edit deletes the u.User guard, this row is what notices.
			wantContains: "credentials",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := parseBroker(tc.raw)
			if err == nil {
				t.Fatalf("parseBroker(%q) returned no error (u = %v). Every input in this "+
					"table carries a password in the URL and must be refused; accepting one "+
					"means the credential reaches autopaho and every connect/disconnect log "+
					"line built from it.", tc.raw, u)
			}
			got := err.Error()

			if strings.Contains(got, brokerPassword) {
				t.Errorf("parseBroker(%q) error = %q\n\n"+
					"THAT STRING CONTAINS THE BROKER PASSWORD. It is returned through "+
					"mqtt.Connect to mqttRunner.connect, which logs it at Error level on "+
					"every settings poll -- five seconds apart, forever, until the operator "+
					"notices. The message this function returns for a well-formed URL "+
					"promises the password is \"sealed and never logged\"; this branch runs "+
					"before that promise and breaks it.", tc.raw, got)
			}
			// Broader than the sentinel: a future shape may carry a credential
			// this test did not think to name. Nothing about a URL the operator
			// just typed needs to be read back to them.
			if strings.Contains(got, tc.raw) {
				t.Errorf("parseBroker(%q) error = %q, which echoes the ENTIRE URL back. "+
					"Even where today's userinfo is caught, echoing the raw input is the "+
					"mechanism the disclosure travelled on -- the operator can see what "+
					"they typed, and the URL adds nothing the reason does not.", tc.raw, got)
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("parseBroker(%q) error = %q, want it to contain %q. Suppressing "+
					"the diagnostic is not a fix for leaking the credential; the operator "+
					"still has to learn what is wrong with the URL.", tc.raw, got, tc.wantContains)
			}
			if u != nil {
				t.Errorf("parseBroker(%q) returned a non-nil URL %v alongside its error; "+
					"a caller that ignores the error would dial it", tc.raw, u)
			}
		})
	}

	// The unparseable row's REASON must survive the unwrap. Unwrapping the
	// url.Error is what drops the URL; dropping the whole error with it would
	// leave the operator a bare "unparseable" and no clue which character.
	t.Run("the unparseable reason survives being unwrapped", func(t *testing.T) {
		_, err := parseBroker("mqtt://user:" + brokerPassword + "@ho st:1883")
		if err == nil {
			t.Fatal("parseBroker accepted a URL with a space in the host")
		}
		const bare = "mqtt broker URL is unparseable"
		if err.Error() == bare {
			t.Errorf("parseBroker error = %q -- the url.Error was discarded rather than "+
				"unwrapped, so the operator is told the URL is bad and not what is bad "+
				"about it. The reason (net/url's, e.g. `invalid character \" \" in host "+
				"name`) carries no part of the input and is safe to show.", err)
		}
	})
}

// TestRedactURLDropsEverythingBelowTheAuthority pins redactURL's contract.
//
// HONEST SCOPE, because this is the target the brief named and the brief was
// wrong about it. redactURL feeds three slog callbacks in Connect and nothing
// else -- it never reaches an MQTT topic (the topic payloads are the whitelist
// structs in state.go, pinned by TestPayloadsCarryOnlyApprovedFields). And it
// cannot receive userinfo today, because parseBroker refuses u.User before
// those callbacks are ever registered. So this is NOT a live disclosure and is
// not presented as one.
//
// It is still worth one test. The function's whole reason to exist is that
// `*url.URL` in a log line is a credential waiting for a caller, and the guard
// that keeps its input clean lives in a DIFFERENT function that this file has
// just finished reordering. Pinning the contract here means a future relaxation
// of parseBroker -- accepting userinfo and moving it into Config.Username, say,
// which is the natural next request -- does not silently make the log lines
// unsafe. The property is the cheap one: scheme and host, nothing else.
func TestRedactURLDropsEverythingBelowTheAuthority(t *testing.T) {
	u := &url.URL{
		Scheme:   "mqtts",
		User:     url.UserPassword("u", brokerPassword),
		Host:     "b.example:8883",
		Path:     "/x",
		RawQuery: "token=T",
	}
	const want = "mqtts://b.example:8883"

	got := redactURL(u)
	if got != want {
		t.Errorf("redactURL() = %q, want %q", got, want)
	}
	if strings.Contains(got, brokerPassword) {
		t.Errorf("redactURL() = %q, which carries the userinfo password into a log line. "+
			"This function exists precisely so that `broker` in `mqtt connected`, `mqtt "+
			"connection lost` and `mqtt connect failed` is a scheme and a host and nothing "+
			"else; if it renders the URL it is doing the opposite of its job.", got)
	}
}
