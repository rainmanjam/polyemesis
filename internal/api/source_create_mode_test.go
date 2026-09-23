package api

import (
	"fmt"
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

// A source created the way the console creates one -- {name} and nothing else
// -- is an SRT source, with a publish URL an encoder can use.
//
// It used to be stored with no ingest mode at all: the body is decoded over
// DefaultSettings().Ingest, whose mode is unset so that a fresh install's
// first-run choice is not made for the operator. While the shared SRT port
// admitted any source that was the SRT source it was used as; now that the
// port admits only SRT-mode sources, it was a source nothing could publish to,
// made by the one button the Sources page offers.
//
// Mutation: seed the create from db.DefaultSettings().Ingest unchanged again.
// Observed to fail with `created from {name} has ingest mode ""`.
func TestASourceCreatedFromItsNameAloneIsAnSRTSource(t *testing.T) {
	h, _, sign := sourceServer(t)

	var created struct {
		sourceRow
		Ingest struct {
			Mode string `json:"mode"`
		} `json:"ingest"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Console"}, http.StatusCreated), &created)

	if created.Ingest.Mode != "srt" {
		t.Fatalf("a source created from {name} has ingest mode %q; the shared SRT port "+
			"refuses every publish into it, and nothing else can reach it either", created.Ingest.Mode)
	}
	if u := created.PublishURLs["srt"]; !strings.Contains(u, "streamid=") {
		t.Errorf("a source created from {name} has publish URLs %v; want an srt:// URL "+
			"carrying its token", created.PublishURLs)
	}
}

// And the unset state cannot be asked for by name: not on create, and not by
// clearing the mode of a source that has one. Either would make a source the
// shared ports refuse, with no error at the moment it was made.
//
// Mutation: drop the unset refusal from handleCreateSource or
// handleUpdateSource. Observed to fail with "was accepted".
func TestASourceCannotBeGivenAnUnsetIngestMode(t *testing.T) {
	h, _, sign := sourceServer(t)

	send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Nothing", "ingest": map[string]any{"mode": ""}},
		http.StatusBadRequest)

	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Chosen"}, http.StatusCreated), &created)
	send(t, h, sign, http.MethodPut, fmt.Sprintf("/api/v1/sources/%d", created.ID),
		map[string]any{"name": "Chosen", "ingest": map[string]any{"mode": ""}},
		http.StatusBadRequest)
}
