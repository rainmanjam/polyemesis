package api

import (
	"net/http"
	"os"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// GET /failover/playlist answers the question engine.playlistItemsReady only
// ever whispers to a log line: which item is keeping the playlist off air, and
// why.
//
// ITS OWN ENDPOINT, NEVER A FIELD ON THE SETTINGS BLOB. handleGetSettings
// serves db.Settings straight out, and the settings page GETs that whole
// document and PUTs it back verbatim on every save (see handlePutSettings and
// savePlaylist in playlist_normalise_test.go). A derived, read-only field
// living in that payload is state the UI would round-trip as though it were
// configuration -- exactly how B1's lockout happened: a stale playlist item
// riding along in a PUT bricked every subsequent settings save with no
// in-product way to clear it. See handlePutMQTTPassword for the same argument
// applied to a different field. Readiness is OBSERVED, not configured, so it
// gets its own GET and never appears in a PUT body.

// Playlist item readiness states.
const (
	// playlistItemReady means a derivative this profile produced already
	// exists -- the same and only question engine.playlistItemsReady asks.
	playlistItemReady = "ready"
	// playlistItemTranscoding means the derivative does not exist yet, but a
	// job to produce one is queued, running or deferred.
	playlistItemTranscoding = "transcoding"
	// playlistItemAttention means neither of the above, and Detail says why:
	// the upload is gone, the last attempt failed, or nothing has been queued
	// for it at all.
	playlistItemAttention = "attention"
)

// PlaylistItemStatus is one playlist entry's readiness, as observed right now.
type PlaylistItemStatus struct {
	Upload string `json:"upload"`
	State  string `json:"state"` // "ready" | "transcoding" | "attention"
	// Detail names the cause when State is "attention". Empty otherwise: a
	// ready or transcoding item needs no explanation, and the spec's worst
	// outcome is a playlist that stays off air with NOTHING telling the
	// operator which item or why -- Detail is the fix, so it is never left
	// blank for the one state where an operator would ask.
	Detail string `json:"detail,omitempty"`
	// Warning carries a fact about the item that does NOT stop it going to
	// air, which is why it is a separate field from Detail rather than a
	// fourth State.
	//
	// It exists for exactly one situation today: the re-verify job recorded a
	// refusal against an upload whose derivative already exists. State stays
	// "ready" and the programme keeps playing, because the derivative was
	// transcoded from those bytes and is itself intact -- taking a running
	// item off air over a re-inspection of its SOURCE would black out a
	// programme to report a fact the operator can act on at their leisure.
	//
	// The split is the one this file already draws: readiness answers "may
	// this go to air", and a refusal is not an answer to that question once a
	// derivative exists. Compare playlistUploadProblems, which DOES refuse the
	// same upload -- but only as an item the operator is introducing, where
	// nothing is on air to interrupt.
	Warning string `json:"warning,omitempty"`
}

// PlaylistStatus is the whole playlist's readiness.
type PlaylistStatus struct {
	// Ready is true only when every item is "ready". An empty playlist is not
	// ready -- there is nothing for the selector to put on air, which is the
	// same reading playlistSig gives it: an enabled playlist with no items
	// never starts the tier.
	Ready bool                 `json:"ready"`
	Items []PlaylistItemStatus `json:"items"`
}

// handlePlaylistStatus reports why the playlist tier is, or is not, available
// to go on air -- one entry per configured item, in play order.
func (s *Server) handlePlaylistStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := settings.Failover.Playlist.Items
	status := PlaylistStatus{Items: make([]PlaylistItemStatus, 0, len(items))}
	// An empty playlist is vacuously NOT ready: Ready starts true only once
	// there is at least one item to be ready, and the loop below can only ever
	// turn it false, never back on.
	status.Ready = len(items) > 0
	for _, item := range items {
		st := s.playlistItemStatus(db.PlaylistUploadName(item.Upload))
		if st.State != playlistItemReady {
			status.Ready = false
		}
		status.Items = append(status.Items, st)
	}
	writeJSON(w, http.StatusOK, status)
}

// uploadRefusal reports the recorded reason this upload was refused, and
// whether it was refused at all.
//
// IT RETURNS THE REASON, NOT A SENTENCE. The two callers in playlistItemStatus
// share the finding and differ on the remedy -- one has a derivative on air to
// keep, the other has nothing that can ever play -- and a helper that picked
// the wording would force the caller's situation into it. This is the split
// uploadObjection already draws in pull_verdict.go: the verdict is a fact, the
// remedy is a function of the state the caller is in.
//
// AN UNCHECKED UPLOAD IS NOT REFUSED and returns false here. That asymmetry is
// #255's, applied to the same question one layer up: an upload nobody managed
// to inspect is a fact about THIS SERVER -- on a box with no ffprobe every
// upload looks unchecked -- while a refusal exists only where an inspection
// ran and read the bytes. Reporting the first as a refusal would put a warning
// on every item on such a box.
//
// A store that cannot be opened yields no refusal rather than a false one.
// This is a reporting path: it says what it observed, and "I could not look"
// is not an accusation against the operator's file. The states that must fail
// closed do so where they are enforced -- playlistUploadProblems returns an
// error when it cannot build a store, because that one gates a write.
func (s *Server) uploadRefusal(name string) (string, bool) {
	store, err := s.uploadStore()
	if err != nil {
		return "", false
	}
	v, recorded := store.Verdict(name)
	if !recorded || v.Outcome != uploads.OutcomeRefused {
		return "", false
	}
	return v.Reason, true
}

