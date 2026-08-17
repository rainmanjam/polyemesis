package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// Facebook's two broadcast-side reads and its one broadcast-side write, exposed
// over HTTP.
//
// THE UI FOR THESE WAS BUILT FIRST AND SHIPPED AGAINST NOTHING. internal/oauth
// grew EndBroadcast and StreamHealth, the destination card grew a menu item and
// a health pane, and no route joined them -- so "End broadcast" would have
// 404ed while the stream stayed live, which is worse than no button at all.
// That is the gap this file closes, and it is worth naming because the same
// shape recurs: a capability is only real when every layer between the platform
// and the operator exists.
//
// BOTH ARE SCOPED TO A DESTINATION, NOT TO AN ACCOUNT, and that is the whole
// reason they are here rather than beside handleAccountStats. A Facebook
// account can hold many broadcasts; the live video these act on is the one
// recorded against THIS destination, in db.FacebookSettings. Keying them by
// account would make "end the broadcast" ambiguous in exactly the situation an
// operator reaches for it -- several destinations live at once.

// facebookBroadcastFor resolves the three things both handlers need, or reports
// which one is missing in words an operator can act on.
//
// The order matters and is not arbitrary. The destination is read first because
// a bad id is the caller's mistake; the platform is checked before the account
// because "this is not a Facebook destination" is a clearer answer than "no
// account"; and the broadcast id is checked LAST because its absence is the
// only one of the four that is a normal, expected state rather than a
// misconfiguration -- a destination that has not gone live yet has no live
// video, and saying so is not an error report.
// IT RETURNS A STATUS, AND THE ROUTE LEDGER IS WHY. The first version handed
// every failure to writeStoreError, which maps an unrecognised error to 500 --
// so asking a TWITCH destination for Facebook stream health answered "500
// destination \"twitch\" is not a Facebook destination". That is a client
// mistake stated as a server fault, and the read-scope sweep caught it by
// driving the route against its own fixture, where destination 1 is Twitch.
// A 500 also reads as "polyemesis is broken" to anything retrying on 5xx.
func (s *Server) facebookBroadcastFor(ctx context.Context, id int64) (
	row *db.Destination, acct *db.PlatformAccount, broadcastID string, status int, err error) {

	row, err = s.store.GetDestination(id)
	if err != nil {
		// Not found, or a real store failure. writeStoreError already tells
		// those apart, so 0 means "let it decide".
		return nil, nil, "", 0, err
	}
	if row.Platform != db.PlatformFacebook {
		return nil, nil, "", http.StatusBadRequest,
			fmt.Errorf("destination %q is not a Facebook destination", row.Name)
	}
	if row.AccountID == nil {
		return nil, nil, "", http.StatusPreconditionFailed,
			fmt.Errorf("destination %q has no connected Facebook account; "+
				"these actions go through the account that created the broadcast", row.Name)
	}
	// tokenFor rather than GetPlatformAccount: it refreshes an expired token,
	// which is the difference between this working an hour into a broadcast and
	// failing with a 190 the operator cannot read.
	acct, err = s.tokenFor(ctx, *row.AccountID)
	if err != nil {
		// An unusable or unrefreshable token is the operator's to fix, and
		// tokenFor's message already says how.
		return nil, nil, "", http.StatusPreconditionFailed, err
	}
	return row, acct, strings.TrimSpace(row.Facebook.BroadcastID), 0, nil
}

// facebookProvider narrows the registered provider to Facebook's concrete type.
//
// Through the Set rather than oauth.Get, for the reason endpoints.go gives
// about every other capability lookup: a Server built with a stubbed Set must
// not fall through to a production provider and talk to graph.facebook.com in
// a test.
func (s *Server) facebookProvider() (*oauth.Facebook, bool) {
	pr, ok := s.providers.All()[db.PlatformFacebook]
	if !ok {
		return nil, false
	}
	fb, ok := pr.(*oauth.Facebook)
	return fb, ok
}

