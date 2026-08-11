package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
)

// auditedUpgradeServer is upgradeServer plus the Server itself, so the audit
// sink can be attached. upgradeServer discards it.
func auditedUpgradeServer(t *testing.T) (http.Handler, func(*http.Request), *Server) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	path, _ := fakeInstall(t)
	s.upgradeMethod = upgrade.MethodSystemd
	s.execPath = path
	return h, login(t, h), s
}

// fieldValue returns the value of the named field, and whether it was present.
// Event.Fields is a slice, not a map: WithField appends, and drops an empty
// value entirely, so "absent" and "empty" are the same thing by construction.
func fieldValue(ev *alerts.Event, name string) (string, bool) {
	for _, f := range ev.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// findEvent returns the first event of type typ, or nil.
func findEvent(evs []alerts.Event, typ alerts.Type) *alerts.Event {
	for i := range evs {
		if evs[i].Type == typ {
			return &evs[i]
		}
	}
	return nil
}

// TestStagingAnUpgradeRaisesAnAuditEvent is #148.
//
// Both endpoints wrote a structured log line and nothing else. A log line stays
// on the box whose binary was just replaced; the audit trail is what reaches
// the operator wherever they are, and the trail already covers a minted API
// token on the reasoning that it "survives the response to a compromise".
// Replacing the binary survives a password change, a token revocation AND a
// restart -- and the restart is what makes it take effect.
//
// Driven through the HANDLER, not the constructor. Constructor-only assertions
// are exactly the gap audit.go records: all five publishAudit call sites were
// once deleted with this package still green.
func TestStagingAnUpgradeRaisesAnAuditEvent(t *testing.T) {
	h, sign, s := auditedUpgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})
	got := captureAudit(t, s)

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	ev := findEvent(*got, alerts.TypeUpgradeStaged)
	if ev == nil {
		t.Fatalf("staging the binary raised %v, not %s", typesOf(*got), alerts.TypeUpgradeStaged)
	}
	if ev.Severity != alerts.SeverityCritical {
		t.Errorf("severity = %s, want %s: this outlives a password change and a restart",
			ev.Severity, alerts.SeverityCritical)
	}
	if got, _ := fieldValue(ev, fieldVersion); got != releaseTag {
		t.Errorf("version field = %q, want %q", got, releaseTag)
	}
	// An unforced stage must NOT carry the forced field: see auditLoginSucceeded
	// for why a routine "no" trains readers to skip the line that matters.
	if _, ok := fieldValue(ev, fieldForced); ok {
		t.Errorf("an off-air stage was labelled forced: %v", ev.Fields)
	}
}

// TestForcingAnUpgradePastALiveBroadcastIsRecordedAsForced is the field the
// issue singles out: "the forced case is the one an operator will want to find
// later".
func TestForcingAnUpgradePastALiveBroadcastIsRecordedAsForced(t *testing.T) {
	h, sign, s := auditedUpgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, liveBroadcast())
	got := captureAudit(t, s)

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag, Force: true}, http.StatusOK)

	ev := findEvent(*got, alerts.TypeUpgradeStaged)
	if ev == nil {
		t.Fatalf("a forced stage raised %v", typesOf(*got))
	}
	if v, ok := fieldValue(ev, fieldForced); !ok || v == "" {
		t.Errorf("an upgrade forced past a live broadcast is not marked forced: %v", ev.Fields)
	}
}

// TestARefusedUpgradeRaisesNothing keeps the trail honest in the other
// direction. An event for an upgrade that did not happen is worse than none:
// it sends the operator looking for a replaced binary that was never replaced.
func TestARefusedUpgradeRaisesNothing(t *testing.T) {
	h, sign, s := auditedUpgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, liveBroadcast())
	got := captureAudit(t, s)

	// No Force, and something is on air: refused at the gate.
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusConflict)

	if ev := findEvent(*got, alerts.TypeUpgradeStaged); ev != nil {
		t.Errorf("a refused stage raised %s: %v", ev.Type, ev.Fields)
	}
}

// TestRollingBackRaisesAnAuditEvent covers the other half of #148. A rollback
// is a binary replacement in the other direction, and "somebody quietly
// returned this box to the version before the security fix" is exactly the
// sentence this trail exists to make findable.
func TestRollingBackRaisesAnAuditEvent(t *testing.T) {
	h, sign, s := auditedUpgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	// A stage first, so there is a rollback point to restore.
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	// Captured AFTER the stage, so the stage's own event cannot be mistaken
	// for the rollback's.
	got := captureAudit(t, s)
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{}, http.StatusOK)

	ev := findEvent(*got, alerts.TypeUpgradeRolledBack)
	if ev == nil {
		t.Fatalf("rolling back raised %v, not %s", typesOf(*got), alerts.TypeUpgradeRolledBack)
	}
	if ev.Severity != alerts.SeverityCritical {
		t.Errorf("severity = %s, want %s", ev.Severity, alerts.SeverityCritical)
	}
	// No version: a rollback names no release, and a tag here would be a guess
	// printed as a fact.
	if v, ok := fieldValue(ev, fieldVersion); ok {
		t.Errorf("the rollback event invented a version %q", v)
	}
}

// TestARefusedRollbackRaisesNothing is the negative half for rollback.
func TestARefusedRollbackRaisesNothing(t *testing.T) {
	h, sign, s := auditedUpgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})
	got := captureAudit(t, s)

	// Nothing was ever staged, so there is no rollback point.
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{}, http.StatusConflict)

	if ev := findEvent(*got, alerts.TypeUpgradeRolledBack); ev != nil {
		t.Errorf("a refused rollback raised %s", ev.Type)
	}
}

// TestTheUpgradeEventsAreSubscribable is the difference between an event that
// reaches an operator and one that is silently dropped: db.scanAlertRule runs
// Rule.Normalized on every read, and Normalized deletes any event KnownType
// does not recognise. A type missing from AllTypes cannot be subscribed to and
// cannot survive a round trip through the settings page.
func TestTheUpgradeEventsAreSubscribable(t *testing.T) {
	for _, typ := range []alerts.Type{alerts.TypeUpgradeStaged, alerts.TypeUpgradeRolledBack} {
		if !alerts.KnownType(typ) {
			t.Errorf("%s is not a known type, so a subscription to it is deleted on read", typ)
		}
	}
}