// playlistItemStatus answers ready / transcoding / attention for one upload
// name, already trimmed by db.PlaylistUploadName.
func (s *Server) playlistItemStatus(name string) PlaylistItemStatus {
	st := PlaylistItemStatus{Upload: name}

	// READ BEFORE THE READY SHORT-CIRCUIT, because both branches need it and
	// only one of them is reachable per call. Costs one small ReadFile per
	// item, the same order as the Stat below, and playlists are short.
	refusal, refused := s.uploadRefusal(name)

	// READY asks the one question engine.playlistItemsReady asks: is there a
	// NON-EMPTY derivative this profile produced. os.Stat, never store.Resolve
	// -- Resolve is a shape check that never touches the disk, and this is a
	// question about existence.
	//
	// Zero-length is treated as absent, and all four readers of this path say
	// so identically: here, enqueuePlaylistNormalisation, RunNormalise's
	// already-normalised skip, and engine.playlistItemsReady. The size clause
	// was missing from the engine once, and the cost of that disagreement is
	// precise: this endpoint said "transcoding", the engine said "ready", so
	// the operator read amber here while the tier respawn-looped on an empty
	// file. An endpoint that explains why the playlist is off air is worth
	// nothing if it is answering a different question from the gate.
	if fi, err := os.Stat(playlistmedia.DerivativePath(s.cfg.DataDir, name)); err == nil && fi.Size() > 0 {
		st.State = playlistItemReady
		if refused {
			// Deliberately does not touch State. See Warning's declaration.
			st.Warning = "\"" + name + "\" was inspected and refused (" + refusal +
				"), but the copy already on air was made before that; " +
				"replace the file or remove this item when convenient"
		}
		return st
	}

	// TRANSCODING: an active normalisation job for this upload. Target, not a
	// join on Upload -- playlistmedia.NormaliseTarget is the same key
	// NewNormaliseJob submits under and the queue's Unique fold dedupes on, so
	// this asks the identical question the queue itself would answer for "is
	// this already being worked".
	//
	// NOT WHEN THE SOURCE IS REFUSED, and this clause is the whole reason the
	// refusal is read at the top of the function rather than down in the
	// attention block where it is reported.
	//
	// Saving a playlist enqueues a normalisation for every item, so a refused
	// upload reliably HAS an active job -- one that will run, fail on the
	// format allowlist, and be re-queued by the next save. Answering
	// "transcoding" for it is the amber "working on it, wait" reading, given
	// to an operator whose file can never finish. The queue state is true and
	// the conclusion drawn from it is false, which is worse than saying
	// nothing.
	target := playlistmedia.NormaliseTarget(name)
	if s.jobq != nil && !refused {
		active, err := s.jobq.List(jobs.Filter{
			States: []jobs.State{jobs.StateQueued, jobs.StateRunning, jobs.StateDeferred},
			Kinds:  []jobs.Kind{playlistmedia.KindNormalise},
			Target: target,
		})
		if err == nil && len(active) > 0 {
			st.State = playlistItemTranscoding
			return st
		}
	}

	// ATTENTION, and Detail must name ONE of the three causes the spec allows:
	// the upload is missing, the last attempt failed, or nothing is queued.
	// Checked in that order because a missing upload is the root cause even
	// when a stale failed job also exists for the same target, and because it
	// is the one cause a re-queue cannot fix by itself.
	if store, err := s.uploadStore(); err == nil {
		if path, err := store.Resolve(name); err == nil {
			if _, err := os.Stat(path); err != nil {
				st.State = playlistItemAttention
				st.Detail = "the upload \"" + name + "\" no longer exists; " +
					"upload the file again or remove this item from the playlist"
				return st
			}
		} else {
			// Resolve refused the name outright -- an escape attempt or an
			// empty/invalid one. Settings.Validate is supposed to keep this
			// from ever being stored, but this endpoint reports what it
			// observes rather than trusting that upstream check held.
			st.State = playlistItemAttention
			st.Detail = "\"" + name + "\" is not a usable upload name: " + err.Error()
			return st
		}
	}

	// REFUSED OUTRANKS A FAILED JOB, and for the same reason a missing upload
	// outranks one: it is the root cause, and it is the one a re-queue cannot
	// fix. The normalisation below failed BECAUSE the source was refused, so
	// reporting "normalisation failed: ..." here would name the symptom and
	// send the operator to retry a job that will fail again for the same
	// reason.
	//
	// The remedy differs from the ready case above even though the finding is
	// identical, which is why the sentence is built here rather than returned
	// by uploadRefusal: there is no copy on air to keep, so this one is not
	// something to do "when convenient".
	if refused {
		st.State = playlistItemAttention
		st.Detail = "\"" + name + "\" was inspected and refused (" + refusal +
			"), so it cannot be normalised; sending the same file again will " +
			"not change that -- replace the file or remove this item"
		return st
	}

	if s.jobq != nil {
		failed, err := s.jobq.List(jobs.Filter{
			States: []jobs.State{jobs.StateFailed},
			Kinds:  []jobs.Kind{playlistmedia.KindNormalise},
			Target: target,
			Limit:  1,
		})
		if err == nil && len(failed) > 0 {
			st.State = playlistItemAttention
			st.Detail = "normalisation failed"
			if failed[0].Error != "" {
				st.Detail += ": " + failed[0].Error
			}
			return st
		}
	}

	st.State = playlistItemAttention
	if s.jobq == nil {
		st.Detail = "the background job queue is not running on this server, " +
			"so this item cannot be normalised"
	} else {
		st.Detail = "not yet queued for normalisation"
	}
	return st
}
