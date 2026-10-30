package api

import (
	"encoding/json"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// RECORD KEY: the install-wide recording toggle, GET and POST /api/v1/recording.
//
// Recording is one switch for the whole install. The recorder follows the
// Recording block of the settings document, and the free-space floor can stop
// it on its own while the switch is still on. A Stream Deck key needs both
// facts, so GET says which one it is looking at:
//
//	off          recording.enabled is false
//	halted_disk  enabled, but the free-space floor has stopped the recorder
//	recording    enabled, the floor is clear, and an engine has a recorder child
//	not_running  enabled and the floor is clear, but no recorder child is up
//	             (no ingest is live, or the recorder has not been started yet)
//
// Off is checked first: a switch that is off is off whatever the disk says.
//
// POST writes Recording.Enabled and nothing else. It goes through UpdateSettings,
// which holds the store's settings lock across read, merge and write, so it
// cannot lose a concurrent PUT /settings that changed another block, and it
// takes settingsMu the same way the settings save does. It then runs the same
// reconcile the settings save runs, so the recorder starts or stops now rather
// than on the next restart.
//
// The write is admin-scoped by method, like every other POST in this group.

// recordingState is the answer to GET and to a successful POST.
type recordingState struct {
	Enabled     bool   `json:"enabled"`
	State       string `json:"state"`
	Halted      bool   `json:"halted"`
	Reason      string `json:"reason,omitempty"`
	InstallWide bool   `json:"installWide"`
}

const (
	recordingStateOff        = "off"
	recordingStateRecording  = "recording"
	recordingStateNotRunning = "not_running"
	recordingStateHaltedDisk = "halted_disk"
)

// recordingStateFor derives the four states from the setting and the engines.
// It reads the engines' own answers rather than inferring from the setting:
// the setting is the intent, the recorder child is the fact.
func (s *Server) recordingStateFor(enabled bool) recordingState {
	out := recordingState{Enabled: enabled, InstallWide: true}
	if !enabled {
		out.State = recordingStateOff
		return out
	}
	if verdict := s.storageVerdict(); verdict.Halted {
		out.State = recordingStateHaltedDisk
		out.Halted = true
		out.Reason = verdict.Reason
		return out
	}
	out.State = recordingStateNotRunning
	if s.mgr != nil {
		for _, e := range s.mgr.Engines() {
			if e.RecorderRunning() {
				out.State = recordingStateRecording
				break
			}
		}
	}
	return out
}

// handleGetRecording answers GET /api/v1/recording. A read token may ask.
func (s *Server) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.recordingStateFor(settings.Recording.Enabled))
}

// handleSetRecording answers POST /api/v1/recording {"enabled": bool}.
func (s *Server) handleSetRecording(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, `"enabled" is required`)
		return
	}

	// The same boundary the settings save holds, so a media delete waiting on
	// settingsMu sees either the whole save or none of it.
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	enabled := *req.Enabled
	if _, err := s.store.UpdateSettings(func(st *db.Settings) error {
		st.Recording.Enabled = enabled
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if rw := s.reconcileNow("the recording"); rw != "" {
		writeError(w, http.StatusInternalServerError, rw)
		return
	}
	writeJSON(w, http.StatusOK, s.recordingStateFor(enabled))
}
