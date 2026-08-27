package api

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THREE SETTINGS THAT WERE STORED, DOCUMENTED, AND NEVER REACHED THE ENGINE.
 *
 * Each is the same shape as the capability bug fixed alongside this: the
 * product tells the operator something is in effect, and nothing is wired to
 * it. None of them fails loudly, which is why all three survived.
 *
 * The reload ledger makes this checkable rather than a matter of opinion:
 * internal/engine/reload.go:301 classifies automod.model.timeoutForBan as
 * {ClassLive, "ApplyAutomod", "rebuilds the model checker"}, which is a written
 * promise that saving it takes effect through that function.
 */

func modelSettings() db.AutomodSettings {
	var a db.AutomodSettings
	a.Enabled = true
	a.Model.Enabled = true
	a.Model.Endpoint = "https://example.invalid/v1"
	a.Model.Model = "test-model"
	a.Model.TimeoutSeconds = 12 // the HTTP timeout
	a.Model.TimeoutForBan = 45  // how long a model-decided timeout should last
	a.Model.Action = string(automod.ActionTimeout)
	a.Model.MaxCallsPerHour = 10
	a.Model.MinConfidence = 0.5
	return a
}

// THE OPERATOR'S timeoutForBan MUST REACH THE MODEL.
//
// ApplyAutomod set cfg.Timeout from Model.TimeoutSeconds -- the HTTP request
// timeout -- and never set cfg.TimeoutSeconds, which is the duration a
// model-decided timeout actually lasts. So every model timeout used the
// built-in 300s default and the operator's number did nothing, while the reload
// ledger promised otherwise.
func TestTheModelTimeoutForBanReachesTheEngine(t *testing.T) {
	a := modelSettings()
	cfg, _ := modelConfigFrom(a)

	if got, want := cfg.TimeoutSeconds, a.Model.TimeoutForBan; got != want {
		t.Errorf("model timeout = %ds, want the configured %ds. The operator set "+
			"timeoutForBan and every model-decided timeout ignored it.", got, want)
	}
	// And the HTTP timeout must still come from its own field, not be confused
	// with the ban duration.
	if got, want := cfg.Timeout, time.Duration(a.Model.TimeoutSeconds)*time.Second; got != want {
		t.Errorf("HTTP timeout = %v, want %v — the two fields must not be swapped", got, want)
	}
}

// A ZERO timeoutForBan MUST NOT BECOME A PERMANENT BAN either, for the same
// reason the rule path was fixed: the adapters read zero as forever.
func TestAZeroModelTimeoutDoesNotBecomeAPermanentBan(t *testing.T) {
	a := modelSettings()
	a.Model.TimeoutForBan = 0
	cfg2, _ := modelConfigFrom(a)
	if got := cfg2.TimeoutSeconds; got <= 0 {
		t.Errorf("model timeout = %d with timeoutForBan unset; a non-positive "+
			"duration is a permanent ban at every adapter", got)
	}
}
