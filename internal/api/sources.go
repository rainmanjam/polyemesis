package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/rtmpserver"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
)

// Sources are the multi-programme endpoints: one install, several independent
// ingests, each with its own destinations and renditions.
//
// Reconcile goes through the manager rather than a single engine here, because
// creating or deleting a source changes which engines should exist at all --
// s.eng().Reconcile() would only ever reconcile the default programme and a new
// source would sit in the database doing nothing until a restart.

// sourceView is a source plus the things the UI needs but does not store: the
// publish URLs an operator pastes into OBS, and whether this programme is the
// one unscoped requests act on.
type sourceView struct {
	*db.Source
	// PublishURLs are ready to paste into an encoder, one per protocol this
	// source accepts, with <server> where the hostname goes. Built here rather
	// than in the UI so the URL syntax lives next to the code that produces the
	// listener it has to match. See publishURLs for why the token is NOT in
	// them.
	PublishURLs map[string]string `json:"publishUrls"`
	IsDefault   bool              `json:"isDefault"`
	// TokenEnforced reports whether the publish token actually gates anything.
	// True for SRT and RTMP alike now -- both are one listener demultiplexed by
	// token -- and only when that listener is actually bound. The UI reads this
	// rather than assuming, so nobody is told a rotated token protects an
	// ingest that it does not.
	TokenEnforced bool `json:"tokenEnforced"`
	// LegacyRTMPKey is a pre-one-port stream key that still reaches this source,
	// empty when none does. Set only on an install upgraded from a build where
	// the stream key WAS the address; see engine.legacyRTMPKeys. Surfaced so
	// the page can tell an operator their encoder is on a grandfathered
	// address, rather than leaving the URL on screen and the URL in OBS
	// disagreeing with nothing to say which is real.
	LegacyRTMPKey string `json:"legacyRtmpKey,omitempty"`
	// Publishing reports whether an encoder is live on the shared listener for
	// this source right now.
	Publishing bool `json:"publishing"`
	// Link is this publisher's uplink health, when one is connected on the
	// shared listener. Surfaced per source because with several programmes on
	// one install, "why is it breaking up" is a question about one encoder's
	// uplink and not about the server -- and answering it per programme is
	// something Restreamer's UI does not do.
	Link *srtserver.LinkStats `json:"link,omitempty"`
	// RTMPLink is the same thing for a source on the RTMP listener.
	//
	// A SECOND FIELD rather than one union type, because the two carry
	// genuinely different facts. SRT reports RTT, loss and retransmits because
	// the protocol measures them; TCP has none of those numbers to report, so a
	// merged struct would be half zero for every RTMP source and a reader could
	// not tell "no loss" from "loss is not a thing here". A second field also
	// keeps `link` meaning exactly what it meant before, which is what stops
	// this from being a breaking change to every existing consumer.
	RTMPLink *rtmpserver.LinkStats `json:"rtmpLink,omitempty"`
	// Destinations and Renditions are what a delete would take with it.
	//
	// Counts rather than prose, and computed server-side rather than guessed by
	// the UI: a confirmation that says "this also removes 3 destinations and 1
	// rendition" is a decision, and one that says "and its destinations" is a
	// click. Shingo's fixed-value method -- confirm a number.
	Destinations int `json:"destinations"`
	Renditions   int `json:"renditions"`
	// Running reports whether an engine actually came up for this source. A
	// source whose ingest port was already taken is stored but not running, and
	// a UI that showed it as configured-and-fine would be lying about why
	// nothing arrives.
	Running bool `json:"running"`
	// ListenerHealth is the THIRD state between bound and absent, and it is
	// here because TokenEnforced and Running are both booleans derived from a
	// listener that can be half up.
	//
	// A wildcard SRT listener binds one socket per address family and survives
	// one of them failing -- deliberately, because a container without IPv6 is
	// a legitimate deployment. Everything on this card then reported the source
	// as running with its token enforced, which is true for the family that
	// bound and a flat lie to the operator whose encoder is on the other one.
	// Omitted from the JSON when there is nothing to say. See #105.
	ListenerHealth *engine.ListenerHealth `json:"listenerHealth,omitempty"`
}

