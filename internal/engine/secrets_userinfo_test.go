package engine

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// #229's residual: THE SPELLING OF THE SECRET HAS TO MATCH THE SPELLING IN THE
// ARGV.
//
// SecretSet.Scrub is a substring replacement over the argv the kernel was
// handed. A secret literal collected in a different spelling from the one on
// the command line matches nothing and masks nothing, and the whole mechanism
// fails silently -- there is no error, no log line, just a credential in a
// response body.
//
// url.URL.User is exactly such a different spelling: it DECODES. That is the
// entire defect, and it only shows up for passwords carrying a character that
// forces percent-encoding. Those characters -- @ / ! # : % -- are ordinary in
// generated credentials, so this was not an exotic shape.
//
// These tests drive the REAL SecretSet over the REAL argv text rather than
// inspecting the secret list, because a test that asserted the list would pass
// on a set full of literals that match nothing. What is being claimed is that
// the password does not survive the scrub, so that is what is asserted.

// encodedCreds are passwords that url.URL will decode into something other than
// what the command line carries. Each is at least alerts.MinSecretLen (8) long
// once encoded AND once decoded, because SecretSet drops anything shorter and a
// case that was dropped for length would prove nothing about spelling.
var encodedCreds = []struct {
	name    string
	rawPass string // as it appears in the URL and therefore in the argv
	decoded string // as url.URL.User.Password() reports it
}{
	{"at sign and bang", "p%40ssw0rd%21", "p@ssw0rd!"},
	{"slash", "s%2Fecretvalue", "s/ecretvalue"},
	{"hash and colon", "hunter%232%3Along", "hunter#2:long"},
	{"percent itself", "fifty%25percent", "fifty%percent"},
}

func TestAPercentEncodedPullPasswordDoesNotSurviveInArgv(t *testing.T) {
	for _, c := range encodedCreds {
		t.Run(c.name, func(t *testing.T) {
			raw := "rtsp://operator:" + c.rawPass + "@cam.example.com/stream/1"
			pull := db.PullSettings{URL: raw}

			set := alerts.NewSecretSet(slog.New(slog.NewTextHandler(discard{}, nil)),
				ingestSecrets(db.SRTSettings{}, db.RTMPSettings{}, pull, "")...)

			// The argv an ingest child is actually handed carries the URL
			// verbatim -- that is the point of belowAuthority's comment.
			argv := []string{"ffmpeg", "-i", raw, "-c", "copy", "-f", "mpegts", "-"}
			got := strings.Join(set.ScrubArgv(argv), " ")

			if strings.Contains(got, c.rawPass) {
				t.Errorf("the password survives the scrub AS IT APPEARS IN THE ARGV.\n"+
					"  argv after scrub: %s\n"+
					"  looking for:      %q\n"+
					"GET /api/v1/processes renders this text and a READ-SCOPED token can "+
					"reach it, so this is a live credential disclosure and not a cosmetic "+
					"one. url.URL.User.Password() reports %q, which is a different string "+
					"from the one on the command line, so a set built only from url.URL "+
					"matches nothing here.", got, c.rawPass, c.decoded)
			}

			// And the fix is not "mask the whole URL": the host has to survive,
			// or an operator cannot tell which camera is failing.
			if !strings.Contains(got, "cam.example.com") {
				t.Errorf("the host was masked away too; an operator reading /processes "+
					"needs to know WHICH source this is: %s", got)
			}
		})
	}
}

// The same defect on the destination side, which is a separate code path:
// destSecrets -> urlSecrets, not ingestSecrets -> pullURLSecrets. Both called
// url.URL.User and both leaked; a fix applied to one only would leave
// dest:N leaking on exactly the route the pull side no longer does.
func TestAPercentEncodedDestinationPasswordDoesNotSurviveInArgv(t *testing.T) {
	for _, c := range encodedCreds {
		t.Run(c.name, func(t *testing.T) {
			raw := "rtmp://publisher:" + c.rawPass + "@ingest.example.com/live"
			row := &db.Destination{URL: raw}

			set := alerts.NewSecretSet(slog.New(slog.NewTextHandler(discard{}, nil)),
				destSecrets(row)...)

			argv := []string{"ffmpeg", "-i", "-", "-c", "copy", "-f", "flv", raw}
			got := strings.Join(set.ScrubArgv(argv), " ")

			if strings.Contains(got, c.rawPass) {
				t.Errorf("a destination's percent-encoded password survives the scrub: %s\n"+
					"looking for %q (url.URL reports %q)", got, c.rawPass, c.decoded)
			}
		})
	}
}

// rawUserinfo has to bound the authority BEFORE it looks for '@', or a path
// containing one -- legal, and ordinary in S3-style and token-in-path URLs --
// gets read as a credential separator and the real path segment is handed to
// the scrubber as a "password".
//
// The failure this prevents is over-masking rather than disclosure, which is
// the safe direction, but it would mask a path an operator needs to read.
func TestRawUserinfoDoesNotReadAPathAtSignAsACredential(t *testing.T) {
	cases := []struct {
		raw           string
		wantUser      string
		wantPass      string
		whyItIsTricky string
	}{
		{
			raw:           "https://cdn.example.com/live/user@host/index.m3u8",
			whyItIsTricky: "no userinfo at all, but an '@' in the path",
		},
		{
			raw:      "rtsp://operator:p%40ss@cam.example.com/a@b/1",
			wantUser: "operator", wantPass: "p%40ss",
			whyItIsTricky: "an '@' in BOTH the userinfo and the path",
		},
		{
			raw:           "rtmp://tokenonly@ingest.example.com/live",
			wantUser:      "tokenonly",
			whyItIsTricky: "a username with no password",
		},
	}
	for _, c := range cases {
		user, pass := rawUserinfo(c.raw)
		if user != c.wantUser || pass != c.wantPass {
			t.Errorf("rawUserinfo(%q) = (%q, %q), want (%q, %q) -- %s",
				c.raw, user, pass, c.wantUser, c.wantPass, c.whyItIsTricky)
		}
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
