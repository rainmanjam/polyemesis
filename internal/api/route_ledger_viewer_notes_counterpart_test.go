package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// viewerNoteSpoken is the planted note text: the operator's private words about
// a viewer. Ordinary English for the reason chatFlagsSpoken gives.
const viewerNoteSpoken = "the lighthouse keeper suspects this viewer of feeding the seagulls"

// proveViewerNotes discharges GET /chat/users/notes.
//
// The route hands back the operator's private note about a viewer. The proof
// stores one through PUT as the admin, shows the admin's GET carries it (the
// positive control), and returns what the read token received for the runner to
// scan for the planted words.
func proveViewerNotes(t *testing.T) proofResult {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-viewer-notes", db.ScopeRead)

	const path = "/api/v1/chat/users/notes?platform=twitch&account=acct&authorId=u-ledger"
	send(t, h, sign, http.MethodPut, path, map[string]any{"note": viewerNoteSpoken, "tags": []string{"watch"}}, http.StatusOK)
	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, viewerNoteSpoken) {
		t.Fatalf("the admin's note answered %d without the planted words, so the read principal "+
			"would be denied nothing: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's note answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{viewerNoteSpoken},
	}
}