// readScopeCannotSeePublishTokens reports whether this request's principal must
// have the source's publish credential withheld.
//
// A read-scoped token is promised to be read-only, and a source's token is the
// one piece of readable state that breaks that promise: the token IS the
// address on both listeners, so anything holding it can PUBLISH -- inject video
// into somebody's live programme -- using nothing but a GET it was explicitly
// allowed to make. "Read-only" would then mean "read-only, plus it can take
// over your broadcast", which is not a sentence worth shipping.
//
// The scope model refuses writes by HTTP method, and that is the right shape
// for a rule about routes; it cannot see that one GET's response body is itself
// a credential. This is the exception, and it is handled where the credential
// is serialised rather than by carving GET /sources out of the read scope --
// the listing is genuinely useful to a monitoring script, and it stays useful
// with the secret removed.
//
// Session principals and admin tokens are unaffected: the console needs the
// token to show the operator, and an admin token could rotate it anyway.
func readScopeCannotSeePublishTokens(r *http.Request) bool {
	p, ok := principalFrom(r.Context())
	return ok && p.token != nil && p.token.Scope != db.ScopeAdmin
}

func (s *Server) viewSource(r *http.Request, src *db.Source, defaultID int64) sourceView {
	var link *srtserver.LinkStats
	var rtmpLink *rtmpserver.LinkStats
	legacyKey := ""
	publishing := false
	// Derived from the RUNNING listener, never from configuration. Tokens are
	// how every SRT and every RTMP source is addressed now, so the only way
	// they are not enforced is that the listener for that protocol is not up --
	// and reporting "enforced" while nothing is bound is the exact false
	// assurance this field exists to prevent.
	//
	// It asks per protocol because the two listeners fail independently: 1935
	// being held by something else says nothing about 6000.
	tokenEnforced := (src.Ingest.Mode == db.IngestSRT || src.Ingest.Mode == db.IngestRTMP) &&
		s.mgr != nil && s.mgr.ListenerBound(src.Ingest.Mode)
	// Reported beside tokenEnforced rather than folded into it: a half-bound
	// listener DOES enforce the token for everyone who can reach it, so turning
	// that boolean off would answer a different question wrongly. See #105.
	var health *engine.ListenerHealth
	if s.mgr != nil {
		if h := s.mgr.ListenerHealth(src.Ingest.Mode); h.State != "" {
			health = &h
		}
	}
	// The ports are install-wide, so the publish URL comes from the settings
	// rather than from the source. Defaults on a read failure: a URL with the
	// wrong port is more useful than no URL at all, and the Sources page is
	// where an operator goes precisely when something is not working.
	listeners := db.DefaultSettings().Listeners
	if st, err := s.store.GetSettings(); err == nil {
		listeners = st.Listeners
	}
	if s.mgr != nil {
		publishing = s.mgr.SharedIngestPublishing(src.ID)
		link = linkForCard(s.mgr.SRTLinks(), src.ID)
		rtmpLink = rtmpLinkForCard(s.mgr.RTMPLinks(), src.ID)
		legacyKey = s.mgr.LegacyRTMPKey(src.ID)
	}
	urls := publishURLs(src, listeners)
	if readScopeCannotSeePublishTokens(r) {
		// A COPY, because src points at the caller's row and blanking the
		// token in place would hand the next reader -- including the store's
		// own update path -- a source whose credential has been erased.
		redacted := *src
		redacted.Token = ""
		// And the STORED ingest block, which is the half this originally
		// missed. sourceView embeds *db.Source, so every leaf of db.Source
		// marshals at the top level of the response -- including
		// ingest.srt.passphrase, ingest.rtmp.streamKey and an
		// ingest.pull.url carrying rtsp://user:pass@ userinfo.
		//
		// Blanking legacyRtmpKey below without this was measurably a NO-OP:
		// engine.legacyRTMPKeys computes that key as exactly
		// src.Ingest.RTMP.StreamKey, so the identical string came straight
		// back two JSON fields away. See internal/api/redact.go.
		redacted = readSafeSource(redacted)
		src = &redacted
		// PrevToken is NOT blanked here, and deliberately so: it carries
		// `json:"-"` and has never left the process, so clearing it would
		// suggest to the next reader that it once did.
		// The URLs go too, and they are the reason this cannot be a one-line
		// blanking of the token field: every publish URL has the token EMBEDDED
		// in it, because the token is the address. Leaving them would hand back
		// the same secret in a different shape.
		urls = nil
		// legacyRtmpKey is REDACTED IN PLACE rather than blanked, because it
		// carries `omitempty`: assigning "" deleted the key outright, so the
		// read-scoped body was a different SHAPE from the admin one and #150's
		// own "the wire shape does not change" claim was false here. The old
		// guard could not see it -- it compared zero-value fixtures, where the
		// field is absent either way, and only at the top level. See
		// TestViewShapesAreIdenticalByPrincipal.
		legacyKey = redactInPlace(legacyKey)
	}
	return sourceView{
		Publishing:     publishing,
		Link:           link,
		RTMPLink:       rtmpLink,
		Source:         src,
		PublishURLs:    urls,
		IsDefault:      src.ID == defaultID,
		TokenEnforced:  tokenEnforced,
		LegacyRTMPKey:  legacyKey,
		Running:        s.mgr != nil && s.mgr.Engine(src.ID) != nil,
		ListenerHealth: health,
	}
}

