package alerts

import (
	"strings"
	"testing"
)

// secretSegment is a credential sitting in the MIDDLE of a path -- the shape
// #236 was. Low-entropy and self-describing because gitleaks scans this PR's
// own commits and what is under test is whether the string survives, not how
// random it is. The high-entropy literal in this file is the existing
// package-level `secretKey` from redact_test.go, reused rather than duplicated.
const secretSegment = "midpath-not-a-real-secret"

// shortPassword is the credential no heuristic will ever recognise: short,
// lowercase, dictionary-adjacent, indistinguishable from a username. It is here
// because every mask that has failed in this repository failed by reasoning
// about what credentials LOOK like.
const shortPassword = "hunter22"

// TestPrincipalMaskingCoversEverySegmentOfEveryScheme is the first test
// RedactURLForPrincipal has ever had.
//
// It had none. `grep -rn RedactURLForPrincipal --include=*_test.go` returned
// nothing at bf38d19, while internal/api/redact.go:84 routes GET /api/v1/sources
// and GET /api/v1/settings through it for every read-scoped token. It is the
// function that decides what a low-privilege reader sees, and the only thing
// asserting it worked was that #236 had been fixed once.
//
// This repository has now shipped FOUR URL maskers and found a defect in three:
// urlSecrets masked a filename and rendered a mid-path credential; RedactURL
// masks only the last path segment and only for rtmp/rtsp/srt-like schemes, so
// an https URL went through untouched; and RedactURLForPrincipal -- the fix for
// that -- returned url.String() directly, so its mask arrived at the client as
// `%5Bredacted%5D`. The pattern in all three is the same: a mask built from
// where credentials USUALLY live rather than from what the URL carries.
//
// So the table below is paired with a per-row PROPERTY -- the output never
// contains the row's own secret literal -- which is the assertion that survives
// somebody rewriting the goldens to match new behaviour. A golden says "this is
// what it does"; the property says "this is what it must never do".
func TestPrincipalMaskingCoversEverySegmentOfEveryScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		// secrets are the literals that must not appear in the output, whatever
		// the golden says.
		secrets []string
	}{
		{
			name:    "rtsp, credential in the userinfo AND in the middle of the path",
			in:      "rtsp://admin:" + secretKey + "@cam.example/" + secretSegment + "/stream1",
			want:    "rtsp://" + Mask + "@cam.example/" + Mask + "/" + Mask,
			secrets: []string{secretKey, secretSegment},
		},
		{
			name: "https HLS pull URL with the credential mid-path -- this is #236",
			// The exact shape that was handed verbatim to a read-scoped bearer
			// through GET /api/v1/sources. https is not in keyCarrying, so
			// RedactURL did nothing at all here; and where it does run it masks
			// index.m3u8, the one segment that is not a secret.
			in:      "https://cdn.example/live/" + secretSegment + "/stream1/index.m3u8",
			want:    "https://cdn.example/" + Mask + "/" + Mask + "/" + Mask + "/" + Mask,
			secrets: []string{secretSegment},
		},
		{
			name:    "rtmp, the classic last-segment stream key",
			in:      "rtmp://a.rtmp.youtube.com/live2/" + secretKey,
			want:    "rtmp://a.rtmp.youtube.com/" + Mask + "/" + Mask,
			secrets: []string{secretKey},
		},
		{
			name:    "a token in the query, under a name secretParam knows",
			in:      "https://cdn.example/hls/index.m3u8?token=" + secretKey,
			want:    "https://cdn.example/" + Mask + "/" + Mask + "?token=" + Mask,
			secrets: []string{secretKey},
		},
		{
			name: "a credential in the query under a name secretParam has never heard of",
			// `authcode` is not in secretParam and deliberately is not being
			// added to it: that table is a list of names somebody remembered,
			// and CDNs use authcode, hdnts and policy. For a principal the
			// answer is EVERY parameter, which is what makes the table
			// irrelevant here rather than incomplete.
			in:      "https://cdn.example/live/index.m3u8?authcode=" + secretKey,
			want:    "https://cdn.example/" + Mask + "/" + Mask + "?authcode=" + Mask,
			secrets: []string{secretKey},
		},
		{
			name: "rtsp with a short, ordinary-looking password and a one-segment path",
			// Two things at once. The password looks like a username, so
			// nothing entropy-based would flag it. And the path is a SINGLE
			// segment, which maskLastSegment deliberately leaves alone (it
			// reads as the application, not the key) -- for a principal there is
			// no such thing as a segment that is safe by position.
			in:      "rtsp://user:" + shortPassword + "@cam.example/stream",
			want:    "rtsp://" + Mask + "@cam.example/" + Mask,
			secrets: []string{shortPassword},
		},
		{
			name: "a URL url.Parse refuses is masked past the authority, not passed through",
			// The conservative direction. A string that does not parse is the
			// one case where the function cannot reason about structure at all,
			// and passing it through is how a stream key reaches a reader.
			in:      "rtsp://cam example/live/" + secretKey,
			want:    "rtsp://cam example/" + Mask,
			secrets: []string{secretKey},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURLForPrincipal(tc.in)
			if got != tc.want {
				t.Errorf("RedactURLForPrincipal(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
			// THE PROPERTY, independent of the golden above. A future edit that
			// changes the shape must still not change this.
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Errorf("RedactURLForPrincipal(%q) = %q, which still contains %q.\n\n"+
						"This string is what GET /api/v1/sources and GET /api/v1/settings "+
						"hand to a READ-SCOPED token -- a credential that is entitled to see "+
						"that a source exists and not to see what unlocks it. Over-masking a "+
						"path such a reader was never entitled to costs nothing; "+
						"under-masking it is the disclosure.", tc.in, got, s)
				}
			}
			// The mask must arrive readable. F4's defect was invisible to a
			// secrets check: %5Bredacted%5D contains no credential and is still
			// wrong, because a reader cannot tell it from a bug in the server.
			if strings.Contains(got, "%5B") || strings.Contains(got, "%5D") {
				t.Errorf("RedactURLForPrincipal(%q) = %q -- the mask is percent-escaped. "+
					"url.String escapes the brackets, so this reaches an API response body "+
					"as `%%5Bredacted%%5D`, which reads as a bug rather than a deliberate "+
					"omission. RedactURL has run its output through unescapeMask since it "+
					"was written; this function was added later and did not.", tc.in, got)
			}
		})
	}
}

