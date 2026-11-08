package api

// The operator's notes about a viewer: free text and a few tags, kept locally
// beside the user card. GET/PUT/DELETE /chat/users/notes.
//
// These are personal data about people who chatted, so: GET is read-denied like
// /chat/users, PUT and DELETE are mutations (a read token cannot reach them),
// and nothing here is published to the event stream, an overlay, a hook or a
// platform. The only response that is not one viewer's own record is the
// tagged-viewers list (GET without authorId), which carries tags and never the
// note text.

import (
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// viewerNoteBody is a viewer note on the wire. Note and Tags are what the
// operator wrote; UpdatedAt and UpdatedBy are when and by whom.
type viewerNoteBody struct {
	Platform  db.Platform `json:"platform"`
	Account   string      `json:"account,omitempty"`
	AuthorID  string      `json:"authorId"`
	Note      string      `json:"note"`
	Tags      []string    `json:"tags"`
	UpdatedAt *time.Time  `json:"updatedAt,omitempty"`
	UpdatedBy string      `json:"updatedBy,omitempty"`
}

// viewerTagsBody is one tagged viewer in the list answer: tags only.
type viewerTagsBody struct {
	Platform db.Platform `json:"platform"`
	Account  string      `json:"account,omitempty"`
	AuthorID string      `json:"authorId"`
	Tags     []string    `json:"tags"`
}

// viewerNoteRequest is PUT's body.
type viewerNoteRequest struct {
	Note string   `json:"note"`
	Tags []string `json:"tags"`
}

// viewerNoteKey reads the addressing every method shares.
func viewerNoteKey(r *http.Request) (db.Platform, string, string) {
	q := r.URL.Query()
	return db.Platform(strings.TrimSpace(q.Get("platform"))), strings.TrimSpace(q.Get("account")),
		strings.TrimSpace(q.Get("authorId"))
}

// handleViewerNoteGet answers one viewer's note (an empty one when there is
// none, so the card always has something to edit), or, with no authorId, the
// list of tagged viewers.
func (s *Server) handleViewerNoteGet(w http.ResponseWriter, r *http.Request) {
	platform, account, authorID := viewerNoteKey(r)
	if authorID == "" {
		list, err := s.store.ListViewerTags()
		if err != nil {
			writeStoreError(w, err)
			return
		}
		out := make([]viewerTagsBody, 0, len(list))
		for _, v := range list {
			out = append(out, viewerTagsBody{Platform: v.Platform, Account: v.Account, AuthorID: v.AuthorID, Tags: v.Tags})
		}
		writeJSON(w, http.StatusOK, map[string]any{"viewers": out, "presetTags": db.ViewerPresetTags})
		return
	}
	if platform == "" {
		writeError(w, http.StatusBadRequest, "platform and authorId are required")
		return
	}
	n, ok, err := s.store.GetViewerNote(platform, account, authorID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, viewerNoteBody{Platform: platform, Account: account, AuthorID: authorID, Tags: []string{}})
		return
	}
	writeJSON(w, http.StatusOK, viewerNoteToBody(n))
}

func viewerNoteToBody(n db.ViewerNote) viewerNoteBody {
	at := n.UpdatedAt.UTC()
	return viewerNoteBody{Platform: n.Platform, Account: n.Account, AuthorID: n.AuthorID,
		Note: n.Note, Tags: n.Tags, UpdatedAt: &at, UpdatedBy: n.UpdatedBy}
}

// handleViewerNotePut replaces a viewer's note and tags. Saving it empty
// removes it, so there is no stored note that says nothing.
func (s *Server) handleViewerNotePut(w http.ResponseWriter, r *http.Request) {
	platform, account, authorID := viewerNoteKey(r)
	if platform == "" || authorID == "" {
		writeError(w, http.StatusBadRequest, "platform and authorId are required")
		return
	}
	var req viewerNoteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	note, tags, err := db.NormalizeViewerNote(req.Note, req.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the note or its tags are over the limit: at most "+
			"1000 characters, 10 tags of up to 32 characters each")
		return
	}
	by := "token"
	if p, ok := principalFrom(r.Context()); ok && p.username != "" {
		by = p.username
	}
	n := db.ViewerNote{Platform: platform, Account: account, AuthorID: authorID,
		Note: note, Tags: tags, UpdatedAt: time.Now(), UpdatedBy: by}
	if err := s.store.PutViewerNote(n); err != nil {
		writeStoreError(w, err)
		return
	}
	if note == "" && len(tags) == 0 {
		writeJSON(w, http.StatusOK, viewerNoteBody{Platform: platform, Account: account, AuthorID: authorID, Tags: []string{}})
		return
	}
	writeJSON(w, http.StatusOK, viewerNoteToBody(n))
}

// handleViewerNoteDelete removes a viewer's note. Missing is success.
func (s *Server) handleViewerNoteDelete(w http.ResponseWriter, r *http.Request) {
	platform, account, authorID := viewerNoteKey(r)
	if platform == "" || authorID == "" {
		writeError(w, http.StatusBadRequest, "platform and authorId are required")
		return
	}
	if err := s.store.DeleteViewerNote(platform, account, authorID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
