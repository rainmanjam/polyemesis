package api

import (
	"net/http"
	"os"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
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

// playlistItemStatus answers ready / transcoding / attention for one upload
// name, already trimmed by db.PlaylistUploadName.
func (s *Server) playlistItemStatus(name string) PlaylistItemStatus {
	st := PlaylistItemStatus{Upload: name}

	// READY asks the one question engine.playlistItemsReady asks: does a
	// derivative this profile produced already exist. os.Stat, never
	// store.Resolve -- Resolve is a shape check that never touches the disk,
	// and this is a question about existence. Zero-length is treated as
	// absent, matching enqueuePlaylistNormalisation's own check, so a
	// derivative left truncated by an interrupted write is not mistaken for a
	// finished one.
	if fi, err := os.Stat(playlistmedia.DerivativePath(s.cfg.DataDir, name)); err == nil && fi.Size() > 0 {
		st.State = playlistItemReady
		return st
	}

	// TRANSCODING: an active normalisation job for this upload. Target, not a
	// join on Upload -- playlistmedia.NormaliseTarget is the same key
	// NewNormaliseJob submits under and the queue's Unique fold dedupes on, so
	// this asks the identical question the queue itself would answer for "is
	// this already being worked".
	target := playlistmedia.NormaliseTarget(name)
	if s.jobq != nil {
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
