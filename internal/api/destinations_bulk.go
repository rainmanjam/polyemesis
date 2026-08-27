package api

import (
	"net/http"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ============================================================================
// BULK DESTINATION CONTROL
//
// One press that starts, or stops, every destination on the install. An
// operator with eight destinations was pressing eight buttons.
//
// ---------------------------------------------------------------------------
// WHAT THIS DOES TO A YOUTUBE BROADCAST. READ THIS BEFORE CHANGING ANYTHING.
// ---------------------------------------------------------------------------
//
// These two routes drive applyDestinationEnabled, which is the SAME code the
// per-destination start and stop buttons drive. That is deliberate: "stop all"
// must be exactly N presses of a button the operator already has, so it can
// never be more destructive than the control it replaces.
//
// It is also the whole of the danger, because in this codebase start/stop and
// enable/disable ARE ONE THING. There is no separate "process" control:
//
//	POST /destinations/{id}/stop -> setDestinationEnabled(w, r, false)
//	                             -> db.SetDestinationEnabled(id, false)
//	                             -> destinations.enabled = 0
//
// and internal/api/lifecycle.go keys its END policy on exactly that column.
// planLifecycle(enabled=false, ...) at lifecycle.go:804 is "THE END BRANCH":
// a destination whose recorded phase is testing, live, testStarting or
// liveStarting is sent oauth.PhaseComplete. A completed YouTube broadcast
// CANNOT RETURN TO LIVE -- lifecycle.go:838 says so in the fault text an
// operator would then be shown, and its only remedy is to create or announce a
// new broadcast.
//
// So: STOPPING ALL DESTINATIONS ENDS EVERY YOUTUBE BROADCAST ON THIS INSTALL,
// PERMANENTLY. Starting them again puts the video back on the wire; it does not
// bring the broadcasts back. That is a property of the existing per-destination
// stop button, not something introduced here -- but a control that does it to
// one row on purpose and a control that does it to eight rows at once are
// different amounts of consequence, which is why the UI names the outcome in
// its confirmation instead of saying "you can start them again".
//
// A crash is deliberately NOT this path: the coordinator only ends what the row
// says is disabled, so a daemon that dies leaves the broadcasts recoverable.
// Pressing stop is an operator saying they are finished.
//
// If a future change wants a bulk control that takes the video off air WITHOUT
// ending broadcasts, it needs a genuine process-level stop that leaves
// destinations.enabled alone. One does not exist yet. Do not fake it by
// skipping the reconcile.
//
// ---------------------------------------------------------------------------
// EVERY DESTINATION, NOT A SELECTION
// ---------------------------------------------------------------------------
//
// There is no id list in the request body and there are no checkboxes on the
// cards. The routes act on whatever ListDestinations returns. A bulk control
// with a selection is a per-destination control with extra steps, and the
// selection state is one more thing that can be stale when the button is
// pressed.
// ============================================================================

// bulkStartPacing is the gap left between starting one destination and starting
// the next.
//
// IT IS A PACING CHOICE, NOT A LIMIT. It encodes no platform's ceiling, no
// concurrency cap and no quota -- nothing here counts anything or refuses
// anything, and nothing here should start. It is a decision about how fast this
// box asks the world for things.
//
// What it buys, in order:
//
//   - The local burst. Each start writes a row, runs a full manager reconcile
//     and spawns an FFmpeg child that immediately opens a socket and starts
//     encoding. Eight of those inside one scheduler tick contend for CPU with
//     the ingest that is already running, and the process that loses is not
//     necessarily one of the new ones. Spread over a couple of seconds each,
//     the box absorbs them one at a time.
//
//   - The remote burst. Several destinations coming up in the same instant
//     arrive at their platforms as one clap of near-simultaneous connections,
//     and platforms answer a burst differently from the same connections
//     arriving spread out. Refusals earned that way are self-inflicted: nothing
//     was wrong with any single destination. Leaving a gap makes them ordinary
//     sequential connections instead.
//
// Two seconds because it is long enough for a reconcile and a child spawn to
// settle before the next one begins, and short enough that a realistic
// destination list finishes inside the time an operator will sit and watch. It
// is a round number chosen for those two reasons and for no other; tune it if
// the box says otherwise, and do not derive it from anything a platform
// publishes.
var bulkStartPacing = 2 * time.Second

// A var rather than a const SO TESTS CAN STOP BURNING WALL CLOCK ON IT, and
// that is not a cosmetic concern: it timed the whole internal/api package out
// in CI.
//
// The suite runs under -race, which is several times slower than a plain run,
// against a 15-minute budget for the package. Two tests drive bulk starts
// across three and five destinations, and at two real seconds a gap that is
// twelve seconds of deliberate sleeping before the race detector's multiplier.
// The package was already the longest in the tree; that pushed it over.
//
// Only the test that asserts the pacing ITSELF should pay for it. Everything
// else that merely needs several destinations started can set this low, which
// is what withBulkPacing does.
//
// Never written outside a test. The one production value is above.

// bulkOutcome is what happened to one destination. See bulkDestResult.
type bulkOutcome string

const (
	// bulkStarted and bulkStopped: the intent was written, the pipeline was
	// reconciled and the process state was read back afterwards.
	bulkStarted bulkOutcome = "started"
	bulkStopped bulkOutcome = "stopped"
	// bulkWarned: it happened, and something about it was NOT observed. Today
	// this is only the unreaped stop (#209) -- SIGKILL issued, nobody waited,
	// a child that may still be publishing. Not a failure and not a clean
	// success, so it is neither.
	bulkWarned bulkOutcome = "warned"
	// bulkFailed: refused, with the reason in Message.
	bulkFailed bulkOutcome = "failed"
	// bulkSkipped: never attempted. Reached when the caller goes away partway
	// through a paced start, which leaves a real tail of destinations that
	// were NOT touched. Reporting those as anything else would be a lie about
	// the install's state.
	bulkSkipped bulkOutcome = "skipped"
)

// bulkDestResult is ONE DESTINATION'S row of the answer.
//
// A BULK RESULT IS REPORTED PER DESTINATION, NEVER AS ONE BOOLEAN. Same
// doctrine as the metadata composer, which states it at Dashboard.tsx:140 for
// the same reason: eight destinations of which two refuse is not "failed", and
// an operator told only "failed" has to go and look at all eight to find out
// which two. Every row says which destination it was, what happened, and -- when
// something did not happen -- why.
//
// Mirrored in ui/src/lib/types.ts as BulkDestResult.
type bulkDestResult struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	Platform db.Platform `json:"platform"`
	Outcome  bulkOutcome `json:"outcome"`
	// State is the supervisor's word for the process after the fact, absent
	// when the engine carries no process for this row.
	State string `json:"state,omitempty"`
	// Message is why, and is present on every outcome that is not a clean
	// start or stop.
	Message string `json:"message,omitempty"`
}