// TestRedactURLStillLeavesADiagnosticReadable is the other half, and it exists
// so nobody "fixes" the asymmetry above by making both functions the same.
//
// The split between RedactURL and RedactURLForPrincipal is DELIBERATE and is
// argued at redact.go:121-147. A diagnostic wants to stay readable: blanking
// every path segment of every URL in every log line destroys the message an
// operator on a headless box is reading. A response body handed to a
// read-scoped token wants the opposite.
//
// So RedactURL leaving an https mid-path segment alone is not a bug in
// RedactURL, and the correct response to #236 was a second function -- which is
// what happened. If a future reader deletes this test to "harmonise the four
// maskers", they will have turned every log line into `https://host/[redacted]/
// [redacted]/[redacted]` and lost the ability to tell two CDN endpoints apart.
func TestRedactURLStillLeavesADiagnosticReadable(t *testing.T) {
	const in = "https://cdn.example/live/" + secretSegment + "/stream1/index.m3u8"
	got := RedactURL(in)
	if got != in {
		t.Errorf("RedactURL(%q) = %q, want it unchanged.\n\n"+
			"RedactURL is the OPERATOR-facing mask and is documented to leave https path "+
			"segments alone (redact.go, `IT DOES NOT MASK AN https PATH SEGMENT`). If this "+
			"now masks them, the diagnostic/principal split has been collapsed and every "+
			"log line naming a CDN endpoint has become indistinguishable from every other. "+
			"The mask a read-scoped reader gets is RedactURLForPrincipal, tested above.", in, got)
	}
}
