package multitrack

import (
	"context"
)

// Outcome is what one go-live negotiation decided, and it is deliberately not
// an (Outcome, error) pair.
//
// NOT NEGOTIATING IS AN ORDINARY RESULT, NOT A FAILURE. Twitch refuses any
// client without a supported GPU, and polyemesis is built to be installed on
// the operator's own server -- a rented VPS has none. So on most installs the
// fallback IS the path, every time, for ever. A function that returned an error
// for it would make the ordinary case look broken: it would be logged at error
// level, counted as a fault, and retried by somebody who assumed a non-nil
// error meant something had gone wrong. Nothing here can fail in a way the
// caller must handle -- the caller either publishes to Target or publishes to
// the destination's own URL, and both are correct.
type Outcome struct {
	// Target is where to publish, valid only when Use is true. Its Key is a
	// CREDENTIAL and is the minted one; see the Use comment.
	Target Target

	// Use reports whether the caller must publish to Target instead of the
	// destination's stored URL.
	//
	// A TRUE HERE IS NOT EVIDENCE THE STREAM KEY IS VALID. Measured: the live
	// endpoint returned a successful negotiation, with a full ladder and a
	// minted key, for a plainly invalid stream key. Validation happens at
	// PUBLISH, not at negotiation. So a caller must not read a successful
	// Negotiate as "the credential works" -- the failure will arrive later, at
	// the ingest, and anything that reported the key as verified here will have
	// made it harder to diagnose rather than easier.
	//
	// WHEN THIS IS TRUE THE MINTED KEY IS MANDATORY, not preferred. Twitch
	// answers a successful negotiation with a new 312-character stream key that
	// carries the agreed ladder signed inside it, ending with the operator's
	// original. Publishing with the operator's own key instead would connect --
	// which is what makes this dangerous rather than merely wrong -- and send a
	// ladder the ingest never agreed to. Target.Key is that minted value; it
	// must not be reassembled from the destination row.
	Use bool

	// Verdict is Twitch's answer, for a caller that wants to distinguish "we
	// never asked" from "we asked and were refused". Refused whenever Use is
	// false.
	Verdict Verdict

	// Note is ONE sentence for the operator, and it is always set -- including
	// on the quiet success path, where it says nothing happened.
	//
	// IT CARRIES NO CREDENTIAL, and that is enforced here rather than inherited.
	// See the scrubbing closure in Negotiate for what is defence and what is a
	// measurement -- the distinction matters, and an earlier version of this
	// comment got it wrong in a way that would have sent a reader to the wrong
	// field.
	//
	// It is a note rather than a warning on purpose. An operator on a GPU-less
	// server has not misconfigured anything and must not be shown a fault.
	Note string

	// Divergences are the places the negotiated configuration departs from what
	// was asked for. ADVISORY ONLY: they annotate, they never block. An optional
	// VOD track must never veto a working broadcast, so a divergence is reported
	// beside a destination that is publishing, not instead of one.
	Divergences []Divergence
}

// noteNoGPU is the fallback sentence for the majority install, and its wording
// is the whole point: nothing here is a fault.
const noteNoGPU = "Enhanced Broadcasting was not requested: it needs a supported GPU, " +
	"which this server has not been told it has, so this destination is publishing to the ordinary Twitch ingest."

