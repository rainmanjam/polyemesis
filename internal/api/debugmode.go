package api

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/diag"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// Debug mode: the toggle, and the export.
//
// TWO ROUTES WITH DELIBERATELY DIFFERENT WEIGHT, and the asymmetry is the whole
// design. Turning recording on is reversible, affects only this box, and
// discloses nothing -- an ordinary admin session is enough. The EXPORT produces
// an artefact intended to leave the machine and reach somebody who does not have
// it, so it carries a confirmation in the UI and an audit event here.
//
// WHY THE EXPORT IS A POST. It is not a read: it mints an artefact, it is
// audited, and it must not be reachable by following a link or by a browser
// prefetching one. A GET that produces an audit entry every time something
// speculatively fetches it would make the audit trail useless for the one
// question it exists to answer -- who took a copy of the logs, and when.
//
// WHAT THE BUNDLE CONTAINS IS internal/diag's PROBLEM, NOT THIS FILE'S. Nothing
// here scrubs anything: the records were scrubbed on the way INTO the ring, by
// the secret set, because a buffer holding plaintext credentials until export
// time is the failure this feature is designed around. See package diag.

// debugState is what the UI renders.
type debugState struct {
	Recording bool   `json:"recording"`
	Level     string `json:"level"`
	// Held, Seen and Capacity let the UI say "5,000 of 22,431 lines kept"
	// rather than showing a number that looks complete and is not.
	Held     int    `json:"held"`
	Seen     uint64 `json:"seen"`
	Capacity int    `json:"capacity"`
	// Bytes is the measured payload size of what is held, so the confirmation
	// dialog can state how large the bundle is BEFORE it is sent. Approximate --
	// it excludes the JSON envelope -- which is the right precision for a
	// sentence an operator reads, and the wrong one to compute a Content-Length
	// from.
	Bytes int `json:"bytes"`
	// RecordsTruncated is how many captured lines were cut at the per-record
	// cap. A different claim from Held < Seen, which is about whole lines the
	// ring dropped.
	RecordsTruncated uint64 `json:"recordsTruncated"`
}

func (s *Server) debugStateNow() debugState {
	st := debugState{}
	if s.diag == nil {
		return st
	}
	recs := s.diag.Records()
	st.Recording = s.diag.Recording()
	st.Held = len(recs)
	st.Seen = s.diag.Seen()
	st.Capacity = s.diag.Capacity()
	st.Bytes = s.diag.Bytes()
	st.RecordsTruncated = s.diag.TruncatedCount()
	if s.diagLevel != nil {
		st.Level = s.diagLevel.Level().String()
	}
	return st
}

// handleGetDebug reports whether recording is on and how much is held.
//
// ANSWERS EVEN WHEN DEBUG MODE IS NOT WIRED, with recording:false. Copying
// handleAccountStats' doctrine: "we cannot offer this" and "the route is gone"
// are different problems, and a UI that cannot tell them apart shows the wrong
// one. A 404 here would make a build without the recorder look broken.
func (s *Server) handleGetDebug(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.debugStateNow())
}

