package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

/* The onboarding tour's two endpoints.

   The route ledger already establishes the SCOPE half by driving it: GET
   /api/v1/tour is swept (a read token receives 200 with a body and no
   sentinel), and POST /api/v1/tour/complete is classified denied-by-method
   because a read token was actually refused it. Neither of those says anything
   about what the handlers DO, which is what is here.

   The behaviour worth pinning is idempotence. The UI calls complete when the
   operator finishes the tour AND when they dismiss the offer, and the two race:
   pressing "done" on the last step destroys the popover, which is the same path
   a dismissal takes, so the second call is normal rather than exceptional. A
   handler that toggled -- or that simply re-stamped the timestamp -- would turn
   that into the offer coming back, or into a "first seen" date that is really a
   "last replayed" date. */

func readTourState(t *testing.T, h http.Handler, sign func(*http.Request)) tourState {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/tour", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/tour status = %d, body %s", w.Code, w.Body.String())
	}
	var got tourState
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode tour state: %v (body %s)", err, w.Body.String())
	}
	return got
}

func TestTheTourIsOfferedUntilItIsCompleted(t *testing.T) {
	h, _, sign := sourceServer(t)

	if got := readTourState(t, h, sign); got.Completed || got.CompletedAt != 0 {
		t.Fatalf("a fresh install reports %+v; the tour must be OFFERED on an install "+
			"nobody has taken it on, or the feature never appears at all", got)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/tour/complete", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/tour/complete status = %d, body %s", w.Code, w.Body.String())
	}
	var wrote tourState
	if err := json.Unmarshal(w.Body.Bytes(), &wrote); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	if !wrote.Completed || wrote.CompletedAt == 0 {
		t.Fatalf("the write answered %+v; it returns the new state so the client does "+
			"not have to re-read to learn what it just did", wrote)
	}

	after := readTourState(t, h, sign)
	if !after.Completed || after.CompletedAt != wrote.CompletedAt {
		t.Errorf("after completing, GET says %+v and the write said %+v. The offer is "+
			"driven by this read, so a disagreement is the strip coming back on the "+
			"next page load.", after, wrote)
	}
}

// TestCompletingTheTourTwiceKeepsTheFirstTimestamp plants a timestamp rather
// than posting twice and comparing.
//
// The obvious version of this test -- POST, POST, compare the two timestamps --
// PASSES AGAINST A HANDLER THAT RE-STAMPS ON EVERY CALL, and that was measured
// rather than guessed: a mutation that replaced the whole guard with an
// unconditional `SetTourCompleted(now)` was green, because both requests land
// inside the same wall-clock second and `now == now`. A guard whose subject is
// a second-resolution timestamp cannot be checked with two calls a millisecond
// apart.
//
// So the first completion is written directly, well into the past, where a
// re-stamp is unmistakable.
func TestCompletingTheTourTwiceKeepsTheFirstTimestamp(t *testing.T) {
	h, store, sign := sourceServer(t)

	u, err := store.GetUser()
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	const planted = 1600000000 // 2020-09-13, unmistakably not "now"
	if err := store.SetTourCompleted(u.ID, time.Unix(planted, 0)); err != nil {
		t.Fatalf("plant the first completion: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/tour/complete", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/tour/complete status = %d, body %s", w.Code, w.Body.String())
	}
	var second tourState
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !second.Completed {
		t.Error("the second completion un-completed the tour. This endpoint is called " +
			"twice by ordinary use -- finishing the last step also destroys the " +
			"popover, which is the dismiss path -- so a toggle here means the offer " +
			"reappears for anyone who reads to the end.")
	}
	if second.CompletedAt != planted {
		t.Errorf("completedAt moved from %d to %d on the second call. The first "+
			"completion is meant to win, so the field answers \"when did this operator "+
			"stop needing the tour\" rather than \"when did they last replay it\".",
			planted, second.CompletedAt)
	}
	// And it must have stayed put in the DATABASE, not merely in the response:
	// a handler that returns the old value while writing a new one is the same
	// defect with a longer fuse.
	stored, err := store.TourCompletedAt(u.ID)
	if err != nil {
		t.Fatalf("TourCompletedAt: %v", err)
	}
	if stored != planted {
		t.Errorf("the stored timestamp is %d, want %d. The response agreed with the "+
			"plant while the row did not.", stored, planted)
	}
}

// TestAReadTokenCannotCompleteTheTour is the scope claim stated where somebody
// looking for it would look.
//
// The ledger establishes it too, as denied-by-method over the whole non-GET
// surface, and that is the stronger check because it is universally quantified.
// This one is here because the ledger's failure message is about a population
// and this one is about a decision: the write is admin-scoped because a
// read-only credential must not be able to change what another operator's
// console shows them, and if that ever stops being true the failure should say
// so in those words.
func TestAReadTokenCannotCompleteTheTour(t *testing.T) {
	h, _, sign := sourceServer(t)
	read := createScopedToken(t, h, sign, "monitoring", "read")

	r := jsonRequest(t, http.MethodPost, "/api/v1/tour/complete", nil)
	bearer(read)(r)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Errorf("a read-scoped token got %d from POST /api/v1/tour/complete, want 403. "+
			"It is a write to user state; \"it is only a boolean\" is the argument that "+
			"ends with a read token switching off something somebody else relies on.",
			w.Code)
	}

	// And the READ is deliberately open to it: the answer is one timestamp about
	// a popover, and refusing it would be a denial that teaches nobody anything.
	rr := jsonRequest(t, http.MethodGet, "/api/v1/tour", nil)
	bearer(read)(rr)
	if w := do(t, h, rr); w.Code != http.StatusOK {
		t.Errorf("a read-scoped token got %d from GET /api/v1/tour, want 200", w.Code)
	}
}
