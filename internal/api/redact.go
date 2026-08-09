package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Read-safe views of the credential-bearing stored types.
//
// WHY THIS FILE EXISTS, stated once so the next person does not re-derive the
// hole it closes. The scope model refuses writes by HTTP METHOD, which is the
// right shape for a rule about routes and structurally blind to a GET whose
// RESPONSE BODY is itself a credential. Every leak #150's review found was a
// correct-by-the-rule GET: a rule that cannot see what it returns will keep
// producing this finding. So the second half of the rule lives here, at the
// point where the credential is serialised.
//
// THREE PROPERTIES, each of which was arrived at by ruling out the obvious
// alternative:
//
//  1. It operates on a COPY. Not a MarshalJSON on the db type, not `json:"-"`,
//     not a Secret string type. Those were tried and they ERASE STORED
//     CREDENTIALS: PutSettings and CreateSource marshal these very structs for
//     STORAGE, so a type that refuses to serialise its own secret writes an
//     empty passphrase into the database on the next save. It also defeats
//     ingestEqual, which compares marshalled ingest blocks to decide whether
//     the live listener must be restarted -- a passphrase rotation would then
//     silently fail to take effect. Redaction has to be a function applied at
//     egress, never a property of the type.
//
//  2. It BLANKS AND MASKS VALUES, never removes fields. The field set of every
//     response stays byte-for-byte the same shape. The settings and destination
//     endpoints are read-modify-write for both the UI and the acceptance
//     drivers, and the PUT side uses DisallowUnknownFields -- a view that
//     dropped or renamed a field would 400 on the way back in, or worse, would
//     round-trip a redaction sentinel into the database.
//
//  3. It is applied for a READ-SCOPED TOKEN ONLY. A session and an admin token
//     see exactly what they saw before. The console has to show the operator
//     the key they are about to paste into OBS, and an admin token could rotate
//     any of these secrets anyway -- withholding them from it would be a lock
//     with the key taped to the front.
//
// The masking of URLs reuses alerts.RedactURL rather than inventing a second
// spelling: it already masks userinfo, the passphrase/streamid/token query
// parameters and the key-carrying last path segment, and it is already what
// supervisor.CommandString and Process.Logs run. GET /processes was clean
// throughout this review precisely because it calls it, and GET /system was not
// because it did not.

// isReadScopedToken reports whether this request's principal is a bearer token
// that is not admin -- the one principal any of this applies to.
//
// The same predicate as readScopeCannotSeePublishTokens, under the name that
// says what it TESTS rather than what its first caller did with the answer. It
// is used where the consequence is not about publish tokens at all: the
// hardware re-detect gate on GET /encoders, for one, which is about spawning
// processes.
//
// Anything that is not admin counts, including a scope string this build does
// not recognise. A value from a newer schema or a hand-edited row must narrow
// what a credential can do, never widen it.
func isReadScopedToken(r *http.Request) bool {
	return readScopeCannotSeePublishTokens(r)
}

// maskURL masks the credential parts of a URL, leaving an empty string empty.
//
// The empty check is not cosmetic: alerts.RedactURL turns "" into "[redacted]",
// and a settings blob whose unset pull URL came back as a literal
// "[redacted]" would be PUT straight back by the settings page and stored.
func maskURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	return alerts.RedactURL(raw)
}

// readSafeIngest blanks the two ingest credentials and masks the pull URL.
//
// Both blanked fields are LIVE. ingest.srt.passphrase is the AES key the SRT
// listener enforces; ingest.rtmp.streamKey no longer addresses anything on a
// fresh install but engine.Manager still honours a stored one as a legacy
// address on an install upgraded from a pre-one-port build, which makes it a
// working publish credential on exactly the installs least able to notice.
// ingest.pull.url carries rtsp://user:pass@ userinfo verbatim for an IP camera.
func readSafeIngest(in db.IngestSettings) db.IngestSettings {
	in.SRT.Passphrase = ""
	in.RTMP.StreamKey = ""
	in.Pull.URL = maskURL(in.Pull.URL)
	return in
}

// readSafeSource returns a copy of a source with its ingest credentials gone.
//
// The publish token and the publish URLs are handled by viewSource, which owns
// the derived fields; this covers the stored block those derived fields are
// computed FROM. Blanking only the derived side was measurably a no-op: the
// legacy RTMP key that viewSource blanked is computed as exactly
// src.Ingest.RTMP.StreamKey, so the identical string came back two JSON fields
// away.
func readSafeSource(src db.Source) db.Source {
	// The publish token is blanked here as well as in viewSource, which owns
	// the DERIVED fields that embed it. The duplication is deliberate: this
	// function has to be complete on its own, because the scrubber-completeness
	// test plants a sentinel into every leaf classified secret and marshals
	// what comes back. A function that relied on its one caller to finish the
	// job would pass no such test, and the next caller would not know.
	src.Token = ""
	src.Ingest = readSafeIngest(src.Ingest)
	return src
}

// readSafeSettings returns a copy of the settings blob with every credential in
// it blanked or masked.
//
// The BACKUP ingest block matters as much as the primary and is easy to forget:
// failover.backup.srt.passphrase is the passphrase the STANDBY listener
// enforces, so leaving it would expose the redundant path independently of the
// one that was fixed.
//
// mqtt.brokerUrl is masked rather than blanked. Settings.Validate refuses
// userinfo in it, but only when MQTT is ENABLED -- a broker URL carrying
// user:pass@ can be saved while MQTT is off and then round-trips out of this
// endpoint. Masking covers that gap without depending on the validator.
//
// The MQTT password and the automod model key are deliberately absent from this
// function: they are sealed in their own tables and this blob only ever carried
// derived booleans for them. That is the pattern the ingest block never got.
func readSafeSettings(s db.Settings) db.Settings {
	s.Ingest = readSafeIngest(s.Ingest)
	s.Failover.Backup.SRT.Passphrase = ""
	s.Failover.Backup.RTMP.StreamKey = ""
	s.Failover.Backup.Pull.URL = maskURL(s.Failover.Backup.Pull.URL)
	s.MQTT.BrokerURL = maskURL(s.MQTT.BrokerURL)
	return s
}

// readSafeDestination returns a copy of a destination with its publish
// credentials gone.
//
// url is MASKED rather than left alone because for an audio destination it is
// an Icecast mount of the form icecast://user:pass@host/mount -- the userinfo
// IS the publish credential there, and for every other kind alerts.RedactURL
// leaves the recognisable part of the endpoint intact. backupUrl gets the same
// treatment for the same reason.
func readSafeDestination(d db.Destination) db.Destination {
	d.StreamKey = ""
	d.BackupStreamKey = ""
	d.URL = maskURL(d.URL)
	d.BackupURL = maskURL(d.BackupURL)
	return d
}

// principalVaryingResponse marks a response whose BODY DEPENDS ON WHO ASKED.
//
// These five endpoints now return different bytes on the same URL for a read
// token than for a session, which is a new property and one that a cache in
// front of the server would get wrong in the worst possible direction: a
// response containing the real stream key, stored under the URL alone, served
// afterwards to the read token the redaction exists for. Operators front this
// with nginx and Caddy, so this is required rather than defensive.
//
// Vary alone would not be enough for a shared cache that ignores it, hence
// no-store as well.
func principalVaryingResponse(w http.ResponseWriter) {
	w.Header().Add("Vary", "Authorization")
	w.Header().Set("Cache-Control", "private, no-store")
}
