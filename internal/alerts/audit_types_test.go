package alerts

import "testing"

// auditTypes is the security and configuration half of the catalogue, listed
// here rather than derived from AllTypes: a test that asks AllTypes what is in
// AllTypes cannot fail.
var auditTypes = []Type{
	TypeLoginFailed, TypeLoginSucceeded,
	TypePasswordChanged, TypeAPITokenCreated, TypeAPITokenRevoked,
	TypeSettingsChanged, TypeClipCaptured,
}

// The reason the five types had to go into AllTypes rather than into a
// catalogue of their own.
//
// KnownType is defined in terms of AllTypes, Rule.Normalized silently DROPS any
// subscription entry that is not KnownType, and db.scanAlertRule runs
// Normalized on every read. So a type outside AllTypes is not merely absent
// from the picker: an operator who somehow stored a subscription to it would
// find that subscription deleted the next time anything read the rule back out
// of SQLite, with no error anywhere to say so.
//
// Mutation: drop TypeSettingsChanged from the AllTypes return. Observed FAIL
// ("Normalized dropped the subscription entirely").
func TestAuditTypesSurviveTheRoundTripThroughARule(t *testing.T) {
	for _, ty := range auditTypes {
		t.Run(string(ty), func(t *testing.T) {
			if !KnownType(ty) {
				t.Fatalf("KnownType(%q) = false; a rule can never subscribe to it", ty)
			}
			r := Rule{Name: "ops", URL: "https://example.test/hook", Events: []Type{ty}}.Normalized()
			if err := r.Validate(); err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
			got := r.Events
			if len(got) != 1 || got[0] != ty {
				t.Fatalf("Normalized dropped the subscription entirely: Events = %v, want [%q]", got, ty)
			}
		})
	}
}

// The five are APPENDED, so no row of the settings picker moves.
//
// The picker renders AllTypes in order. An operator who has learned that the
// fourth checkbox is "ingest recovered" and finds "sign-in failed" there after
// an upgrade has been taught that the list is not stable, and the next thing
// they do is stop reading it. This pins the operational prefix so that an
// insertion is a test failure rather than a UI surprise.
func TestAllTypesKeepsTheOperationalPickerOrderItAlreadyHad(t *testing.T) {
	operational := []Type{
		TypeDestinationDown, TypeDestinationRecovered,
		TypeIngestLost, TypeIngestRecovered,
		TypeFailoverSwitched,
		TypeClipping,
		TypeDiskLow, TypeDiskRecovered,
		TypeLoudnessOut, TypeLoudnessRecovered,
	}
	// Appended after the audit block for the same stability reason this test
	// exists: they belong beside the other destination events by meaning, and
	// putting them there would move eight established rows.
	health := []Type{TypeDestinationFallingBehind, TypeDestinationCaughtUp}
	all := AllTypes()
	if len(all) != len(operational)+len(auditTypes)+len(health) {
		t.Fatalf("AllTypes has %d entries, want %d operational + %d audit + %d health",
			len(all), len(operational), len(auditTypes), len(health))
	}
	for i, want := range operational {
		if all[i] != want {
			t.Fatalf("AllTypes()[%d] = %q, want %q; an existing picker row moved", i, all[i], want)
		}
	}
	// TypeTest stays out of the catalogue. It is the "send a test message"
	// button's event, not something to subscribe to, and Rule.Wants short
	// circuits on it before any filter runs.
	for _, ty := range all {
		if ty == TypeTest {
			t.Fatal("AllTypes contains TypeTest; it is not subscribable")
		}
	}
}

// The behaviour change an upgrade brings, asserted rather than assumed.
//
// Rule.Events empty means "every type", which is the default the first rule
// somebody creates is saved with. Adding a type to AllTypes therefore starts
// delivering it to rules nobody has touched. That is not a bug to be fixed here
// -- see the no-backfill argument in docs/MONITORING.md -- but it is the kind
// of fact that should be written down in a place that breaks if it stops being
// true, because the whole "is this upgrade noisy?" question rests on it.
func TestARuleSubscribedToNothingHearsTheNewSecurityEvents(t *testing.T) {
	r := Rule{Name: "everything", URL: "https://example.test/hook"}.Normalized()
	for _, ty := range auditTypes {
		ev := Event{Type: ty, Severity: SeverityInfo, Key: string(ty)}
		if !r.Wants(ev) {
			t.Fatalf("a rule with an empty Events list refused %q", ty)
		}
	}
	// And the severity floor is still the off switch: a rule raised to
	// critical-only hears the password change and the token mint, and nothing
	// else. This is the mitigation the upgrade note points operators at, so it
	// has to actually work.
	quiet := Rule{Name: "phone", URL: "https://example.test/hook", MinSeverity: SeverityCritical}.Normalized()
	if quiet.Wants(Event{Type: TypeLoginSucceeded, Severity: SeverityInfo}) {
		t.Fatal("a critical-only rule still hears routine sign-ins")
	}
	if !quiet.Wants(Event{Type: TypePasswordChanged, Severity: SeverityCritical}) {
		t.Fatal("a critical-only rule missed a password change")
	}
}
