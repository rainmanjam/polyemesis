package api

import (
	"net/http"
	"strings"
	"testing"
)

// A source can be created already choosing its ingest, without restating
// every default.
//
// The one-port SRT listener now admits a publisher only into a source set to
// SRT (engine.Manager.lookupToken), so "create it, then publish" needs the mode
// to be chosen. The create handler applied the defaults only when the body
// carried no ingest mode at all, so naming the mode alone decoded into a
// zero-valued ingest block -- ports 0, latency 0 -- and failed validation with
// errors that said nothing about what was missing. The smallest request that
// names a transport must be as small as the one that does not.
func TestCreatingASourceCanNameItsIngestModeAlone(t *testing.T) {
	h, _, sign := sourceServer(t)

	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical", "ingest": map[string]any{"mode": "srt"}},
		http.StatusCreated), &created)

	u := created.PublishURLs["srt"]
	if !strings.HasPrefix(u, "srt://") || !strings.Contains(u, "streamid=") {
		t.Fatalf("a source created in SRT mode has publish URLs %v; want an srt:// URL "+
			"carrying its token", created.PublishURLs)
	}
}