// handleSetDebug turns recording on or off.
//
// A session OR AN ADMIN API TOKEN authorises this, and the earlier comment here
// said "the session is the authorisation", which was wrong. requireScope
// (api.go:1648) passes an admin-scoped token straight through, so anything
// holding one can toggle recording and export a bundle WITHOUT the UI
// confirmation ever being drawn.
//
// That is the correct behaviour -- an admin token is admin -- but it means the
// confirmation dialog is a courtesy to a human, not a control on the route. The
// control on the route is the audit entry, which fires either way.
//
// The toggle itself is still cheap to authorise: it changes what THIS box
// records, nothing leaves it, and the operator can already read every one of
// those lines with journalctl.
func (s *Server) handleSetDebug(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Recording *bool `json:"recording"`
		// Reset empties the ring, so an operator can start a clean reproduction
		// instead of exporting a buffer full of unrelated history.
		Reset bool `json:"reset"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.diag == nil {
		writeError(w, http.StatusPreconditionFailed,
			"debug recording is not available on this build")
		return
	}
	if body.Reset {
		s.diag.Reset()
	}
	if body.Recording != nil {
		// REBUILD THE SECRET SET AS RECORDING STARTS, because the scrubbing is
		// only as good as the literals it was given and a set built at boot is
		// stale by the first key refresh. This is the moment it matters most:
		// nothing has been captured yet, so everything the ring is about to hold
		// is covered by a set assembled from the destinations as they are now.
		//
		// THE RESIDUAL IS A KEY ROTATED WHILE RECORDING IS ALREADY ON. That
		// literal is one this set has never held, and alerts.Redact is what
		// stands behind it -- see diag.Recorder.SetSecrets, which exists so
		// whatever reconciles destinations can close the gap.
		if *body.Recording {
			// SOURCES AS WELL AS DESTINATIONS, and the first version had only
			// destinations -- which covers where a stream GOES and nothing about
			// where it comes FROM. A pull source is addressed by a URL that
			// routinely carries credentials (rtsp://user:pass@, a CDN token in
			// the query), engine.go logs that URL, and everything in it therefore
			// reached the exported bundle behind nothing but the residual
			// alerts.Redact pass. The publish token goes in for the same reason:
			// a token in a bundle sent to a stranger is a stranger who can
			// publish.
			// AND ACCOUNTS, WHICH WERE THE THIRD OMISSION IN A ROW. This started
			// as destinations only; a review added sources; a review of THAT found
			// platform accounts had never been here at all -- and they hold the
			// OAuth access and refresh tokens. The path is concrete: a failed
			// refresh keeps a 300-character snippet of the provider's response
			// body, oauth_handlers.go logs it, and a body echoing the token puts a
			// literal in the ring that the declared set has never heard of.
			//
			// The pattern worth naming is that each omission was an inventory
			// nobody enumerated, not a mistake in the code that was written.
			dests, dErr := s.store.ListDestinations()
			srcs, sErr := s.store.ListSources()
			accts, aErr := s.store.ListPlatformAccounts()
			if dErr == nil && sErr == nil && aErr == nil {
				lits := append(engine.DestinationSecrets(dests), engine.SourceSecrets(srcs)...)
				lits = append(lits, engine.AccountSecrets(accts)...)
				s.diag.SetSecrets(alerts.NewSecretSet(s.log, lits...))
			} else {
				// Refused rather than recorded: turning on a recorder whose
				// scrub cannot be built is how a bundle full of stream keys
				// gets made, and the operator would have no way to know.
				writeError(w, http.StatusServiceUnavailable,
					"could not read the destinations, sources and connected accounts "+
						"needed to build the redaction set, so recording "+
						"was not started")
				return
			}
		}
		s.diag.SetRecording(*body.Recording)
		// The LEVEL moves with the switch. Recording at the configured level
		// captures exactly what the operator already had, which is not what
		// anybody means by "turn on debug mode" -- and the commonest reason a
		// capture comes back useless is that it was taken at info.
		if s.diagLevel != nil {
			s.diagLevel.SetDebug(*body.Recording)
		}
		s.log.Info("debug recording changed", "recording", *body.Recording)
	}
	writeJSON(w, http.StatusOK, s.debugStateNow())
}

// handleExportDebug returns the bundle.
//
// AUDITED, because this is the moment a copy of the server's own logs leaves the
// operator's control. The audit entry is the only durable record that it
// happened: the bundle itself is handed to the caller and polyemesis keeps no
// copy, deliberately -- storing one would create a second place credentials
// could be read from, which is precisely what the scrub-on-the-way-in design
// exists to avoid.
func (s *Server) handleExportDebug(w http.ResponseWriter, r *http.Request) {
	if s.diag == nil {
		writeError(w, http.StatusPreconditionFailed,
			"debug recording is not available on this build")
		return
	}

	b := diag.Build(s.version, runtime.GOOS+"/"+runtime.GOARCH, s.diag, s.diagLevel, time.Now().UTC())

	// Named with the time so two bundles in one support thread can be told
	// apart, which is otherwise a real problem for whoever receives them.
	name := fmt.Sprintf("polyemesis-debug-%s.json", b.GeneratedAt.Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

	// BEFORE THE WRITE, ON PURPOSE, and this was queried in review. Publishing
	// after a successful Encode would miss the case that matters most: a write
	// that fails halfway has ALREADY put bytes on the wire, so the disclosure
	// happened and an audit trail that omitted it would be wrong in the
	// dangerous direction. The cost is that a connection dropped before the
	// first byte is recorded as an export that did not deliver -- an audit trail
	// that over-reports a disclosure is the right way round.
	s.publishAudit(auditDebugExported(len(b.Records), b.Capture.Truncated, s.clientIP(r)))

	if err := b.Encode(w); err != nil {
		// The status is already written; all that is left is to say so
		// somewhere an operator can find it.
		s.log.Error("debug bundle write failed", "err", err)
	}
}
