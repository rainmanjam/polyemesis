package api

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The third consumer of "stored does not imply checked".
//
// #118 made "stored but never inspected" a state the product can SAY -- a
// uploads.Verdict beside the file, `verified` on every listing -- and taught
// two consumers to stop assuming stored implied checked:
// playlistUploadProblems refuses a playlist item the operator is introducing
// whose upload carries a recorded "nobody read this", and playlistmedia's
// normalise worker re-runs ffmpeg.ProbeFile's format allowlist on whatever it
// is handed.
//
// THE PULL SOURCE WAS THE ONE THAT WAS MISSED. uploads.File.PullURL is
// "file://uploads/<name>", it is offered copyable in the Library for exactly
// this purpose, and pasting it into Settings -> Ingest -> Pull hands the path
// to the ENGINE's FFmpeg -- a different code path from ffmpeg.ProbeFile,
// carrying neither the selfContainedFormats allowlist nor the
// -protocol_whitelist file pin. So an upload the operator's own dropped
// connection kept this server from inspecting could be routed to air without
// anything having read a byte of it.
//
// WHY IT IS A REAL GATE AND NOT A DUPLICATE OF THE LIBRARY'S MARKER. The marker
// is good and it was the argument for deferring this: the operator has to copy
// the URL out of a row that says "Not checked". But #201 also names what
// removes the marker from the path -- a pull source configured by automation
// from a listing, which never sees a row at all -- and the settings API is
// reachable by exactly that. A warning in the UI is not a check in the server.
//
// WHY HERE AND NOT IN internal/db.Settings.Validate, which is where every other
// pull-URL rule lives (ffmpeg.ValidatePullURL is called from there twice). The
// same reason playlistUploadProblems is here: this needs an uploads store, and
// putting one behind Settings.Validate would put an os.MkdirAll and a stat
// behind every db.GetSettings -- roughly twenty callers, several of them
// per-request handlers. That is a defect this sub-project has already had to
// undo once.
//
// THE REAL FIX IS STILL BIGGER, and #201 says so: pushing the allowlist into
// the engine's file-input path would give one gate instead of a per-consumer
// list. This is the per-consumer gate, and it closes the reachable hole today
// without touching the engine's input construction.

// pullSourceUploadProblems reports why a pull source the operator is
// INTRODUCING cannot be used, or nil.
//
// want is the document about to be stored; storedPrimary and storedBackup are
// the two pull URLs already saved, captured before the incoming payload was
// decoded over them.
//
// SCOPED TO WHAT THIS SAVE CHANGES, exactly as playlistUploadProblems is, and
// for the same reason spelled out there: validation rejects what the operator
// is introducing and must not punish them for pre-existing state. The settings
// page PUTs the whole document back on every unrelated change, so an
// unconditional check would refuse a save about an alert threshold because a
// pull source configured last year names a file that was stored unchecked --
// with the operator given no way to tell which of the two things they touched
// was the problem. An unchanged URL is state they inherited.
//
// A URL that does not name an upload at all -- rtsp://, https://, a file://
// pointing somewhere else under the data directory -- is not this function's
// business and returns nothing. ffmpeg.ValidatePullURL, called from
// Settings.Validate, is what decides whether it is a legal source.
func (s *Server) pullSourceUploadProblems(want db.Settings, storedPrimary, storedBackup string) error {
	type candidate struct {
		where  string
		url    string
		stored string
	}
	// The backup is checked even when Enabled is false. Storing a source that
	// is off today and switched on during an outage is the case this exists
	// for: the moment it matters is the moment nobody is going to re-read the
	// Library row.
	for _, c := range []candidate{
		{"the pull source", want.Ingest.Pull.URL, storedPrimary},
		{"the backup pull source", want.Failover.Backup.Pull.URL, storedBackup},
	} {
		if c.url == c.stored {
			continue
		}
		name, ok := uploads.UploadFromPullURL(c.url)
		if !ok {
			continue
		}
		store, err := s.uploadStore()
		if err != nil {
			// Fail closed, as playlistUploadProblems does. Not being able to
			// ask is not the same as the answer being yes.
			return fmt.Errorf("%s cannot be checked: %w", c.where, err)
		}
		// RECORDED AS UNVERIFIED, NOT "NOT RECORDED AS VERIFIED", which is the
		// same distinction playlistUploadProblems draws and for the same
		// reason: every upload stored before verdicts existed has no record at
		// all, and refusing those would strand media an operator has had for a
		// year over a file that was never written. See uploads.Store.Verdict's
		// second return.
		//
		// A REFUSAL IS SPLIT OUT for the reason playlistUploadProblems gives at
		// length: it refuses the same save, but "upload it again" is advice that
		// cannot work for a file that was read and is not media.
		v, recorded := store.Verdict(name)
		switch {
		case !recorded:
			// Stored before verdicts existed. Allowed; see above.
		case v.Outcome == uploads.OutcomeRefused:
			return fmt.Errorf("%s names %q, which was inspected and refused (%s); "+
				"point it at a different file", c.where, name, v.Reason)
		case !v.Verified():
			return fmt.Errorf("%s names %q, which was stored without being checked (%s); "+
				"upload it again before pulling from it", c.where, name, v.Reason)
		}
	}
	return nil
}
