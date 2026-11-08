package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The YouTube stream picker's read: which reusable streams does this connected account's channel hold, and which
// destinations already publish to each.
//
// IT NEVER RETURNS A KEY OR AN INGEST ADDRESS. The response is built from oauth.StreamSummary, which has neither,
// and the "which stream is mine" question is answered here by matching stored keys inside the process: the answer
// leaves as a boolean and as destination ids and names. A stream key is a credential for publishing to the
// channel, and a list a read-scope viewer could fetch would otherwise hand it out.

// ytStreamUse is another destination publishing to a listed stream.
type ytStreamUse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ytStreamRow is one stream in the picker's answer.
type ytStreamRow struct {
	oauth.StreamSummary
	// Current is true for the stream the queried destination publishes with (its stored key) or has pinned.
	Current bool `json:"current,omitempty"`
	// UsedBy are the OTHER destinations on this account that publish to this stream, matched by key or pinned id.
	UsedBy []ytStreamUse `json:"usedBy,omitempty"`
}

// handleYouTubeStreams answers GET /platforms/accounts/{id}/youtube/streams[?destinationId=N].
func (s *Server) handleYouTubeStreams(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	var self int64
	if v := r.URL.Query().Get("destinationId"); v != "" {
		if self, err = strconv.ParseInt(v, 10, 64); err != nil || self < 0 {
			writeError(w, http.StatusBadRequest, "destinationId must be a destination id")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	acct, err := s.tokenFor(ctx, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	lister, ok := oauth.As[oauth.StreamLister](s.providers, acct.Platform)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false,
			"reason": "polyemesis lists reusable streams only for YouTube"})
		return
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var mine []*db.Destination
	var keys []string
	for _, d := range rows {
		if d.Platform == acct.Platform && d.AccountID != nil && *d.AccountID == id {
			mine = append(mine, d)
			keys = append(keys, d.StreamKey)
		}
	}
	streams, err := lister.ListStreams(ctx, acct.AccessToken, keys)
	if err != nil {
		writeError(w, http.StatusBadGateway, scrubSourceKeys(err.Error(), keys))
		return
	}
	out := make([]ytStreamRow, 0, len(streams))
	for _, st := range streams {
		row := ytStreamRow{StreamSummary: st}
		for i, d := range mine {
			pinned := d.Compliance.YouTube != nil && d.Compliance.YouTube.StreamID == st.ID
			held := false
			for _, k := range st.MatchedKeys {
				if k == i {
					held = true
				}
			}
			if !held && !pinned {
				continue
			}
			if d.ID == self {
				row.Current = true
				continue
			}
			row.UsedBy = append(row.UsedBy, ytStreamUse{ID: d.ID, Name: d.Name})
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "streams": out})
}

// scrubSourceKeys removes every held key from a message.
func scrubSourceKeys(msg string, keys []string) string {
	for _, k := range keys {
		msg = scrubSourceKey(msg, k)
	}
	return msg
}
