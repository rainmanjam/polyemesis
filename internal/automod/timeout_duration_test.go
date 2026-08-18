package automod

import (
	"testing"
	"time"
)

/* A TIMEOUT WITH NO DURATION MUST NOT BECOME A PERMANENT BAN.
 *
 * internal/chat/automod.go dispatches ActionTimeout by calling Hub.Ban with
 * time.Duration(TimeoutSeconds)*time.Second, and the case immediately below it
 * says what a zero means: "Zero duration is a permanent ban, same as the
 * moderator UI sends."
 *
 * TimeoutSeconds is `json:",omitempty"` and nothing validated it. So an
 * operator who wrote a rule saying `timeout` and left the duration out got a
 * viewer PERMANENTLY BANNED from their channel, and the log recorded the action
 * as having succeeded.
 *
 * That is worse than the sibling bug in the capability table, and the contrast
 * is the point: the capability table fails SAFE -- the operator believes a
 * channel is protected and nothing happens. This fails DANGEROUS -- something
 * harsher than what was asked for happens, to a third party, irreversibly
 * enough that the operator would have to notice and undo it by hand.
 *
 * TWO HALVES, DELIBERATELY. Refusing at save stops new rules. Clamping at
 * execution is what protects the rules ALREADY STORED with a zero, which no
 * validation change can reach.
 */

func TestATimeoutRuleMustCarryADuration(t *testing.T) {
	r := Rule{Name: "no duration", Pattern: "spam", Action: ActionTimeout, Enabled: true}
	err := r.Compile()
	if err == nil {
		t.Fatal("a timeout rule with no duration compiled. It would be dispatched as " +
			"Ban(0), which every adapter reads as a PERMANENT ban.")
	}
	if !containsAny(err.Error(), "duration", "timeout") {
		t.Errorf("refusal does not name the problem: %v", err)
	}
}

func TestABanRuleNeedsNoDuration(t *testing.T) {
	// A ban is permanent on purpose; zero is its correct value.
	r := Rule{Name: "ban", Pattern: "spam", Action: ActionBan, Enabled: true}
	if err := r.Compile(); err != nil {
		t.Errorf("a ban rule was refused for having no timeout: %v", err)
	}
}

func TestAnOrdinaryTimeoutRuleStillCompiles(t *testing.T) {
	r := Rule{Name: "ten minutes", Pattern: "spam", Action: ActionTimeout,
		TimeoutSeconds: 600, Enabled: true}
	if err := r.Compile(); err != nil {
		t.Errorf("a valid timeout rule was refused: %v", err)
	}
}

// THE HALF THAT PROTECTS RULES ALREADY IN THE DATABASE. Validation cannot reach
// a rule stored before this shipped; the executor can.
func TestAStoredZeroTimeoutIsClampedRatherThanBanning(t *testing.T) {
	if got := TimeoutDuration(0); got <= 0 {
		t.Errorf("TimeoutDuration(0) = %v — a non-positive duration is a PERMANENT "+
			"ban at every adapter. A rule already stored with zero must be clamped, "+
			"not honoured.", got)
	}
	if got := TimeoutDuration(-5); got <= 0 {
		t.Errorf("TimeoutDuration(-5) = %v, want a positive clamp", got)
	}
	if got, want := TimeoutDuration(600), 600*time.Second; got != want {
		t.Errorf("TimeoutDuration(600) = %v, want %v — a real duration must pass "+
			"through untouched", got, want)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if equalFold(s[i:i+len(sub)], sub) {
				return true
			}
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