// handleEndFacebookBroadcast ends the live video recorded against a destination.
//
// A WRITE THAT CANNOT BE UNDONE FROM HERE, which is why it is a POST behind
// requireCSRF and why the UI puts a confirmation in front of it. Facebook turns
// the broadcast into a VOD rather than deleting it, so the artefact survives --
// but it does not go back to live, and no call in this file will bring it back.
//
// A refusal is a real error rather than a quiet 200. EndBroadcast's own comment
// records why: an empty id makes the underlying request a POST to "/", which
// Graph answers in a way that reads as success, and reporting a still-live
// broadcast as ended is the one wrong answer that matters here.
func (s *Server) handleEndFacebookBroadcast(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	row, acct, broadcastID, status, err := s.facebookBroadcastFor(ctx, id)
	if err != nil {
		if status != 0 {
			writeError(w, status, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	if broadcastID == "" {
		// 409 rather than 404: the destination exists and the route exists, and
		// what is absent is a broadcast to act on. A 404 here would read as
		// "polyemesis does not support this", which is the opposite of true.
		writeError(w, http.StatusConflict,
			"no Facebook broadcast is recorded for this destination, so there is nothing to end")
		return
	}
	fb, ok := s.facebookProvider()
	if !ok {
		writeError(w, http.StatusPreconditionFailed, "the Facebook provider is not configured")
		return
	}

	end, err := fb.EndBroadcast(ctx, acct.AccessToken, acct.AccountRef, broadcastID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Logged rather than audited, DELIBERATELY AND PROBABLY TEMPORARILY. The
	// audit trail takes typed constructors (auditSettingsChanged and friends in
	// audit.go), so a new event here means a new constructor and a new entry in
	// whatever classifies them -- worth doing, and not worth doing inside the
	// commit that first exposes the route. Ending somebody's public broadcast
	// is exactly the kind of act an audit trail exists for, so this is a
	// deliberate gap rather than an oversight: see the audit.go pattern.
	s.log.Info("ended a Facebook broadcast",
		"destination", row.Name, "broadcast", broadcastID, "ended", end.Ended)

	// Ended false with no error is an ORDINARY outcome and must not read as a
	// failure: Facebook accepted the end and has not yet reported VOD. The
	// warnings say what was actually seen, in the shape MetadataResult uses, so
	// the UI can render them the way it renders every other per-platform result.
	writeJSON(w, http.StatusOK, map[string]any{
		"ended":    end.Ended,
		"status":   end.Status,
		"warnings": end.Warnings,
	})
}

// handleFacebookStreamHealth reads what Facebook sees arriving at its ingest.
//
// ANSWERS 200 WITH supported:false RATHER THAN 404 when there is nothing to
// report, copying handleAccountStats exactly. "We cannot ask" and "the
// destination is gone" are different problems with different fixes, and a
// client that cannot tell them apart shows the wrong one.
//
// Facebook is the only platform here that publishes encoder health at all --
// the word "bitrate" appears zero times in Twitch's entire Helix reference --
// so the absence of this data elsewhere is a fact about the platform and not a
// gap in polyemesis. The UI is required to say so in those terms.
//
// POLLING IS BOUNDED BY FACEBOOK, IN ITS OWN WORDS: "Stream health data
// refreshes every 2 seconds, so limit queries to no more than once every 2
// seconds. A stream timeout will be detected and reported after 4 seconds of no
// data being received." That is a stated number, so it may be relied on -- but
// it is a floor on the CLIENT's interval, and nothing here enforces it. A
// caller that polls faster gets Facebook's refusal rather than ours.
func (s *Server) handleFacebookStreamHealth(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	_, acct, broadcastID, status, err := s.facebookBroadcastFor(ctx, id)
	if err != nil {
		if status != 0 {
			writeError(w, status, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	if broadcastID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": false,
			"reason":    "no Facebook broadcast is recorded for this destination yet",
		})
		return
	}
	fb, ok := s.facebookProvider()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": false,
			"reason":    "the Facebook provider is not configured",
		})
		return
	}

	streams, err := fb.StreamHealth(ctx, acct.AccessToken, acct.AccountRef, broadcastID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// An EMPTY list is a 200 and not an error. Facebook reports no ingest
	// streams for a broadcast nothing is arriving at, which is exactly the
	// state an operator opens this pane to diagnose -- returning an error there
	// would hide the answer behind a failure.
	out := make([]map[string]any, 0, len(streams))
	for _, st := range streams {
		out = append(out, map[string]any{"id": st.ID, "health": st.Health})
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "streams": out})
}
