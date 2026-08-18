package ffmpeg

import (
	"strings"
	"testing"
)

/* THE INGEST URL GOES IN A LOG LINE, AND IT CARRIES A CREDENTIAL.
 *
 * engine.ingestURLForLog builds an IngestSpec for display and its comment says
 * the quiet part out loud: "No RTMPAddress: PublicIngestURL deliberately renders
 * the server half only, and the token goes in OBS's separate stream-key box.
 * THIS IS A LOG LINE AND A DASHBOARD STRING, SO THE TOKEN STAYS OUT OF IT."
 *
 * The RTMP token does stay out. The other two credentials do not:
 *
 *   SRT renders ...&passphrase=<cleartext> into the query
 *   Pull returns the operator's pull URL WHOLE, and a pull URL is where a
 *     camera password or a CDN path token lives
 *
 * Both reach "ingest started" (engine.go) and "backup ingest started"
 * (selector.go), which are ordinary Info lines: journalctl, server.log, and
 * anywhere those are shipped. internal/diag's scrubbing does NOT cover this --
 * the recorder is a second consumer and the inner handler still writes the
 * original record.
 *
 * NOT alerts.RedactURL EITHER, and this is the part worth stating. That is the
 * residual pass, written for strings nobody constructed. Its keyCarrying table
 * is rtmp/rtsp/srt/udp/rtp -- https is absent -- so a CDN pull URL of the shape
 * https://cdn.example/live/<TOKEN>/index.m3u8 goes through it untouched. Here we
 * BUILT the URL and know exactly which parts are secret, so the safe rendering
 * is assembled rather than pattern-matched.
 */

const (
	logPassphrase = "notArealPassphrase.notreal"
	logCamPass    = "notArealCameraPw.notreal"
	logPathToken  = "NOTAREALPATHSEGMENT"
)

func TestTheLogRenderingCarriesNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		spec   IngestSpec
		secret string
		// keep is something diagnostic the line must still contain, so the
		// redaction cannot be "print nothing and pass".
		keep string
	}{
		{
			name: "an SRT passphrase",
			spec: IngestSpec{Kind: IngestSRT, SRTPort: 6000,
				SRTPassphrase: logPassphrase, SRTLatencyMS: 200},
			secret: logPassphrase,
			keep:   "6000",
		},
		{
			name: "a camera password in pull userinfo",
			spec: IngestSpec{Kind: IngestPull,
				PullURL: "rtsp://operator:" + logCamPass + "@camera.local/stream1"},
			secret: logCamPass,
			keep:   "camera.local",
		},
		{
			name: "a CDN token in a pull path segment",
			spec: IngestSpec{Kind: IngestPull,
				PullURL: "https://cdn.example/live/" + logPathToken + "/stream1/index.m3u8"},
			secret: logPathToken,
			keep:   "cdn.example",
		},
		{
			name: "a token in a pull query parameter",
			spec: IngestSpec{Kind: IngestPull,
				PullURL: "https://cdn.example/live/index.m3u8?token=" + logPathToken},
			secret: logPathToken,
			keep:   "cdn.example",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.spec.IngestURLForLog("<server>")
			if strings.Contains(got, tc.secret) {
				t.Errorf("the log rendering carries the credential:\n  %s", got)
			}
			if !strings.Contains(got, tc.keep) {
				t.Errorf("the log rendering dropped %q, which is the part an operator "+
					"debugging a failed ingest actually needs:\n  %s", tc.keep, got)
			}
		})
	}
}

// THE RTMP CASE WAS ALREADY SAFE AND MUST STAY USEFUL. PublicIngestURL renders
// the server half only for RTMP, by design; the log rendering must not become
// more aggressive than it needs to be and throw the app away.
func TestTheRTMPLogRenderingIsUnchanged(t *testing.T) {
	s := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live"}
	got := s.IngestURLForLog("<server>")
	if want := s.PublicIngestURL("<server>"); got != want {
		t.Errorf("RTMP log rendering = %q, want %q — this branch carries no "+
			"credential and had nothing to redact", got, want)
	}
}

// AND THE FULL RENDERING IS UNTOUCHED, because the authenticated API genuinely
// shows an operator their own passphrase — that is the feature.
func TestPublicIngestURLStillCarriesTheCredential(t *testing.T) {
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTPassphrase: logPassphrase}
	if !strings.Contains(s.PublicIngestURL("host"), logPassphrase) {
		t.Error("PublicIngestURL stopped rendering the passphrase; the settings page " +
			"shows an operator their own ingest URL and needs it whole")
	}
}
