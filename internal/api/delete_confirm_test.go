package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A DELETE that destroys more than its URL names has to be confirmed in the
// request, not only in a dialog. Readiness review row 15.
//
// DELETE /sources/{id} cascades to every destination and rendition on the
// programme (schema.sql, ON DELETE CASCADE), which destroys their sealed stream
// keys and, through the lifecycle coordinator's "removed" reason, ends any live
// YouTube broadcast among them -- permanently. DELETE /destinations/{id} does
// the second of those for one row. Both are reachable with an admin API token,
// so the UI's confirmation dialog was the only gate, and a script never sees it.
//
// Mutation: drop the `!req.Confirm` refusal in handleDeleteSource, or the
// count comparison, or the endableFromPhase gate in handleDeleteDestination.

func sourcePath(id int64) string { return "/api/v1/sources/" + strconv.FormatInt(id, 10) }

func destPath(id int64) string { return "/api/v1/destinations/" + strconv.FormatInt(id, 10) }

func TestDeletingASourceWithoutAConfirmationIsRefusedAndDeletesNothing(t *testing.T) {
	h, store, sign := sourceServer(t)
	only := listSources(t, h, sign)[0]
	destID := makeDest(t, h, sign, "survives")

	for _, tc := range []struct {
		name string
		body any
		want int
		says string
	}{
		{"no body at all", nil, http.StatusBadRequest, `"confirm": true`},
		{"confirm false", map[string]any{"confirm": false, "destinations": 1}, http.StatusBadRequest, `"confirm": true`},
		{"confirm without the count", map[string]any{"confirm": true}, http.StatusBadRequest, `"destinations"`},
		{"a stale count", map[string]any{"confirm": true, "destinations": 0}, http.StatusConflict, "1 destination"},
		{"an unknown field", map[string]any{"confirm": true, "destinations": 1, "force": true}, http.StatusBadRequest, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, http.MethodDelete, sourcePath(only.ID), tc.body, tc.want)
			if !strings.Contains(msg, tc.says) {
				t.Errorf("refusal %q does not say %q", msg, tc.says)
			}
			if _, err := store.GetSource(only.ID); err != nil {
				t.Fatalf("a refused delete removed the source anyway: %v", err)
			}
			if _, err := store.GetDestination(destID); err != nil {
				t.Fatalf("a refused delete cascaded to the source's destination anyway: %v", err)
			}
		})
	}

	// The count the operator was shown is the count that is there: it goes.
	send(t, h, sign, http.MethodDelete, sourcePath(only.ID),
		map[string]any{"confirm": true, "destinations": 1}, http.StatusNoContent)
	if _, err := store.GetDestination(destID); err == nil {
		t.Error("the confirmed delete left the source's destination behind")
	}
}

func TestDeletingAnOnAirDestinationRequiresAConfirmation(t *testing.T) {
	h, store, sign := sourceServer(t)

	for _, phase := range []string{"live", "testing", "LIVE"} {
		t.Run(phase, func(t *testing.T) {
			id := makeDest(t, h, sign, "on air "+phase)
			if _, err := store.UpdateLifecycle(id, func(d *db.Destination) bool {
				d.Lifecycle.BroadcastID = "broadcast-placeholder"
				d.Lifecycle.Phase = phase
				return true
			}); err != nil {
				t.Fatal(err)
			}

			msg := mustJSONError(t, h, sign, http.MethodDelete, destPath(id), nil, http.StatusBadRequest)
			if !strings.Contains(msg, `"confirm": true`) {
				t.Errorf("refusal %q does not name the confirmation it wants", msg)
			}
			mustJSONError(t, h, sign, http.MethodDelete, destPath(id),
				map[string]any{"confirm": false}, http.StatusBadRequest)
			if _, err := store.GetDestination(id); err != nil {
				t.Fatalf("an unconfirmed delete of a %s broadcast removed the row: %v", phase, err)
			}

			send(t, h, sign, http.MethodDelete, destPath(id), map[string]any{"confirm": true}, http.StatusOK)
			if _, err := store.GetDestination(id); err == nil {
				t.Error("the confirmed delete left the row behind")
			}
		})
	}
}

// Only the delete that would END something asks. A destination with no
// broadcast, or one never confirmed on air, deletes as it always did -- which is
// what keeps every acceptance script and e2e cleanup that deletes its own rows
// working unchanged.
func TestDeletingADestinationWithNothingOnAirNeedsNoConfirmation(t *testing.T) {
	h, store, sign := sourceServer(t)

	plain := makeDest(t, h, sign, "plain")
	send(t, h, sign, http.MethodDelete, destPath(plain), nil, http.StatusOK)

	announced := makeDest(t, h, sign, "announced")
	if _, err := store.UpdateLifecycle(announced, func(d *db.Destination) bool {
		d.Lifecycle.BroadcastID = "broadcast-placeholder"
		d.Lifecycle.Phase = "created"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	send(t, h, sign, http.MethodDelete, destPath(announced), nil, http.StatusOK)

	// A body is still a body: a misspelt confirmation is refused rather than
	// silently read as "not confirmed" -- or, worse, as confirmed.
	other := makeDest(t, h, sign, "misspelt")
	mustJSONError(t, h, sign, http.MethodDelete, destPath(other),
		map[string]any{"confirmed": true}, http.StatusBadRequest)
	if _, err := store.GetDestination(other); err != nil {
		t.Fatalf("a refused delete removed the row: %v", err)
	}
}
