package api

import (
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/services"
)

// servicesResponse carries the registry plus where it came from. The
// provenance travels with the data on purpose: these are other people's
// published limits, copied, and a UI that shows "max 160 kbps" should be able
// to say whose figure that is.
type servicesResponse struct {
	Provenance string             `json:"provenance"`
	Services   []services.Service `json:"services"`
}

// handleListServices returns the platform registry: ingest servers, encoder
// ceilings, codecs, and for per-channel platforms the note explaining why
// there is no server to pick.
//
// No database read, no per-install state -- this is the same answer for every
// deployment, which is why it needs no filtering by source or ownership.
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, servicesResponse{
		Provenance: services.Provenance(),
		Services:   services.All(),
	})
}
