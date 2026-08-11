package api

import (
	"net/http"
	"testing"
)

// Capturing from a buffer that is ON but has received nothing is a 409, and
// that is a real branch rather than an error path: it is what every operator
// sees for the first thirty seconds after enabling the buffer, and the page
// turns it into "start streaming first". The generic 500 beside it says "the
// clip buffer is switched off", which is a different situation with a different
// fix, and errors.Is(err, clips.ErrEmpty) is the only thing separating them.
//
// WHY THIS IS NOT ALREADY COVERED. clipbuffer_test.go:92
// (TestCapturingAClipWithNoBufferIsRefusedNotIgnored) sends {"name":"goal"} to
// POST /clips. `name` is not a field of the capture request -- the only field
// is `seconds` -- and decodeJSONInto calls DisallowUnknownFields, so that body
// is a 400 from the JSON decoder BEFORE any capture logic runs. Its assertion
// is "not 200 and not 201", which a 400 satisfies. A mutation making capture
// falsely return success passes it today. That test is another council's file
// and is deliberately not edited here; see this PR's description.
//
// So this covers the two states that test cannot reach, with the field name the
// handler actually defines.

func TestCapturingFromAnEnabledButEmptyBufferIsAConflictNotAFailure(t *testing.T) {
	h, _, sign := sourceServer(t)

	// Buffer ON. The engine really starts a clips.Capturer here -- it opens a
	// relay socket, no FFmpeg involved -- so Clip() reaches the capturer and
	// comes back with clips.ErrEmpty because nothing has been relayed into it.
	// That is the branch under test, not a nil-dependency stub.
	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": true, "windowSeconds": 30}, http.StatusOK)

	r := jsonRequest(t, http.MethodPost, "/api/v1/clips", map[string]any{"seconds": 0})
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "POST /clips")
	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("capturing from an empty buffer reported SUCCESS (%d): %s\n"+
			"The operator is handed a clip name for a file that was never "+
			"written, and finds out when they try to play it.",
			w.Code, w.Body.String())
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("capturing from an enabled-but-empty buffer produced %d, want 409.\n"+
			"409 is not an error path: it is the first thirty seconds after the "+
			"buffer is switched on, and the page turns it into \"start streaming "+
			"first\". A 500 sends the operator looking for a fault that is not "+
			"there. Body: %s", w.Code, w.Body.String())
	}
	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/clips",
		map[string]any{"seconds": 0}, http.StatusConflict)
	if msg != "the clip buffer is empty — nothing has arrived to capture yet" {
		t.Errorf("the 409 said %q; the operator needs to be told nothing has "+
			"arrived yet, not that something failed", msg)
	}

	// The other state, and the reason the 409 has to be distinguishable from
	// it: buffer OFF is a different situation with a different fix.
	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": false}, http.StatusOK)

	off := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/clips",
		map[string]any{"seconds": 0}, http.StatusInternalServerError)
	if off != "the clip buffer is switched off" {
		t.Errorf("with the buffer off, POST /clips said %q; \"switched off\" and "+
			"\"empty\" are two different things to do about it", off)
	}
}
