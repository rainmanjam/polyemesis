package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A pull URL puts the credential in the MIDDLE and the filename last, which is
// the shape RedactURL's last-segment rule gets exactly backwards -- and for an
// https URL it does not run at all, because https is not in keyCarrying.
//
// GET /api/v1/sources handed this verbatim to a read-scoped bearer through
// readSafeIngest -> maskURL. Measured on main before this change.
func TestAMidPathPullCredentialIsMaskedForAReadPrincipal(t *testing.T) {
	const secret = "SUPERSECRETPATHSEG"
	in := db.IngestSettings{}
	in.Pull.URL = "https://cdn.example/live/" + secret + "/stream1/index.m3u8"

	got := readSafeIngest(in).Pull.URL
	if strings.Contains(got, secret) {
		t.Errorf("a read-scoped principal receives the pull credential verbatim: %q\n"+
			"maskURL must mask EVERY path segment: nothing in a URL says which one "+
			"is the secret, and this one is neither first nor last", got)
	}
	if !strings.Contains(got, "cdn.example") {
		t.Errorf("the host was destroyed too, which makes the field useless rather "+
			"than safe: %q", got)
	}
}