// bulkAction is which of the two things a bulk control does, and it exists so
// that "start everything" and "stop everything" cannot be transposed by a typo.
//
// Finding #3, poka-yoke audit 2026-08-21: the two routes below were one bare
// boolean apart --
//
//	func (s *Server) handleStartAllDestinations(...) { s.bulkSetDestinationsEnabled(w, r, true) }
//	func (s *Server) handleStopAllDestinations(...)  { s.bulkSetDestinationsEnabled(w, r, false) }
//
// -- on the single most destructive operation in the system (see the file
// header). A named type does not stop a future author writing the wrong
// constant, but it stops the mistake THIS finding was about: swapping `true`
// and `false`, or fat-fingering one to the other in a merge, produces a
// compile-time-visible `bulkStop`/`bulkStart` at the call site instead of a
// silent, unreadable boolean.
type bulkAction int

const (
	bulkStop bulkAction = iota
	bulkStart
)

// enabled is the bool applyDestinationEnabled and classifyBulkEffect still
// take -- kept at the boundary between this type and the rest of the package,
// so the transposition risk stays confined to the two one-line handlers above
// it and does not spread through the file.
func (a bulkAction) enabled() bool { return a == bulkStart }

func (s *Server) handleStartAllDestinations(w http.ResponseWriter, r *http.Request) {
	s.bulkSetDestinationsEnabled(w, r, bulkStart)
}

// bulkStopRequest is stop-all's only accepted body.
type bulkStopRequest struct {
	// Confirm is required. Finding #2, poka-yoke audit 2026-08-21: this route
	// permanently ends every live YouTube broadcast on the install (see the
	// file header) and its only gate used to be a dialog in the UI -- a
	// confirmation an API caller, a script, or a replayed request never sees.
	// Lifted from expert.go's PUT, which requires the same field for a
	// strictly smaller hazard (an FFmpeg argument override). Deliberately NOT
	// required on start-all: starting is recoverable by stopping again, but a
	// completed YouTube broadcast cannot return to live (lifecycle.go:838), so
	// only the irreversible half of this pair needs a caller to say they meant it.
	Confirm bool `json:"confirm"`
}

func (s *Server) handleStopAllDestinations(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	var req bulkStopRequest
	// An absent body is a caller who sent no confirmation at all, not a
	// malformed request -- decoding it anyway would answer "invalid request
	// body: EOF" instead of the actual refusal, which is "you must confirm".
	if len(body) > 0 {
		if err := decodeJSONInto(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest,
			"stopping every destination ends any live YouTube broadcast on this install, "+
				"permanently -- starting them again puts video back on the wire but does not "+
				"bring the broadcast back. Repeat this request with a JSON body of "+
				`{"confirm": true} once that is intended.`)
		return
	}
	s.bulkSetDestinationsEnabled(w, r, bulkStop)
}