// linkForCard picks the one uplink a source card should show.
//
// A source can have TWO live links since redundant ingest shipped: a primary
// encoder, and a standby publishing to <token>.backup. Both carry the same
// SourceID, so "the first one matching this id" stopped being a well-defined
// answer -- it returns whichever the map happened to yield first, and it can
// differ between two refreshes with nothing changed.
//
// The primary wins, because it is the feed that is on air and the card shows
// one set of numbers.
//
// A backup is still better than nothing. An operator whose primary has dropped
// is looking at this card precisely because the standby is carrying the show,
// and a blank uplink panel at that moment is the least useful thing it could
// do.
func linkForCard(links []srtserver.LinkStats, sourceID int64) *srtserver.LinkStats {
	return pickLink(links, sourceID, func(l srtserver.LinkStats) (int64, bool) {
		return l.SourceID, l.Backup
	})
}

// rtmpLinkForCard is the same choice on the RTMP listener. It delegates rather
// than repeating the rule, because "the primary wins, a standby beats nothing"
// is a product decision and two copies of it would eventually disagree about
// which card shows what.
func rtmpLinkForCard(links []rtmpserver.LinkStats, sourceID int64) *rtmpserver.LinkStats {
	return pickLink(links, sourceID, func(l rtmpserver.LinkStats) (int64, bool) {
		return l.SourceID, l.Backup
	})
}

// pickLink is linkForCard's rule, over any link type.
//
// Generic with an accessor rather than a shared interface: the two LinkStats
// types deliberately carry different fields (see sourceView.RTMPLink), so there
// is nothing to unify except the two facts this function actually reads.
func pickLink[T any](links []T, sourceID int64, of func(T) (id int64, backup bool)) *T {
	var backup *T
	for _, l := range links {
		id, isBackup := of(l)
		if id != sourceID {
			continue
		}
		stat := l
		if !isBackup {
			return &stat
		}
		if backup == nil {
			backup = &stat
		}
	}
	return backup
}

