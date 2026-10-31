package engine

import (
	"strings"
	"testing"
	"time"
)

// Degraded-link failover (#6) at the level of the pure decision.
//
// A degraded primary is one that is still DELIVERING -- liveness says alive --
// but whose link health has been below the operator's thresholds for the
// sustain window. The decision treats it as two different things depending on
// who is asking, and these tests pin both halves:
//
//   - THE LADDER sees it as unavailable, so the selector leaves it for the
//     backup, the playlist or the slate exactly as it would leave a dead one,
//     and a holding pattern does not return to it.
//   - A PIN still sees it as available. An operator who has looked at the
//     degraded picture and chosen it anyway has made a decision the selector
//     must not override; a pin is only dropped when its source stops
//     delivering at all.

func degradedPrimary(cur sourceKind, mut func(*sourceChoice)) sourceChoice {
	return failoverChoice(cur, func(c *sourceChoice) {
		c.primary = delivering(c.now, time.Second, time.Hour)
		c.primaryDegraded = true
		if mut != nil {
			mut(c)
		}
	})
}

func TestADegradedPrimaryHandsOverToALiveBackupAndSaysItWasDegraded(t *testing.T) {
	c := degradedPrimary(sourcePrimary, func(c *sourceChoice) {
		c.backup = delivering(c.now, time.Second, time.Minute)
	})
	kind, reason := chooseSource(c)
	if kind != sourceBackup {
		t.Fatalf("a degraded primary with a live backup chose %q, want the backup", kind)
	}
	if !strings.HasPrefix(reason, "degraded: ") {
		t.Errorf("reason %q does not start with %q; every surface keys on that prefix", reason, "degraded: ")
	}
}

// With no backup, the slate is where a degraded primary goes: the research
// that put #6 on the plan is exactly "fail over to the slate when the link
// degrades, not only on disconnect".
func TestADegradedPrimaryWithNoBackupGoesToTheSlate(t *testing.T) {
	c := degradedPrimary(sourcePrimary, func(c *sourceChoice) { c.backupEnabled = false })
	kind, reason := chooseSource(c)
	if kind != sourceSlate || !strings.HasPrefix(reason, "degraded: ") {
		t.Fatalf("got %q %q, want the slate with a degraded: reason", kind, reason)
	}
}

func TestADegradedPrimaryPrefersThePlaylistToTheSlate(t *testing.T) {
	c := degradedPrimary(sourcePrimary, func(c *sourceChoice) {
		c.backupEnabled = false
		c.playlistRunning = true
	})
	kind, reason := chooseSource(c)
	if kind != sourcePlaylist || !strings.HasPrefix(reason, "degraded: ") {
		t.Fatalf("got %q %q, want the playlist with a degraded: reason", kind, reason)
	}
}

// Nothing else to run: a degraded picture beats none, so the primary stays and
// nothing is announced.
func TestADegradedPrimaryWithNowhereToGoStaysOnAirSilently(t *testing.T) {
	c := degradedPrimary(sourcePrimary, func(c *sourceChoice) {
		c.backupEnabled = false
		c.slateEnabled = false
	})
	kind, reason := chooseSource(c)
	if kind != sourcePrimary || reason != "" {
		t.Fatalf("got %q %q, want the primary held with no reason", kind, reason)
	}
}

// The pin half. Without the separate pin availability this decided the slate:
// the ladder's "unavailable" overrode the operator.
func TestAnOperatorWhoPinsADegradedPrimaryKeepsIt(t *testing.T) {
	c := degradedPrimary(sourcePrimary, func(c *sourceChoice) {
		c.pinned = sourcePrimary
		c.backup = delivering(c.now, time.Second, time.Minute)
	})
	kind, reason := chooseSource(c)
	if kind != sourcePrimary {
		t.Fatalf("a pinned primary that is degraded but delivering was overridden by %q", kind)
	}
	if !strings.Contains(reason, "operator selected the primary") {
		t.Errorf("reason %q is not the pin's", reason)
	}
}

// The flap half. The slate and playlist branches return to a primary the moment
// it is available, without the return mode; if a degraded primary counted as
// available there, every sweep would flip slate -> primary -> slate.
func TestHoldingPatternsDoNotReturnToADegradedPrimary(t *testing.T) {
	for _, cur := range []sourceKind{sourceSlate, sourcePlaylist} {
		c := degradedPrimary(cur, func(c *sourceChoice) {
			c.backupEnabled = false
			c.playlistRunning = cur == sourcePlaylist
		})
		if kind, reason := chooseSource(c); kind != cur || reason != "" {
			t.Errorf("on %s with a degraded primary: got %q %q, want to stay put silently", cur, kind, reason)
		}
	}
}

// Auto return measures stability of a HEALTHY primary: a degraded one is not a
// candidate however long it has been delivering.
func TestAutoReturnDoesNotGoBackToADegradedPrimary(t *testing.T) {
	c := degradedPrimary(sourceBackup, func(c *sourceChoice) {
		c.autoReturn = true
		c.backup = delivering(c.now, time.Second, time.Minute)
	})
	if kind, _ := chooseSource(c); kind != sourceBackup {
		t.Fatalf("auto return went back to a degraded primary (%q)", kind)
	}
}

// When the backup fails while the primary is degraded, the primary is still the
// better picture than a slate -- but the ladder has already ruled it out, so
// the reason must say why the selector is not simply going home.
func TestABackupThatDiesUnderADegradedPrimarySaysSo(t *testing.T) {
	c := degradedPrimary(sourceBackup, nil)
	kind, reason := chooseSource(c)
	if kind != sourceSlate {
		t.Fatalf("got %q, want the slate", kind)
	}
	if !strings.HasPrefix(reason, "degraded: ") {
		t.Errorf("reason %q does not explain that the primary is degraded", reason)
	}
}

// A primary that is not delivering is DEAD, not degraded, whatever the bit
// says: the monitor resets on silence, but the decision must not depend on that
// to produce the right sentence.
func TestADeadPrimaryIsReportedDeadEvenIfTheDegradedBitIsStale(t *testing.T) {
	c := failoverChoice(sourcePrimary, func(c *sourceChoice) {
		c.primaryDegraded = true
		c.backup = delivering(c.now, time.Second, time.Minute)
	})
	_, reason := chooseSource(c)
	if strings.HasPrefix(reason, "degraded: ") {
		t.Errorf("a dead primary was reported as %q", reason)
	}
}
