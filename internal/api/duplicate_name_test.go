package api

import (
	"net/http"
	"strings"
	"testing"
)

// TWO THINGS WITH THE SAME NAME CANNOT BE TOLD APART, AND THE LIST IS ALL THERE
// IS.
//
// Alert rules and hooks both accepted a name identical to one already stored.
// An operator with two rules called "disk" cannot see which is which, and the
// one they switch off may not be the one that has been firing -- which is
// discovered when the alert they thought they silenced fires again, or when the
// one they needed does not.
//
// 409 rather than 400 on purpose: the body was well formed, and what it asked
// for collides with something that exists. A client can offer "rename the other
// one" for a conflict and cannot for a bad request.
//
// Mutation: drop the CheckNameUnique call from db.CreateAlertRule. Observed to
// fail with "a second alert rule took a name that was already taken".
func TestASecondAlertRuleCannotTakeATakenName(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())

	rule := map[string]any{
		"name": "disk", "enabled": true, "url": "https://example.com/hook",
		"format": "json", "minSeverity": "warning",
	}
	send(t, h, sign, http.MethodPost, "/api/v1/alerts/rules", rule, http.StatusCreated)

	// Case- and space-folded, because "Disk " and "disk" are indistinguishable
	// on screen and that is the whole harm.
	rule["name"] = "  Disk "
	body := send(t, h, sign, http.MethodPost, "/api/v1/alerts/rules", rule, http.StatusConflict)
	if !strings.Contains(strings.ToLower(string(body)), "already exists") {
		t.Errorf("the refusal does not say what is wrong:\n  %s", body)
	}

	// THE CONTROL. A guard that refused every name would satisfy the assertion
	// above and make the feature unusable.
	rule["name"] = "network"
	send(t, h, sign, http.MethodPost, "/api/v1/alerts/rules", rule, http.StatusCreated)
}

// The same for webhooks, which have their own store and their own writer -- the
// mapping is shared but the check is not, so both need saying.
func TestASecondHookCannotTakeATakenName(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())

	hook := map[string]any{
		"name": "ingest edges", "enabled": true, "url": "https://example.com/hook",
		"triggers": []string{"ingest.published"},
	}
	send(t, h, sign, http.MethodPost, "/api/v1/hooks", hook, http.StatusCreated)
	send(t, h, sign, http.MethodPost, "/api/v1/hooks", hook, http.StatusConflict)

	hook["name"] = "destination edges"
	send(t, h, sign, http.MethodPost, "/api/v1/hooks", hook, http.StatusCreated)
}
