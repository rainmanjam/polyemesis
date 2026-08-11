package api

import (
	"net/http"
	"strconv"
	"testing"
)

// DELETE /hooks/{id} is a destructive endpoint that had no test at ANY layer:
// not here, and not in internal/db, where store.DeleteHook is called by nothing
// but this handler. A destructive endpoint with zero coverage anywhere gets one.
//
// The half a naive "the row is gone" test misses is the OTHER hooks. A delete
// whose WHERE clause is dropped or widened removes every row in the table, and
// the only assertion that catches it is one that looks at a hook it did not ask
// to delete -- including its SECRET, which is sealed separately and would be
// the expensive thing to lose. An operator would find out when their receiver
// stopped being called, with nothing anywhere saying why.
//
// The request field is `triggers`, not `events`: hookRequest defines it and
// decodeJSONInto calls DisallowUnknownFields, so `events` is a 400 before any
// handler logic runs. That is the shape that makes a test look like it drove a
// route when it drove the JSON decoder.

type apitailHook struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	HasSecret bool   `json:"hasSecret"`
}

func apitailCreateHook(t *testing.T, h http.Handler, sign func(*http.Request), name string) apitailHook {
	t.Helper()
	var out struct {
		ID     int64       `json:"id"`
		Hook   apitailHook `json:"hook"`
		Secret string      `json:"secret"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/hooks", map[string]any{
		"name":     name,
		"url":      "https://receiver.example/" + name,
		"triggers": []string{"ingest.published"},
	}, http.StatusCreated), &out)
	if out.Secret == "" {
		t.Fatalf("creating hook %q returned no signing secret; the fixture cannot "+
			"go on to assert that a survivor kept one", name)
	}
	if !out.Hook.HasSecret {
		t.Fatalf("hook %q was created without hasSecret set", name)
	}
	out.Hook.ID = out.ID
	return out.Hook
}

func TestDeletingOneHookLeavesTheOthersAndTheirSecrets(t *testing.T) {
	h, _, sign := sourceServer(t)

	doomed := apitailCreateHook(t, h, sign, "first")
	survivor := apitailCreateHook(t, h, sign, "second")
	if doomed.ID == survivor.ID {
		t.Fatalf("both hooks came back with id %d; the fixture cannot tell them apart", doomed.ID)
	}
	route := "/api/v1/hooks/" + strconv.FormatInt(doomed.ID, 10)

	r := jsonRequest(t, http.MethodDelete, route, nil)
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "DELETE "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE %s: status %d, want 200. Body: %s", route, w.Code, w.Body.String())
	}
	var ack map[string]string
	decodeInto(t, w.Body.Bytes(), &ack)
	if ack["status"] != "deleted" {
		t.Errorf("DELETE %s answered %v, want {\"status\":\"deleted\"}", route, ack)
	}

	// The row is really gone, asserted by asking for it rather than by trusting
	// the acknowledgement. A no-op delete answers exactly the same 200.
	mustJSONError(t, h, sign, http.MethodGet, route, nil, http.StatusNotFound)

	// And deleting it again is a 404, not a second cheerful 200. A delete that
	// reports success for a row that was never there means a script cannot tell
	// "removed it" from "there was nothing to remove", which is how a typo in
	// an id gets mistaken for a completed cleanup.
	mustJSONError(t, h, sign, http.MethodDelete, route, nil, http.StatusNotFound)

	// THE HALF A ROW-IS-GONE TEST MISSES. The other hook is still listed, still
	// itself, and still holds the sealed signing key that can never be shown
	// again.
	var rows []apitailHook
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/hooks", nil, http.StatusOK), &rows)
	if len(rows) != 1 {
		t.Fatalf("after deleting one of two hooks the list holds %d: %+v", len(rows), rows)
	}
	if rows[0].ID != survivor.ID {
		t.Fatalf("the surviving hook is id %d, but %d was the one deleted",
			rows[0].ID, doomed.ID)
	}
	if rows[0].Name != "second" {
		t.Errorf("the surviving hook is named %q, want \"second\"", rows[0].Name)
	}
	if !rows[0].HasSecret {
		t.Error("the surviving hook lost its signing secret. It is sealed and " +
			"shown exactly once at creation, so this is not recoverable: every " +
			"delivery to that receiver would fail signature verification and the " +
			"operator would have to re-key by hand.")
	}
}