// publishURLs is what the operator pastes into an encoder.
//
// Both protocols carry the TOKEN, because the token is the only thing that
// identifies a source: every source is reached on one listener per protocol and
// told apart by it. A URL without the token addresses nothing.
//
// What separates and protects each source:
//
//   - SRT: the token, as the streamid. One listener, demultiplexed by it,
//     matched in constant time against every source's current and grace-period
//     token. A publisher presenting nothing, or something unrecognised, is
//     refused with a typed reason rather than quietly accepted into the wrong
//     programme.
//   - SRT, additionally: the passphrase, which is real AES encryption rather
//     than a string comparison.
//   - RTMP: the same token, as the path -- rtmp://host:PORT/APP/<token> --
//     matched the same way by internal/rtmpserver. It used to be
//     ingest.rtmp.streamKey checked as an FFmpeg playpath, which capped an
//     install at one RTMP source and could not be rotated.
//
// The RTMP entry is SPLIT: the map value is the server half and "streamKey"
// carries the token, because that is the two-box form OBS asks for. Putting the
// token in both would give the operator who fills in both /APP/<token>/<token>.
func publishURLs(src *db.Source, listeners db.ListenerSettings) map[string]string {
	const host = "<server>"

	// Nothing chosen, nothing to publish to.
	//
	// Without this, an unchosen source still produced a URL: IngestSpec's zero
	// Kind falls through to the SRT branch, so the map came back as
	// {"": "srt://<server>:6000?..."} and the Sources page showed an SRT
	// address on an install that had never chosen SRT. That is the silent
	// default this whole change exists to remove, arriving by the back door.
	if src.Ingest.Mode == db.IngestUnset {
		return map[string]string{}
	}
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(src.Ingest.Mode),
		SRTPort:       listeners.SRTPort,
		SRTPassphrase: src.Ingest.SRT.Passphrase,
		SRTLatencyMS:  src.Ingest.SRT.LatencyMS,
		RTMPPort:      listeners.RTMPPort,
		RTMPApp:       src.Ingest.RTMP.App,
		PullURL:       src.Ingest.Pull.URL,
	}
	u := spec.PublicIngestURL(host)
	if src.Ingest.Mode == db.IngestSRT && src.Token != "" {
		// The streamid IS the address. Appended here rather than inside
		// IngestSpec because the spec describes what the SERVER binds, and the
		// server binds one socket for every source -- the token only means
		// something on the publisher's side of it.
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "streamid=" + url.QueryEscape(src.Token)
	}
	out := map[string]string{string(src.Ingest.Mode): u}
	if src.Ingest.Mode == db.IngestRTMP {
		// Separate field: OBS wants the server and the key in two boxes, and
		// the key is what addresses this source. The TOKEN, not
		// ingest.rtmp.streamKey -- that field addresses nothing now, and
		// emitting it here would hand the operator a key their encoder cannot
		// reach anything with.
		out["streamKey"] = src.Token
	}
	return out
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	out := make([]sourceView, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.viewSource(r, row, defaultID))
	}
	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, s.viewSource(r, row, defaultID))
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	// Enabled before decoding, not after: a bool cannot distinguish "absent"
	// from "false" once decoded, and seeding it here means an omitted field
	// keeps true while an explicit "enabled": false still wins.
	//
	// The default has to be true. Someone who adds a source has just said they
	// want it; shipping it disabled means the encoder is refused with "source
	// disabled" and the operator has no reason to suspect the thing they just
	// created is off. That is exactly how it failed the first time this path
	// was exercised end to end.
	row := db.Source{Enabled: true}
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	// A payload that carries no ingest block would validate against the zero
	// value -- port 0, unknown mode -- and fail with three errors that say
	// nothing useful. Start from the defaults so the smallest useful request is
	// {"name":"Vertical"} and the operator edits ports afterwards.
	if row.Ingest.Mode == "" {
		row.Ingest = db.DefaultSettings().Ingest
	}
	if err := s.store.CreateSource(&row); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Through the manager: a new source needs an engine built for it, which
	// only Sync does.
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source create", "err", err)
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusCreated, s.viewSource(r, &row, defaultID))
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	// Start from the stored row so a PUT that omits the token does not blank a
	// secret the operator has already pasted into an encoder.
	row := *existing
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = id
	if err := s.store.UpdateSource(&row); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source update", "err", err)
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusOK, s.viewSource(r, &row, defaultID))
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Deleting a source takes its destinations and renditions with it, which
	// the schema does by CASCADE. Its recordings survive with a NULL source_id.
	if err := s.store.DeleteSource(id); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source delete", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateSourceToken issues a new publish secret.
//
// The old one stops working the moment this returns, so the response carries
// the new URLs: an operator who rotates and then cannot find what to paste has
// taken their own ingest down.
func (s *Server) handleRotateSourceToken(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.RotateSourceToken(id); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	row, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusOK, s.viewSource(r, row, defaultID))
}

func sourceStatus(err error) int {
	if errors.Is(err, db.ErrSourceNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
