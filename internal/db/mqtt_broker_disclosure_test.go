package db

import (
	"strings"
	"testing"
)

// brokerPassword is the sentinel. Low-entropy and self-describing on purpose:
// gitleaks scans this PR's own commits, and what is under test is whether a
// string SURVIVES into a validation message, not how random it is.
const brokerPassword = "pw-not-a-real-secret"

// TestSettingsProblemsNeverEchoTheBrokerPassword is the internal/mqtt bug in its
// second copy, and the copy with the worse blast radius.
//
// MQTTSettings.problems() is character-for-character the same validation as
// mqtt.parseBroker, written separately, and it had the same two branches
// running ahead of the same credentials guard -- the guard whose message
// promises the password is "sealed and never logged":
//
//	mqtt://user:PW@            -> parses; u.User set, u.Host == "". The no-host
//	                              case fired first and formatted %q of the raw
//	                              BrokerURL.
//	mqtt://user:PW@ho st:1883  -> url.Parse fails. `%v` of the *url.Error
//	                              renders `parse "<the whole URL>": <reason>`.
//
// THIS ONE IS NOT A LOG LINE. These strings are joined by Settings.Validate and
// returned to the caller, which the API surfaces as a 400 response body on the
// settings save. The operator pastes a broker URL with a credential in it, and
// the password comes back in the HTTP response -- through any proxy, into any
// browser devtools tab, into any error-reporting client watching XHRs.
//
// It is a SEPARATE test from internal/mqtt's rather than a shared helper
// because it is a separate defect in a separate package reaching a separate
// sink. Fixing one has never fixed the other; that is how there came to be two.
func TestSettingsProblemsNeverEchoTheBrokerPassword(t *testing.T) {
	for _, tc := range []struct {
		name         string
		broker       string
		wantContains string
	}{
		{
			name:   "credential, and no host at all",
			broker: "mqtt://user:" + brokerPassword + "@",
			// The credentials complaint, not the no-host one. A URL carrying a
			// password is a credential problem first, and the operator has to
			// be told the password was exposed -- which "has no host" never did.
			wantContains: "credentials",
		},
		{
			name:   "credential, and a host url.Parse refuses",
			broker: "mqtt://user:" + brokerPassword + "@ho st:1883",
			// Only the fixed prefix is pinned; net/url owns the reason and the
			// reason's PRESENCE is asserted separately below. Reordering the
			// switch cases cannot reach this branch: `u` is nil here, so the
			// credentials case is unreachable by construction and the
			// *url.Error must be unwrapped.
			wantContains: "unparseable",
		},
		{
			name:         "credential on an otherwise perfectly good URL",
			broker:       "mqtt://user:" + brokerPassword + "@broker.example:1883",
			wantContains: "credentials",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := MQTTSettings{Enabled: true, BrokerURL: tc.broker, Prefix: "polyemesis", Instance: "studio"}
			probs := m.problems()
			if len(probs) == 0 {
				t.Fatalf("MQTTSettings{BrokerURL: %q}.problems() found nothing wrong. Every "+
					"URL in this table carries a password; accepting one stores the "+
					"credential in the settings blob and hands it to the publisher.", tc.broker)
			}
			joined := strings.Join(probs, "\n")

			if strings.Contains(joined, brokerPassword) {
				t.Errorf("problems() = %q\n\n"+
					"THAT CONTAINS THE BROKER PASSWORD, and these strings are not a log "+
					"line -- Settings.Validate joins them into the error the API returns as "+
					"a 400 RESPONSE BODY. The operator pastes a URL with a credential and "+
					"the credential comes back over the wire. The message this function "+
					"produces for a well-formed URL promises it is \"sealed and never "+
					"logged\"; these branches run before that promise.", probs)
			}
			if strings.Contains(joined, tc.broker) {
				t.Errorf("problems() = %q, which echoes the ENTIRE broker URL back. Echoing "+
					"the raw input is the mechanism the disclosure travelled on, and the "+
					"operator can already see what they typed.", probs)
			}
			if !strings.Contains(joined, tc.wantContains) {
				t.Errorf("problems() = %q, want a problem containing %q. Dropping the "+
					"diagnostic is not a fix for leaking the credential.", probs, tc.wantContains)
			}
		})
	}

	// The reason must survive the unwrap: an operator told only "unparseable"
	// has to find the bad character by bisection.
	t.Run("the unparseable reason survives being unwrapped", func(t *testing.T) {
		m := MQTTSettings{Enabled: true, BrokerURL: "mqtt://user:" + brokerPassword + "@ho st:1883"}
		joined := strings.Join(m.problems(), "\n")
		if !strings.Contains(joined, "unparseable: ") {
			t.Fatalf("problems() = %q, expected an `unparseable: <reason>` entry", joined)
		}
		if strings.HasSuffix(strings.TrimSpace(joined), "unparseable:") {
			t.Errorf("problems() = %q -- the *url.Error was discarded rather than unwrapped, "+
				"so the operator learns the URL is bad and not what is bad about it. The "+
				"reason carries no part of the input and is safe to show.", joined)
		}
	})

	// The real sink, end to end. problems() is the unit; Validate is what the
	// API actually calls and what builds the 400 body, and a future refactor
	// that reintroduces the echo one layer up would leave the rows above green.
	t.Run("nor does the error Validate returns", func(t *testing.T) {
		s := DefaultSettings()
		s.MQTT = MQTTSettings{
			Enabled: true, BrokerURL: "mqtt://user:" + brokerPassword + "@",
			Prefix: "polyemesis", Instance: "studio",
		}
		err := s.Validate()
		if err == nil {
			t.Fatal("Settings.Validate accepted a broker URL carrying a password")
		}
		if strings.Contains(err.Error(), brokerPassword) {
			t.Errorf("Settings.Validate() error = %q, which carries the broker password "+
				"into the 400 response body the settings save returns.", err)
		}
	})
}
