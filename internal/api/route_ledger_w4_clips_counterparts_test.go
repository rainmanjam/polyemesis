package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Wave 4 #5's two read-scope-refused GETs, each given a counterpart proof
// rather than a bare Want, so neither adds to counterpartlessExcusedCeiling.
//
// Shaped like proveChatFlags: plant the thing the route exists to hand out,
// show the admin receives it (the positive control), and hand the read token's
// answer to the runner to scan. The non-GET routes beside them -- scan, accept,
// dismiss, template save and delete -- are denied by method and need nothing
// here.

// highlightExcerptSpoken is the planted transcript excerpt. Ordinary English, for
// the reason transcriptExportSpoken gives: it is speech, and a high-entropy
// literal would be flagged by gitleaks for no benefit.
const highlightExcerptSpoken = "the lighthouse keeper rang the brass bell twice"

// clipTemplateSentinel is the planted template's name.
const clipTemplateSentinel = "the orchard gate creaked in the evening wind"

// proveClipHighlights discharges GET /clipper/recordings/{id}/highlights.
//
// Each suggestion carries a transcript excerpt -- the words again, which is why
// the route is read-scope refused alongside /clipper/.../transcript.
func proveClipHighlights(t *testing.T) proofResult {
	h, sign, srv, rec := highlightFixture(t)
	if _, err := srv.store.ReplacePendingHighlights(rec, []db.Highlight{{StartMS: 0, EndMS: 30_000,
		Signals: []byte(`[]`), Excerpt: highlightExcerptSpoken}}); err != nil {
		t.Fatal(err)
	}
	read := createScopedToken(t, h, sign, "ledger-clip-highlights", db.ScopeRead)
	path := "/api/v1/clipper/recordings/" + strconv.FormatInt(rec, 10) + "/highlights"

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, highlightExcerptSpoken) {
		t.Fatalf("the admin's highlight list answered %d without the planted excerpt, so the read "+
			"principal would be denied nothing: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's highlight list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{highlightExcerptSpoken},
	}
}

// proveClipTemplates discharges GET /clipper/templates: the operator's saved
// layouts, with the fonts directory's listing and the FFmpeg build's text
// capability beside them.
func proveClipTemplates(t *testing.T) proofResult {
	h, sign, srv, _ := highlightFixture(t)
	if _, err := srv.store.PutClipTemplate(clipTemplateSentinel, []byte(`{"layout":"blur"}`)); err != nil {
		t.Fatal(err)
	}
	read := createScopedToken(t, h, sign, "ledger-clip-templates", db.ScopeRead)
	const path = "/api/v1/clipper/templates"

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, clipTemplateSentinel) {
		t.Fatalf("the admin's template list answered %d without the planted template: %s",
			code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's template list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{clipTemplateSentinel},
	}
}
