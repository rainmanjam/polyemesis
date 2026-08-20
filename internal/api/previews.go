package api

import (
	"net/http"
)

// previewTile is one source's preview, as a grid needs it.
//
// A DEDICATED PAYLOAD RATHER THAN N x GET /status, and the reason is the bug
// this endpoint exists to make fixable. The WebSocket carries an UNSCOPED
// status: every engine publishes onto the same broker and the UI keeps one
// status and one bitrate series, so with several sources a tile would be
// redrawn from whichever engine spoke last. A grid built on that shows the
// wrong picture's state under the right picture's name.
//
// It is also much smaller. Status carries every destination, rendition, meter
// and loudness report; a tile needs a name, whether anything is arriving, what
// is on air, and the shape to draw.
type previewTile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Width and Height are the LAST MEASURED ingest geometry, absent until a
	// probe lands. The grid sizes each tile from them so a vertical or 4:3
	// source is not letterboxed into a 16:9 box it never filled.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// OutputLive is whether anything is reaching this programme's destinations
	// -- the encoder, a backup, the slate or a playlist. It is what decides
	// whether a picture is worth showing.
	OutputLive bool `json:"outputLive"`
	// IngestLive is whether the operator's own encoder is arriving. It only
	// LABELS the tile: with the slate on air, OutputLive is true and IngestLive
	// is false, and the honest report is the slate's picture with a line saying
	// the input is gone -- not a blank panel hiding the thing being broadcast.
	IngestLive bool `json:"ingestLive"`
	// OnAir names the tier the picture is coming from: "primary", "backup",
	// "slate", "playlist", or empty when the selector is not running.
	OnAir string `json:"onAir,omitempty"`
}

// handlePreviews lists every source's preview state in one read.
//
// Polled rather than pushed, following PlayoutPage and MonitoringPage, which
// both poll for the same reason: the always-on feed is not source-scoped, and
// making it so is a change to every producer and consumer of TypeStatus rather
// than to this grid.
func (s *Server) handlePreviews(w http.ResponseWriter, r *http.Request) {
	engines := s.mgr.Engines()
	out := make([]previewTile, 0, len(engines))
	for _, e := range engines {
		if e == nil {
			continue
		}
		info := e.SourceInfo()
		tile := previewTile{ID: info.ID, Name: info.Name}
		if info.Video != nil {
			tile.Width, tile.Height = info.Video.Width, info.Video.Height
		}
		tile.OutputLive = e.OutputLive()
		tile.IngestLive = e.IngestLive()
		if f := e.Failover(); f != nil {
			tile.OnAir = string(f.Active)
		}
		out = append(out, tile)
	}
	writeJSON(w, http.StatusOK, out)
}