// Negotiate asks Twitch for an Enhanced Broadcasting configuration and decides
// whether to publish to it.
//
// It is called once per destination per go-live, on the path between the
// operator pressing the button and anything reaching a viewer, which is why
// Client's timeout matters and why the no-GPU case below does not make the call
// at all.
//
// THE NO-GPU SHORT CIRCUIT IS A MEASURED SHORTCUT, NOT AN ASSUMPTION. Twitch
// was observed refusing, by name, a request that sent no GPU information at all
// ("Your broadcast software (polyemesis) did not send GPU Information"), an
// Intel iGPU, an unrecognised vendor ID, and an out-of-date driver. A request
// with no GPU therefore has one possible answer, and spending a network round
// trip at go-live to be told it is the answer we already know would delay every
// broadcast on every GPU-less install to learn nothing. Delete these four lines
// and the behaviour is identical but slower -- which is what makes it safe to
// delete if Twitch ever changes its mind.
//
// Nothing here measures hardware; Capabilities explains why not. The GPU facts
// come from the operator's configuration, and their absence is the default.
func Negotiate(ctx context.Context, c *Client, streamKey string, a Ask) Outcome {
	// EVERY note leaves through here, scrubbed. THIS IS DEFENCE, NOT A FIX FOR AN
	// OBSERVED LEAK, and the difference is worth stating precisely because the
	// first version of this comment claimed the latter and was wrong.
	//
	// WHAT WAS MEASURED, against the live endpoint with a distinctive canary
	// sent as `authentication`:
	//
	//   - status.html_en_us echoes client.name, NOT the stream key. A refusal
	//     for missing canvases came back naming the broadcast software, and the
	//     canary did not appear in it under any refusal that could be produced.
	//   - The key that does come back is in ingest_endpoints[].authentication:
	//     the 312-character MINTED key on the success path, which ends with the
	//     original. That is the credential this response carries.
	//
	// So html_en_us is not a known key channel. It is scrubbed anyway because it
	// is ATTACKER-INFLUENCED TEXT FROM A THIRD PARTY that polyemesis renders to
	// an operator, and it is built by quoting request fields back -- a habit that
	// needs only one more field to become a leak. Scrubbing text we do not
	// control is cheap; discovering later that the habit grew is not.
	//
	// WHAT THIS CLOSURE DOES NOT PROTECT is the minted key, and nothing here
	// could: it never appears in a Note, only in Outcome.Target.Key. Registering
	// the original key as a secret does NOT cover the minted one either --
	// SecretSet.Scrub is a substring replace, so the original masks only the
	// minted key's last segment and leaves the signature and manifest in the
	// clear. Whoever publishes with Target.Key must register that value in its
	// own right; see engine.destSecrets and
	// TestTheMintedKeyIsMaskedWholeAndNotJustItsTail.
	out := func(o Outcome) Outcome {
		o.Note = scrub(o.Note, streamKey)
		return o
	}

	if len(a.Hardware.GPU) == 0 {
		return out(Outcome{Verdict: Refused, Note: noteNoGPU})
	}

	cfg, err := c.Fetch(ctx, streamKey, NewRequest(a))
	if err != nil {
		// Client.Fetch has already scrubbed the key out of every error it
		// builds, including the *url.Error the transport hands back -- which is
		// the shape that leaked in #310 and #324. Carried through as it stands
		// rather than re-wrapped with anything of ours, because the only things
		// this function knows that Fetch does not are the key and the target,
		// and neither may appear in a note.
		return out(Outcome{
			Verdict: Refused,
			Note: "Enhanced Broadcasting could not be negotiated, so this destination is publishing " +
				"to the ordinary Twitch ingest: " + err.Error(),
		})
	}

	verdict, advice := cfg.Verdict()
	if verdict == Refused {
		return out(Outcome{
			Verdict: Refused,
			Note: "Enhanced Broadcasting is not available for this broadcast, so this destination is " +
				"publishing to the ordinary Twitch ingest: " + advice,
		})
	}

	target, rerr := cfg.Resolve(streamKey)
	if rerr != nil {
		// A configuration that passed Verdict but carries no endpoint anyone can
		// publish to. Falling back rather than failing, for the same reason as
		// everywhere else here: the ordinary ingest works.
		return out(Outcome{
			Verdict: Refused,
			Note: "Enhanced Broadcasting returned a configuration with no usable ingest, so this " +
				"destination is publishing to the ordinary Twitch ingest: " + rerr.Error(),
		})
	}

	note := "Enhanced Broadcasting is in use for this destination."
	if advice != "" {
		// Advisory: Twitch agreed and said something about it. Shown, not acted
		// on -- obs-studio puts a modal here and offers to abort, and polyemesis
		// has no operator standing at the machine when a scheduled broadcast
		// starts.
		note = "Enhanced Broadcasting is in use for this destination, and Twitch added: " + advice
	}
	return out(Outcome{
		Target:      target,
		Use:         true,
		Verdict:     verdict,
		Note:        note,
		Divergences: Reconcile(a, cfg),
	})
}