// bulkSetDestinationsEnabled starts or stops every destination, one at a time.
//
// SYNCHRONOUS, and the response is the record of what happened. A paced start
// of a realistic destination list takes tens of seconds, which is a long
// request and is meant to be: the operator pressed a button that puts video in
// front of an audience, and the answer they get is the finished list rather
// than an acknowledgement they would have to go and verify. The caller's
// context is honoured so a client that gives up stops the pacing rather than
// driving the rest of the list at a browser nobody is reading.
func (s *Server) bulkSetDestinationsEnabled(w http.ResponseWriter, r *http.Request, action bulkAction) {
	enabled := action.enabled()

	// THE PROGRAMME THIS BUTTON WAS PRESSED ON, and until now this file did not
	// have the word in it.
	//
	// Both routes carry requireSource, so on an install with two programmes the
	// request cannot arrive without naming one -- and then the handler listed
	// EVERY destination on the box and acted on all of them. That combination is
	// the worst available: the middleware makes the operator name a programme,
	// which is precisely what convinces them the action is confined to it, and
	// the answer they get back is the full list as evidence they were heard.
	//
	// Stop All on Studio B ended Studio A's live broadcasts. On YouTube that is
	// not recoverable: a broadcast that has been completed cannot return to
	// live (see lifecycle.go), so the operator has not lost a process, they
	// have lost the show.
	//
	// Unnamed still means the whole store, and that is not laziness. A
	// single-source install reaches here with no parameter, and
	// ListDestinationsBySource matches on `source_id = ?`, which a legacy row
	// carrying NULL never satisfies -- scoping unconditionally would quietly
	// drop exactly the destinations nobody has migrated yet.
	sourceID, ok := s.namedSourceParam(w, r)
	if !ok {
		return
	}
	var (
		rows []*db.Destination
		err  error
	)
	if sourceID != nil {
		rows, err = s.store.ListDestinationsBySource(*sourceID)
	} else {
		rows, err = s.store.ListDestinations()
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	results := make([]bulkDestResult, 0, len(rows))
	for i, row := range rows {
		// PACING APPLIES TO STARTS ONLY. Tearing down is local: a stop signals
		// a child this box owns and waits for it, and nothing about doing that
		// eight times in a row reaches anybody else's server. There is nothing
		// for a gap to spread out, so there is no gap.
		//
		// The gap goes BEFORE each start except the first, so pressing the
		// button on a single-destination install costs nothing.
		if enabled && i > 0 {
			select {
			case <-r.Context().Done():
				// The caller is gone. Everything from here was NOT touched,
				// and says so.
				for _, rest := range rows[i:] {
					results = append(results, bulkDestResult{
						ID: rest.ID, Name: rest.Name, Platform: rest.Platform,
						Outcome: bulkSkipped,
						Message: "the request was cancelled before this destination was reached, " +
							"so it was left exactly as it was",
					})
				}
				writeJSON(w, http.StatusOK, bulkBody(enabled, results))
				return
			case <-time.After(bulkStartPacing):
			}
		}
		results = append(results, s.bulkOne(row, enabled))
	}

	// 200 EVEN WHEN ROWS FAILED, because the request itself succeeded: every
	// destination was reached and every one of them has an answer. A status
	// code is one number and this is a list -- collapsing the list into it is
	// the boolean this whole shape exists to avoid.
	writeJSON(w, http.StatusOK, bulkBody(enabled, results))
}

func bulkBody(enabled bool, results []bulkDestResult) map[string]any {
	action := "stop"
	if enabled {
		action = "start"
	}
	return map[string]any{"action": action, "results": results}
}

// bulkOne applies the intent to one row and describes what came back.
func (s *Server) bulkOne(row *db.Destination, enabled bool) bulkDestResult {
	res := bulkDestResult{ID: row.ID, Name: row.Name, Platform: row.Platform}

	ctl := s.applyDestinationEnabled(row.ID, enabled)
	switch {
	case ctl.StoreErr != nil:
		// Overwhelmingly db.ErrNotFound: the row was deleted between the list
		// above and this iteration, which a paced walk makes genuinely
		// reachable. One row's disappearance is not the other seven's problem.
		res.Outcome, res.Message = bulkFailed, ctl.StoreErr.Error()
		return res
	case ctl.ReconcileErr != nil:
		res.Outcome, res.Message = bulkFailed, ctl.ReconcileErr.Error()
		return res
	}

	res.State = ctl.Effect.State
	res.Outcome, res.Message = classifyBulkEffect(ctl.Effect.Error, ctl.Effect.Warning, enabled)
	return res
}

// classifyBulkEffect turns one destination's outcome into the word the operator
// reads, and it is a FUNCTION so that it can be tested against constructed
// effects rather than against whatever three identical fixtures happen to do.
//
// That is not a style preference. Inline, this was covered by a test that
// asserted only the SHAPE of the response -- row count, ids, that the outcome
// was one of five known words -- with three identical destinations that all
// took the same branch. Two mutations survived it: making a refusing
// destination report as cleanly started, and making every row report failed.
// Either would have shipped a bulk control whose per-row reporting, the entire
// reason the feature is not one boolean, was decorative.
func classifyBulkEffect(effectErr, effectWarn string, enabled bool) (bulkOutcome, string) {
	switch {
	case effectErr != "":
		// The destination's own fault text -- a URL the platform refuses, an
		// encoder that would not build. The write and the reconcile both
		// succeeded; this row still is not delivering.
		return bulkFailed, effectErr
	case effectWarn != "":
		return bulkWarned, effectWarn
	case enabled:
		return bulkStarted, ""
	default:
		return bulkStopped, ""
	}
}
