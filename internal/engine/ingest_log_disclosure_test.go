package engine

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THE CREDENTIAL MUST NOT REACH THE PROCESS'S OWN LOG.
 *
 * internal/ffmpeg pins the rendering; this pins the WIRING. The two are
 * different claims, and the second is the one that was wrong: a correct
 * IngestURLForLog is worth nothing if `ingest started` still calls
 * PublicIngestURL.
 *
 * It matters that this is the process's own log rather than the debug bundle.
 * internal/diag scrubs the copy it puts in the ring and passes the ORIGINAL
 * record to the inner handler -- deliberately, so "nothing about the existing
 * output changes when debug mode is off". So the secret set added for the
 * export does not cover journalctl, server.log, or wherever those are shipped.
 * That is the failure diag's own header lists twice from 0.7.0 alone: "a key
 * reaching server.log on the give-up path".
 *
 * WHY THE FIXTURES READ notAreal... RATHER THAN LOOKING LIKE REAL KEYS.
 * internal/diag's plants are deliberately realistic and deliberately
 * allowlisted in .gitleaks.toml, because that test exercises the RESIDUAL
 * alerts.Redact pass, which only fires on values shaped like real platform
 * credentials -- "a fixture gitleaks ignores would be a fixture that proves
 * less", in its own words.
 *
 * These prove something else. They assert that a DECLARED literal is masked,
 * and the declared set matches whatever exact bytes it was handed, so realism
 * buys nothing here and costs a gitleaks finding plus an allowlist entry. The
 * constraints that DO matter are kept: each is longer than alerts.MinSecretLen,
 * and the SRT passphrase stays inside the RFC 3986 unreserved alphabet the
 * database enforces.
 */

func settingsWithSRTPassphrase(pass string) db.Settings {
	var s db.Settings
	s.Ingest.Mode = db.IngestSRT
	s.Ingest.SRT.Passphrase = pass
	s.Ingest.SRT.LatencyMS = 200
	s.Listeners.SRTPort = 6000
	return s
}

func settingsWithPullURL(raw string) db.Settings {
	var s db.Settings
	s.Ingest.Mode = db.IngestPull
	s.Ingest.Pull.URL = raw
	return s
}

// logged returns everything an Engine writes for one call to ingestURLForLog,
// as the line `ingest started` actually renders it.
func logged(t *testing.T, s db.Settings) string {
	t.Helper()
	var buf bytes.Buffer
	e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil))}
	e.log.Info("ingest started", "mode", s.Ingest.Mode, "url", e.ingestURLForLog(s))
	return buf.String()
}

func TestTheIngestStartedLineCarriesNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    db.Settings
		secret string
		keep   string
	}{
		{
			name:   "the SRT passphrase",
			set:    settingsWithSRTPassphrase("notArealPassphrase.notreal"),
			secret: "notArealPassphrase.notreal",
			keep:   "6000",
		},
		{
			name:   "a camera password in the pull URL",
			set:    settingsWithPullURL("rtsp://operator:notArealCameraPw.notreal@camera.local/stream1"),
			secret: "notArealCameraPw.notreal",
			keep:   "camera.local",
		},
		{
			// The shape alerts.RedactURL cannot help with: https is not in its
			// keyCarrying table, so the residual pass leaves this untouched.
			name:   "a CDN token in a pull path segment",
			set:    settingsWithPullURL("https://cdn.example/live/NOTAREALCDNPATH.NOTREAL/index.m3u8"),
			secret: "NOTAREALCDNPATH.NOTREAL",
			keep:   "cdn.example",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := logged(t, tc.set)
			if strings.Contains(out, tc.secret) {
				t.Errorf("the credential is in the process's own log:\n  %s", strings.TrimSpace(out))
			}
			if !strings.Contains(out, tc.keep) {
				t.Errorf("the line lost %q, which is what an operator debugging a failed "+
					"ingest needs:\n  %s", tc.keep, strings.TrimSpace(out))
			}
		})
	}
}
