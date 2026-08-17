package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* SourceSecrets IS THE ONLY THING THAT PUTS A SOURCE'S CREDENTIALS INTO THE
 * DEBUG BUNDLE'S SCRUBBER.
 *
 * internal/api/debugmode.go builds the recorder's SecretSet from
 * DestinationSecrets + SourceSecrets when recording starts. Whatever
 * SourceSecrets fails to declare is covered by nothing but the residual
 * alerts.Redact pass, and travels to whoever receives the exported bundle.
 *
 * IT WAS CALLING THE WRONG EXTRACTOR. urlSecrets says so about itself, in the
 * comment directly above it: "THIS RULE IS CORRECT ONLY FOR A PUBLISH URL ...
 * It is NOT correct for a pull URL, where the credential is in the URL and
 * nowhere else -- see pullURLSecrets and #229." ingestSecrets calls
 * pullURLSecrets. SourceSecrets called urlSecrets, which reintroduces #229 on
 * the one path whose output leaves the building.
 */

func TestSourceSecretsDeclaresAPullURLCredential(t *testing.T) {
	// The credential is a path segment, which is how a CDN hands one out. For a
	// PUBLISH url the last segment is the stream key and the rule is to take it;
	// for a PULL url the rule has to keep the whole authority-and-path, because
	// the secret is the path.
	const secretSeg = "NOTAREALPATHSEGMENT"
	src := &db.Source{
		Ingest: db.IngestSettings{
			Pull: db.PullSettings{
				URL: "https://cdn.example/live/" + secretSeg + "/stream1/index.m3u8",
			},
		},
	}

	got := SourceSecrets([]*db.Source{src})
	if !containsLiteral(got, secretSeg) {
		t.Errorf("SourceSecrets declared %q — none of which is the credential in the "+
			"pull URL. urlSecrets takes the LAST path segment, which for a pull URL is "+
			"the filename; the debug bundle therefore declares \"index.m3u8\" and "+
			"exports the credential in the clear.", got)
	}
}

// The same function must not lose the RTMP ingest key either: ingestSecrets
// carries rtmp.StreamKey and this twin did not.
func TestSourceSecretsDeclaresTheRTMPIngestKey(t *testing.T) {
	const key = "notArealRtmpIngest.notreal"
	src := &db.Source{
		Ingest: db.IngestSettings{RTMP: db.RTMPSettings{StreamKey: key}},
	}
	if got := SourceSecrets([]*db.Source{src}); !containsLiteral(got, key) {
		t.Errorf("SourceSecrets declared %q, missing the RTMP ingest key", got)
	}
}

// And the SRT passphrase and tokens it already carried stay carried.
func TestSourceSecretsStillDeclaresWhatItAlreadyDid(t *testing.T) {
	src := &db.Source{
		Token:     "notArealPublishTok.notreal",
		PrevToken: "notArealPrevTok.notreal",
		Ingest:    db.IngestSettings{SRT: db.SRTSettings{Passphrase: "notArealSrtPass.notreal"}},
	}
	got := SourceSecrets([]*db.Source{src})
	for _, want := range []string{src.Token, src.PrevToken, src.Ingest.SRT.Passphrase} {
		if !containsLiteral(got, want) {
			t.Errorf("SourceSecrets no longer declares %q: %q", want, got)
		}
	}
}

func containsLiteral(set []string, want string) bool {
	for _, s := range set {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
