package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// A MUTATION WHOSE RECONCILE FAILED SAYS SO IN THE RESPONSE. #709.
//
// The change was really saved, so the status stays a success -- refusing the
// write would be wrong, and a 500 invites a retry that re-POSTs a destination.
// What must not happen is the previous behaviour: a bare 200 with the failure
// in a log line nothing in the product reads, while the UI raises a green toast
// and renders the new state as though it were running.
func TestAFailedReconcileIsCarriedIntoEveryMutationShape(t *testing.T) {
	const warning = "the destination was saved, but the running pipeline could not be updated"

	type payload struct {
		Status string `json:"status"`
	}

	for _, tc := range []struct {
		name string
		body any
		// wantWarnings is what the response's warnings array must end up
		// holding, in order.
		wantWarnings []string
		keep         map[string]any // fields that must survive untouched
	}{
		{
			name:         "a map payload",
			body:         map[string]any{"status": "deleted"},
			wantWarnings: []string{warning},
			keep:         map[string]any{"status": "deleted"},
		},
		{
			name:         "a typed struct payload",
			body:         payload{Status: "created"},
			wantWarnings: []string{warning},
			keep:         map[string]any{"status": "created"},
		},
		{
			// APPENDED, NOT REPLACED. Two handlers already build a warnings
			// array -- a destination create carries its own validation notes --
			// and losing those to make room for this one would trade one silent
			// failure for another.
			name:         "a payload that already carries warnings",
			body:         map[string]any{"status": "ok", "warnings": []string{"the stream key looks short"}},
			wantWarnings: []string{"the stream key looks short", warning},
			keep:         map[string]any{"status": "ok"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeMutation(w, http.StatusOK, warning, tc.body)

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: the row really was saved, and a 5xx "+
					"invites a retry that re-POSTs it", w.Code)
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("response is not a JSON object: %v\n%s", err, w.Body.String())
			}
			for k, want := range tc.keep {
				if got[k] != want {
					t.Errorf("%q = %v, want %v: the payload's own shape must survive", k, got[k], want)
				}
			}
			if got["reconcileFailed"] != true {
				t.Errorf("reconcileFailed is %v, want true. The SPA needs a "+
					"machine-readable flag; matching on the English sentence breaks "+
					"the day it is reworded", got["reconcileFailed"])
			}
			raw, _ := json.Marshal(got["warnings"])
			var list []string
			if err := json.Unmarshal(raw, &list); err != nil {
				t.Fatalf("warnings is not a string array: %s", raw)
			}
			if len(list) != len(tc.wantWarnings) {
				t.Fatalf("warnings = %q, want %q", list, tc.wantWarnings)
			}
			for i := range list {
				if list[i] != tc.wantWarnings[i] {
					t.Errorf("warnings[%d] = %q, want %q", i, list[i], tc.wantWarnings[i])
				}
			}
		})
	}
}

// A SUCCESSFUL RECONCILE ADDS NOTHING. A flag that is always present is a flag
// the UI stops reading, and a warnings array that is always non-empty trains an
// operator to skim it.
func TestASuccessfulReconcileLeavesTheResponseExactlyAsItWas(t *testing.T) {
	w := httptest.NewRecorder()
	writeMutation(w, http.StatusCreated, "", map[string]any{"destination": "one"})

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["warnings"]; ok {
		t.Errorf("a clean mutation carried a warnings array: %v", got)
	}
	if _, ok := got["reconcileFailed"]; ok {
		t.Errorf("a clean mutation carried reconcileFailed: %v", got)
	}
	if got["destination"] != "one" {
		t.Errorf("the payload did not survive: %v", got)
	}
}

// A 204 HAS NOWHERE TO PUT A WARNING, so a reconcile that failed must change the
// status rather than send an empty body that says the whole operation worked.
func TestANoContentMutationBecomesA200WhenTheReconcileFailed(t *testing.T) {
	clean := httptest.NewRecorder()
	writeMutationNoContent(clean, "")
	if clean.Code != http.StatusNoContent {
		t.Errorf("clean status = %d, want 204", clean.Code)
	}
	if clean.Body.Len() != 0 {
		t.Errorf("a clean 204 has a body: %s", clean.Body.String())
	}

	failed := httptest.NewRecorder()
	writeMutationNoContent(failed, "the programme delete did not reach the pipeline")
	if failed.Code == http.StatusNoContent {
		t.Fatal("a failed reconcile still answered 204. A 204 has no body, so the " +
			"only way to say anything at all is to stop claiming complete success")
	}
	var got map[string]any
	if err := json.Unmarshal(failed.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["reconcileFailed"] != true {
		t.Errorf("reconcileFailed = %v, want true", got["reconcileFailed"])
	}
}

// reconcileNow says nothing when there is nothing to say, and every unit-test
// server in this package has a nil manager -- so a helper that warned on nil
// would put a false failure on every mutation response in the suite.
func TestReconcileNowIsSilentWithNoManager(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	if got := s.reconcileNow("the destination"); got != "" {
		t.Errorf("reconcileNow with no manager = %q, want empty: nothing is running, "+
			"so nothing has diverged from what is stored", got)
	}
}
