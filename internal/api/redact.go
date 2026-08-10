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

// maskDestinationTarget masks a destination's url or backupUrl, EXCEPT when
// that field is holding a filename rather than a URL.
//
// Destination.url is overloaded and the redaction did not notice. For rtmp and
// srt it is a URL and Validate insists on the scheme. For kind:file it is a
// relative name inside the recordings directory -- Validate rejects a leading
// slash and any ".." precisely so it cannot be anything else -- and for
// kind:audio it is EITHER an Icecast mount OR, in exactly the same field, an
// output filename with the same confinement.
//
// alerts.RedactURL is conservative about strings it cannot parse, which is the
// right call for a log line and destroys a filename: "shows/monday-night.mp4"
// comes back as the bare word "[redacted]", and so does "radio-archive.mp3".
// Both were measured. That is not masking, it is deletion of a field that never
// held a credential -- the read-only console would show a file destination with
// no filename, and #150's own second property says values are masked, never
// destroyed.
//
// The test is on the VALUE and not only on the kind. Gating on kind:file alone
// would have left the audio-file form broken in exactly the same way, and
// gating on kind alone would also pass a URL through untouched if one were ever
// stored in a file destination -- Validate's "no leading slash, no .." rules do
// not actually stop "rtmp://host/app/KEY" from being saved as a filename. So a
// target is left alone when its kind may carry a filename AND it does not look
// like a URL; anything with a scheme is masked whatever kind claims it.
func maskDestinationTarget(kind db.DestKind, raw string) string {
	if destinationTargetIsFilename(kind, raw) {
		return raw
	}
	return maskURL(raw)
}

func destinationTargetIsFilename(kind db.DestKind, raw string) bool {
	switch kind {
	case db.DestFile, db.DestAudio:
		return !strings.Contains(raw, "://")
	default:
		return false
	}
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
// automod.model.endpoint is masked, and the reason is the same one that made
// mqtt.brokerUrl a mask rather than a nothing. The blob carries only
// automod.model.hasApiKey, a derived boolean, because the real key is sealed in
// its own table and set through its own endpoint -- and that was taken as
// settling the question for the whole automod block. It does not. The ENDPOINT
// is free text an operator pastes, and the shape a self-hosted or proxied
// inference endpoint most often arrives in is
// https://host/v1/chat/completions?api_key=sk-..., which this endpoint handed
// to a read token verbatim. A key in a query string is still a key; the sealed
// table protects the key the operator entered THERE, not the one they pasted
// into the URL field. alerts.RedactURL already masks api_key, so this is a
// gap in coverage rather than a new rule.
//
// The MQTT password and the automod model key ARE deliberately absent: those
// two are sealed in their own tables and this blob genuinely only ever carried
// derived booleans for them.
func readSafeSettings(s db.Settings) db.Settings {
	s.Ingest = readSafeIngest(s.Ingest)
	s.Failover.Backup.SRT.Passphrase = ""
	s.Failover.Backup.RTMP.StreamKey = ""
	s.Failover.Backup.Pull.URL = maskURL(s.Failover.Backup.Pull.URL)
	s.MQTT.BrokerURL = maskURL(s.MQTT.BrokerURL)
	s.Automod.Model.Endpoint = maskURL(s.Automod.Model.Endpoint)
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
//
// Through maskDestinationTarget rather than maskURL, because the same field
// holds a FILENAME for kind:file and for the file form of kind:audio, and
// masking a filename deletes it. See there.
//
// extraInputArgs and extraOutputArgs are CREDENTIAL-CLASS, and that follows
// from a decision this codebase had already made rather than from a new one.
// GET /destinations/{id}/expert is on readScopeDeniedPatterns -- 403 to a read
// token -- for a stated reason: its response is the resolved FFmpeg argv, and
// the argv contains the destination's stream key. These two fields ARE that
// argv, as the operator typed it, reaching the same principal through GET
// /destinations. The same bytes cannot have two answers depending on which
// route serves them, and of the two answers the 403 is the one somebody
// reasoned about. They were the last unjustified row in the classification
// table.
//
// Masked in place rather than blanked, because both carry `omitempty` and a
// blank would delete the key. See TestScrubbingIsNotAWholesaleWipe.
func readSafeDestination(d db.Destination) db.Destination {
	d.StreamKey = ""
	d.BackupStreamKey = redactInPlace(d.BackupStreamKey)
	d.URL = maskDestinationTarget(d.Kind, d.URL)
	d.BackupURL = maskDestinationTarget(d.Kind, d.BackupURL)
	d.ExtraInputArgs = redactInPlace(d.ExtraInputArgs)
	d.ExtraOutputArgs = redactInPlace(d.ExtraOutputArgs)
	return d
}

// redactInPlace removes a value while KEEPING ITS JSON KEY, for the fields that
// carry `omitempty` and would otherwise disappear.
//
// #150's second property is that redaction blanks values and never removes
// fields, so a read-scoped body has the same wire shape as an admin one. For a
// field tagged `omitempty` the ordinary spelling of that -- assigning "" --
// does the opposite: encoding/json drops the key entirely and the response a
// read token gets is a different shape from the one an admin gets. That is not
// a theoretical objection. destination.backupStreamKey shipped exactly that
// way, under a guard whose name says it cannot happen, because the guard
// compared zero-value fixtures where the field was already absent on both
// sides. See TestScrubbingIsNotAWholesaleWipe.
//
// An empty input stays empty: a destination with no backup key must look
// identical to every principal, and putting "[redacted]" where there was
// nothing would invent a credential that does not exist.
//
// alerts.Mask rather than a sentinel of this package's own, so the string an
// operator sees here is the string they already see in a log line and an alert.
func redactInPlace(s string) string {
	if s == "" {
		return s
	}
	return alerts.Mask
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
// BOTH request headers that carry a principal are named. Authorization is the
// bearer token; Cookie is the SESSION, and it was missing. The session is the
// principal that receives the UNREDACTED body -- it is the one whose response
// must never be replayed to somebody else -- and a cache keyed on
// Authorization alone treats "no Authorization header" as a single cache key
// shared by the signed-in operator and every anonymous caller. Naming only
// the header the redaction reads, rather than every header the principal is
// derived from, is the same class of mistake as the rest of this round: a
// correct-looking rule with one of its inputs left out.
//
// Vary alone would not be enough for a shared cache that ignores it, hence
// no-store as well. That is a belt-and-braces argument and not a reason to
// leave Vary incomplete: the two protect against different caches, and the one
// that honours Vary is the one that would otherwise be handed a correct-looking
// key.
//
// SCOPE. This covers the internal/api routes that call it. The playout MEDIA
// origin in internal/playout/handler.go serves segments with `Cache-Control:
// public` and no Vary at all, which is a real gap and a different one -- that
// handler is in another package, never reaches this function, and its responses
// do not vary by principal in the first place (see the gate in playout.go,
// where a read bearer is byte-identical to an anonymous viewer): it is a
// WATCH-token problem that exists for a purely anonymous deployment too. Filed
// as #155 rather than folded in here.
func principalVaryingResponse(w http.ResponseWriter) {
	w.Header().Add("Vary", "Authorization")
	w.Header().Add("Vary", "Cookie")
	w.Header().Set("Cache-Control", "private, no-store")
}
