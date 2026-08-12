package uploads

import "fmt"

// The three-state answer to "may this consumer use this stored upload", spelled
// once for every consumer of "stored does not imply checked".
//
// IT LIVES HERE BECAUSE THE ENGINE HAS TO ASK IT TOO. The switch was written in
// internal/api (uploadObjection), which was right while all three callers were
// handlers: two save-time gates and the sentence on the source card. #255's
// decision adds a fourth caller in internal/engine, and internal/api imports
// internal/engine -- sourceView carries an engine.ListenerHealth -- so the
// engine cannot reach back for it. The alternatives were a second copy of the
// switch in the engine, which is exactly the drift uploadObjection's own
// comment was written to prevent, or moving it down to the package that owns
// what a Verdict MEANS. This is the second.

// Objection is why a consumer must not use a stored upload.
//
// What completes "...which %s" and Remedy is the sentence after it, so a caller
// supplies only its own subject. The remedy is part of the answer rather than
// of the caller because it is a function of WHICH state the upload is in and
// nothing else: OutcomeRefused is "a statement about the FILE, it is permanent,
// and trying again is not a remedy", so telling that operator to upload it
// again is advice that cannot work.
type Objection struct {
	What   string
	Remedy string
	// Refused separates the two bad states, and it is a field rather than
	// something a caller re-derives from Verdict because the WHOLE of #255's
	// decision turns on the distinction.
	//
	// The argument for letting an inherited unchecked source keep streaming is
	// that an unverified verdict is a fact about THIS SERVER and never about
	// the file -- every reason says so in its own words, ReasonNoProber most
	// plainly -- so an install with no ffprobe has every upload looking
	// unchecked, and a fail-closed engine gate on that condition is not a check
	// keyed to bad media but a kill switch keyed to a missing subprocess, with
	// every file:// pull ingest on the box inside its blast radius.
	//
	// THAT ARGUMENT DOES NOT SURVIVE CONTACT WITH A REFUSAL. A refusal can only
	// exist where an inspection actually RAN and rejected the bytes, so it
	// cannot be an install-wide condition, and the blast-radius objection is
	// simply inapplicable to it. That is why the two states diverge at the
	// engine: unchecked warns and keeps streaming, refused stops the ingest.
	// A caller that folds them back together is re-deciding #255 by accident.
	Refused bool
}

// Objection reports why a consumer must not use the stored upload named name,
// and whether there is an objection at all.
//
// RECORDED AS A PROBLEM, NOT "NOT RECORDED AS FINE". Every upload stored before
// verdicts existed has no record at all, and refusing those would strand media
// an operator has had for a year over a file that was never written. See
// Store.Verdict's second return, which exists to draw exactly this line.
func (s *Store) Objection(name string) (Objection, bool) {
	v, recorded := s.Verdict(name)
	switch {
	case !recorded:
		// Stored before verdicts existed. Allowed; see above.
		return Objection{}, false
	case v.Outcome == OutcomeRefused:
		return Objection{
			What:    fmt.Sprintf("was inspected and refused (%s)", v.Reason),
			Remedy:  "point it at a different file",
			Refused: true,
		}, true
	case !v.Verified():
		return Objection{
			What:   fmt.Sprintf("was stored without being checked (%s)", v.Reason),
			Remedy: "upload it again before pulling from it",
		}, true
	}
	return Objection{}, false
}
