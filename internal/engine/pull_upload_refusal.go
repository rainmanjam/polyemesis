package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The inherited pull source, re-examined where it is actually dialled.
//
// #201 put a gate on the SAVE and #255 was filed for what a save-time gate
// cannot reach: a pull source configured before the gate existed, or by a route
// that never went through it, keeps running with nothing re-examining it. And
// unlike a playlist item -- refused at the save AND again by the normalise
// worker before it spends a transcode -- a pull source had only the one gate.
// sources.go said so in the code: "it does not gate. The gate is on the save."
//
// THE ANSWER IS NOT THE SAME FOR THE TWO BAD STATES, and the split is the whole
// substance of the decision. Written out here because the next person to touch
// this will otherwise collapse the branches back together -- which is precisely
// what a mechanical merge did two PRs ago, producing the self-contradicting
// sentence "stored without being checked (no video or audio stream this server
// can play); upload it again": a reason only an inspection could produce,
// prescribing the one action that cannot fix a permanent refusal.
//
// UNCHECKED WARNS AND KEEPS STREAMING. An unverified verdict is a fact about
// THIS SERVER and never about the file. All four reasons say so in their own
// words -- uploads.ReasonNoProber is "this server had no ffprobe available when
// the file arrived" -- and a file ffprobe DID read and reject never becomes an
// unverified verdict at all, because probeUpload answers 400 and the bytes are
// never stored. So on an install with no ffprobe, EVERY upload looks unchecked
// by construction, and a fail-closed gate on that condition is not a check
// keyed to bad media: it is a kill switch keyed to this server's toolchain,
// whose blast radius is every file:// pull ingest on the box, all at once, at
// 3am, arriving as a stopped stream and a log line. That case is reported
// instead -- api.Server.pullUploadUnchecked puts the server's own sentence in
// the /sources response, and the card renders it beside the running badge.
//
// A REFUSAL IS NOT THAT, and the fail-open argument does not survive contact
// with it. uploads.OutcomeRefused is "a statement about the FILE, it is
// permanent, and trying again is not a remedy". It can only exist where an
// inspection actually RAN and rejected the bytes, so it cannot be an
// install-wide condition -- the missing-ffprobe blast radius, which was the
// entire shape of the outage being avoided, is inapplicable to it. The bounded
// worst case weakens too: what airs is not "the wrong one of your own files"
// but bytes this server has already read and concluded are not media. So this
// one stops the ingest.
//
// NO SUBPROCESS, WHICH IS WHY THIS CAN LIVE ON THE RECONCILE PATH AT ALL. #201
// refused to push the format allowlist into the engine because that means an
// ffprobe behind IngestArgs, on a function that runs on every source switch and
// every supervisor respawn. This reads the verdict sidecar that is already on
// disk. internal/ffmpeg/build.go's objection was never the cost of the probe --
// it was the fail-closed outcome, and that cost is paid here deliberately and
// only for the state that has been shown to be about the file.

// pullUploadRefusal reports why this engine must not dial pullURL, or "".
//
// subject completes the sentence -- "this source" for the primary listener,
// "the backup pull source" for the standby -- because uploads.Objection returns
// everything except whose problem it is.
//
// IT ASKS THROUGH uploads.UploadFromPullURL, WHICH ASKS ffmpeg.PullFilePath.
// That is not incidental. #266 closed a bypass where four spellings of one URL
// -- "file://uploads//show.ts", "file://./uploads/show.ts" and two more --
// produced one identical -i argument and four different gate answers, because
// each gate re-parsed the string its own way. PullFilePath is the normalisation
// that BUILDS the -i argument, so keying on it means any spelling producing a
// given input gets one verdict by construction. A check here that parsed the
// URL itself would inherit the bypass that PR closed.
func (e *Engine) pullUploadRefusal(subject, pullURL string) string {
	name, ok := uploads.UploadFromPullURL(pullURL)
	if !ok {
		// Not a pull from a stored upload: rtsp://, https://, or a file://
		// pointing elsewhere under the data directory. Not this function's
		// business. ffmpeg.ValidatePullURL decides whether it is legal at all.
		return ""
	}
	dir := strings.TrimSpace(e.cfg.DataDir)
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	// Built only for a pull source that names an upload, exactly as the card's
	// report is, which is what keeps a reconcile of an SRT or RTMP programme
	// from paying an os.MkdirAll.
	store, err := uploads.New(abs)
	if err != nil {
		// FAILS OPEN, unlike all three save-time gates, and the asymmetry is
		// deliberate. A gate on a save can refuse and hand the operator the
		// error; the only refusal available here is taking a running programme
		// off air, and "the data directory could not be opened" is a fact about
		// this server -- the very category of condition the paragraphs above
		// give for NOT stopping an ingest. Refusing here would reintroduce the
		// install-wide kill switch through the back door.
		return ""
	}
	o, bad := store.Objection(name)
	if !bad || !o.Refused {
		// !bad covers verified and the no-record-at-all case. !o.Refused is the
		// unchecked one, and it is deliberately not stopped here: it is warned
		// about on the card. See the header -- these two lines ARE the decision.
		return ""
	}
	return fmt.Sprintf("%s pulls from %q, which %s; %s", subject, name, o.What, o.Remedy)
}
