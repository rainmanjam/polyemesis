package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Clip publishing's two session-only GETs, each given a counterpart proof rather
// than a bare Want. They carry a connected account's name and a platform's own
// post id: things a bearer must never be handed, so the 403 is shown to be the
// only thing standing between a token and them.

// publishAccountSentinel is the planted channel name. Ordinary English, so a
// secret scanner has nothing to flag.
const publishAccountSentinel = "the harbour lamp burned until morning"

// publishPostSentinel is the planted platform post id.
const publishPostSentinel = "post-harbour-lamp-0451"

func provePublishTargets(t *testing.T) proofResult {
	f := newPubFixture(t)
	f.account(t, db.PlatformYouTube, publishAccountSentinel, ytUploadScope)
	read := createScopedToken(t, f.h, f.sign, "ledger-publish-targets", db.ScopeRead)
	path := "/api/v1/publish/targets?clip=" + strconv.FormatInt(f.clip, 10)

	code, high := rawBodyOf(t, f.h, f.sign, path)
	if code != http.StatusOK || !strings.Contains(high, publishAccountSentinel) {
		t.Fatalf("the session's targets answered %d without the planted account: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, f.h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's targets answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, f.h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{publishAccountSentinel},
	}
}

func provePublishStatus(t *testing.T) proofResult {
	f := newPubFixture(t)
	var v publicationView
	decodeInto(t, f.click(t, f.form("youtube", f.yt), http.StatusAccepted), &v)
	if err := f.srv.store.RecordPublicationPost(v.ID, publishPostSentinel, "https://example.invalid/"+publishPostSentinel, "private"); err != nil {
		t.Fatal(err)
	}
	read := createScopedToken(t, f.h, f.sign, "ledger-publish-status", db.ScopeRead)
	path := "/api/v1/publish/" + strconv.FormatInt(v.ID, 10)

	code, high := rawBodyOf(t, f.h, f.sign, path)
	if code != http.StatusOK || !strings.Contains(high, publishPostSentinel) {
		t.Fatalf("the session's status answered %d without the planted post id: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, f.h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's status answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, f.h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{publishPostSentinel},
	}
}
