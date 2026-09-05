package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

func (s *Server) handlePlatformGuides(w http.ResponseWriter, r *http.Request) {
	guides := oauth.Guides()
	// Render the exact redirect URI to whitelist, using the origin the user is
	// actually browsing. Getting this wrong is the single most common cause of
	// a failed OAuth setup, so we compute it rather than describe it.
	origin := s.origin(r)
	for i := range guides {
		if guides[i].RedirectPath != "" {
			guides[i].RedirectPath = origin + guides[i].RedirectPath
			// Preflight the URI we are about to tell them to register, rather
			// than letting the platform reject it after the fact.
			guides[i].RedirectWarnings = redirectWarnings(s.cfg, r, guides[i].RedirectPath)
		}
	}
	writeJSON(w, http.StatusOK, guides)
}

// origin reconstructs the browser-visible base URL.
func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || s.cfg.ServesTLS() {
		scheme = "https"
	}
	if s.cfg.TrustProxyHeaders {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		}
	}
	host := r.Host
	if s.cfg.TrustProxyHeaders {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
	}
	return scheme + "://" + host
}

func (s *Server) redirectURI(r *http.Request, platform db.Platform) string {
	return fmt.Sprintf("%s/api/v1/oauth/%s/callback", s.origin(r), platform)
}

func (s *Server) handleListCreds(w http.ResponseWriter, r *http.Request) {
	creds, err := s.store.ListPlatformCreds()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, creds)
}

func (s *Server) handlePutCreds(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	if _, err := s.providers.Get(platform); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
		writeError(w, http.StatusBadRequest, "both a client ID and a client secret are required")
		return
	}
	if err := s.store.PutPlatformCreds(s.box, platform, req.ClientID, req.ClientSecret); err != nil {
		writeStoreError(w, err)
		return
	}
	// The check runs AFTER the store succeeds, and its verdict never changes
	// the status code. An operator is usually part-way through a platform
	// console when they paste these; refusing to save a credential they are
	// three clicks from making valid is obstructive rather than protective.
	//
	check := s.providers.CheckCredentialsFor(r.Context(), platform, req.ClientID, req.ClientSecret)
	writeJSON(w, http.StatusOK, map[string]any{
		"platform":  platform,
		"hasSecret": true,
		"check":     check,
	})
}

// handleCheckCreds re-runs the check against what is stored, so an operator who
// has just fixed something in the platform console can retest without pasting
// the secret again -- which they frequently cannot, because most consoles show
// a client secret exactly once.
func (s *Server) handleCheckCreds(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	if _, err := s.providers.Get(platform); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		s.providers.CheckCredentialsFor(r.Context(), platform, creds.ClientID, creds.ClientSecret))
}

func (s *Server) handleDeleteCreds(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	if err := s.store.DeletePlatformCreds(platform); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// accountView is a stored account plus whether it still holds the permissions
// this build needs.
//
// Computed here rather than stored, because the answer changes when the BINARY
// changes, not when the row does: an operator who upgrades polyemesis has the
// same account row and a different verdict.
type accountView struct {
	db.PlatformAccount
	Reconnect oauth.ReconnectReason `json:"reconnect"`
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]accountView, 0, len(accts))
	for _, a := range accts {
		out = append(out, accountView{PlatformAccount: a, Reconnect: oauth.AccountNeedsReconnect(a)})
	}
	writeJSON(w, http.StatusOK, out)
}

// accountDestination is one destination that a disconnect would cut loose,
// named so the caller can say WHICH.
//
// The list travels on the refusal AND on the success, for the reason
// handleDeleteRendition returns its counts either way: an operator who
// confirmed still has to know what they just did, and a client that only ever
// sees a count cannot render a sentence with a destination's name in it.
type accountDestination struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	Platform db.Platform `json:"platform"`
	// Enabled is the operator's run/stop intent for this destination. It stays
	// true across the disconnect -- ON DELETE SET NULL clears account_id and
	// nothing clears this -- so an enabled row goes on being reconciled with no
	// account behind it.
	Enabled bool `json:"enabled"`
	// BroadcastID and Phase are the platform's own handle and word for the
	// broadcast this destination is driving, read the same way the lifecycle
	// coordinator reads them (the recorded id first, the announcement mirror
	// second) so that "this one has something in flight" means here exactly
	// what it means there.
	BroadcastID string `json:"broadcastId,omitempty"`
	Phase       string `json:"phase,omitempty"`
	// Broadcasting is that judgement, precomputed: there is a broadcast and the
	// platform has not said it is over.
	Broadcasting bool `json:"broadcasting"`
}

// blocks reports whether this destination is one a disconnect must not take by
// surprise.
func (a accountDestination) blocks() bool { return a.Enabled || a.Broadcasting }

// accountDeleteRefusal is the 409 body: the sentence, the branch key, and the
// rows the sentence is about.
//
// Shaped after upgradeRefusal for its stated reason -- internal/api's whole
// error surface is {"error": ...} and the SPA's fetch wrapper reads that field
// and nothing else, so a refusal that omitted it would reach an operator as
// "request failed (409)". Everything beside it is what a client needs to build
// the confirmation for itself.
type accountDeleteRefusal struct {
	Error        string               `json:"error"`
	Code         string               `json:"code"`
	Destinations []accountDestination `json:"destinations"`
}

// codeAccountInUse is the branch key for that refusal. A client matches on this
// rather than on the English, and answers it by re-sending the same request
// with {"confirm": true}.
const codeAccountInUse = "account_in_use"

// handleDeleteAccount disconnects a platform account, and refuses to do it
// blind while destinations are still hanging off it.
//
// WHAT AN UNGUARDED DISCONNECT COSTS, which is why this counts first the way
// handleDeleteRendition does. Deleting the row is not the end of it: the
// destinations' account_id is ON DELETE SET NULL, so every one of them is
// rewritten in place, keeps its enabled flag, and becomes a destination with a
// stream key nothing can refresh and a broadcast nothing can end. The lifecycle
// coordinator then drops them -- internal/api/lifecycle.go untracks any
// destination with no AccountID, deliberately, because with no token there is
// nothing it could send -- so a YouTube broadcast that was mid-flight is never
// completed. It stays on the channel as a live broadcast with no ingest, and
// the only remedy left is YouTube Studio.
//
// None of that is recoverable by reconnecting: the new account gets a new row
// id, and the destinations' account_id is already NULL.
//
// WHICH RUNG THIS REACHES, PER CASE. One number for the whole route is wrong
// in either direction, so the two cases are stated separately.
//
// THE UNCONFIRMED CASE IS RUNG 1, CONTROL. A request that has not said
// {"confirm": true} while something is enabled or mid-broadcast deletes
// NOTHING: the account row is still there, the destinations still point at it,
// and there is no window in which the delete has happened and the operator is
// reading a warning about it. The mistake this exists for -- disconnecting an
// account that is still carrying live destinations, in one unconsidered click
// -- cannot be made. That is control, not a warning.
//
// THE CONFIRMED CASE IS RUNG 2, WARNING, AND DELIBERATELY SO. {"confirm": true}
// is an operator override: the refusal has already named the destinations and
// said what disconnecting costs, and the operator has decided anyway. The
// delete proceeds and the same list comes back in `warnings`, past tense.
// Control here would mean refusing outright, which is not available: `blocks()`
// fires on Enabled alone and a normal install leaves its destinations enabled,
// so an outright refusal would leave most accounts with no way to be
// disconnected at all -- and an operator who cannot proceed goes around the
// guard, at which point the product has taught them to.
//
// So: rung 1 against the MISTAKE, rung 2 against the DECISION. Calling the
// route "rung 2 overall" understates the unconfirmed path, which is the one
// that fires by default; calling it "rung 1" claims a control over a choice
// that is not a mistake.
//
// The dialog in the SPA is not the device and could not be. It is one client;
// a script, a terminal, or a second UI reaches this route directly, and a
// confirmation that only exists in one caller protects only that caller. It is
// still REQUIRED, for the opposite reason: this refusal is the ordinary answer
// rather than a rare one, so a client with no branch for it has a Disconnect
// button that does nothing on most accounts. ui/src/pages/SettingsPage.tsx
// renders the list and re-sends confirmed; ACCOUNT_IN_USE in ui/src/lib/api.ts
// is the key it matches on, pinned against the constant below by
// ui/src/lib/account-in-use-code.test.ts.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	confirmed, ok := deleteConfirmed(w, r)
	if !ok {
		return
	}
	// COUNTED BEFORE THE DELETE, and it has to be: once the row is gone the
	// destinations no longer point at it, and there is no query left that could
	// say which ones used to.
	attached, err := s.accountDestinations(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if blocking := blockingDestinations(attached); len(blocking) > 0 && !confirmed {
		writeJSON(w, http.StatusConflict, accountDeleteRefusal{
			Error:        accountDeleteRefusalMessage(blocking),
			Code:         codeAccountInUse,
			Destinations: attached,
		})
		return
	}
	if err := s.store.DeletePlatformAccount(id); err != nil {
		writeStoreError(w, err)
		return
	}
	resp := map[string]any{"status": "disconnected", "destinations": attached}
	if len(attached) > 0 {
		resp["warnings"] = []string{accountDeleteWarning(attached)}
	}
	writeJSON(w, http.StatusOK, resp)
}

// deleteConfirmed reads the optional {"confirm": true} body off a DELETE.
//
// AN ABSENT BODY IS NOT AN ERROR, which is why this is not decodeJSON: DELETE
// is routinely sent with no body at all, and every existing caller of this
// route sends none. Those callers must keep working -- and, being unconfirmed,
// must be the ones the refusal protects. A body that is present and malformed
// still fails loudly, because a client that MEANT to confirm and got the shape
// wrong must not be read as one that did not confirm; DisallowUnknownFields
// (inside decodeJSONInto) is what makes a typo in the field name land here
// rather than as a silent false.
//
// Returns (confirm, ok); ok is false when a response has already been written.
func deleteConfirmed(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, ok := readJSONBody(w, r)
	if !ok {
		return false, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return false, true
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSONInto(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false, false
	}
	return req.Confirm, true
}

// accountDestinations lists the destinations linked to one connected account.
//
// Read through ListDestinations rather than a query of its own for the reason
// keyIsSharedWithASibling gives: this layer owns the table and there is exactly
// one shape a destination is read in.
func (s *Server) accountDestinations(accountID int64) ([]accountDestination, error) {
	rows, err := s.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	out := []accountDestination{}
	for _, d := range rows {
		if d == nil || d.AccountID == nil || *d.AccountID != accountID {
			continue
		}
		// The recorded id wins over the announcement mirror, exactly as the
		// lifecycle coordinator resolves it: the mirror is rewritten whenever a
		// schedule moves, so it can name a broadcast that is not the one on air.
		broadcastID := strings.TrimSpace(d.Lifecycle.BroadcastID)
		if broadcastID == "" {
			broadcastID = strings.TrimSpace(d.Facebook.BroadcastID)
		}
		out = append(out, accountDestination{
			ID:          d.ID,
			Name:        d.Name,
			Platform:    d.Platform,
			Enabled:     d.Enabled,
			BroadcastID: broadcastID,
			Phase:       d.Lifecycle.Phase,
			// A broadcast the platform has already called complete or revoked
			// is finished business and blocks nothing -- isTerminalPhase is the
			// same test the coordinator uses to stop spending quota on it.
			// An UNRECOGNISED phase counts as in flight, which is the safe
			// direction: a second platform with its own vocabulary makes this
			// ask for confirmation rather than assume the show is over.
			Broadcasting: broadcastID != "" && !isTerminalPhase(d.Lifecycle.Phase),
		})
	}
	return out, nil
}

// blockingDestinations is the subset that must be confirmed before a disconnect.
func blockingDestinations(all []accountDestination) []accountDestination {
	out := []accountDestination{}
	for _, d := range all {
		if d.blocks() {
			out = append(out, d)
		}
	}
	return out
}

// accountDeleteRefusalMessage says what is in the way and how to proceed anyway.
//
// THE SERVER OWNS THE WORDING, for onAirRefusal's reason: the same refusal has
// to reach a terminal as well as a browser, and two phrasings is how they come
// to disagree. It names the destinations rather than counting them, because
// "3 destinations" is not something an operator can act on and "Main YouTube"
// is.
func accountDeleteRefusalMessage(blocking []accountDestination) string {
	msg := fmt.Sprintf("%s still on this connected account: %s.",
		plural(len(blocking), "destination is", "destinations are"), namesOf(blocking))
	if n := countBroadcasting(blocking); n > 0 {
		subject := "One of them is"
		if n > 1 {
			subject = fmt.Sprintf("%d of them are", n)
		}
		msg += fmt.Sprintf(" %s publishing to a broadcast the platform has not called finished, and "+
			"disconnecting now leaves it live with nothing able to end it — the remedy after that is the "+
			"platform's own studio.", subject)
	}
	return msg + " Disconnecting unlinks every one of them: they keep their settings and lose the " +
		"ability to refresh a stream key or end a broadcast, and reconnecting does not link them back. " +
		`Send this request again with {"confirm": true} to do it anyway.`
}

// accountDeleteWarning is what a confirmed disconnect reports afterwards. It is
// past tense and it is not a refusal: the operator has already decided.
func accountDeleteWarning(attached []accountDestination) string {
	return fmt.Sprintf("%s no longer linked to a connected account, and can no longer refresh a stream "+
		"key or end a broadcast: %s. Link them to another connected account, or paste a key by hand.",
		plural(len(attached), "destination is", "destinations are"), namesOf(attached))
}

// countBroadcasting is how many of these are mid-broadcast.
func countBroadcasting(list []accountDestination) int {
	n := 0
	for _, d := range list {
		if d.Broadcasting {
			n++
		}
	}
	return n
}

// namesOf renders the destinations for a sentence, capped so that an install
// with forty destinations on one account produces a sentence rather than a
// paragraph. The full list is always in the response body beside it.
func namesOf(list []accountDestination) string {
	const shown = 3
	names := make([]string, 0, shown)
	for i, d := range list {
		if i == shown {
			return strings.Join(names, ", ") + fmt.Sprintf(" and %d more", len(list)-shown)
		}
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

// handleOAuthStart redirects the browser to the platform's consent screen.
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	provider, err := s.providers.Get(platform)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed,
			fmt.Sprintf("no %s developer credentials configured. Add them in Settings → Platform credentials first.", platform))
		return
	}

	// The state parameter is this flow's CSRF token: single-use, stored
	// server-side, and validated on the way back.
	state, err := auth.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// PKCE (RFC 7636) rides alongside: the verifier never leaves the server, so
	// a code intercepted in the browser's redirect cannot be redeemed elsewhere.
	// Only providers that opt in get a challenge — see Provider.PKCE.
	var verifier, challenge string
	if provider.PKCE() {
		verifier, challenge, err = oauth.NewPKCE()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.store.PutOAuthState(state, platform, verifier); err != nil {
		writeStoreError(w, err)
		return
	}

	http.Redirect(w, r, provider.AuthURL(creds.ClientID, s.redirectURI(r, platform), state, challenge), http.StatusFound)
}

// handleOAuthCallback completes the flow and stores the connected account.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	q := r.URL.Query()

	if e := q.Get("error"); e != "" {
		s.oauthDone(w, r, "", fmt.Sprintf("%s: %s", e, q.Get("error_description")))
		return
	}

	// Validate state BEFORE touching the code: this is what stops an attacker
	// grafting their own account onto the admin's session.
	statePlatform, verifier, err := s.store.TakeOAuthState(q.Get("state"))
	if err != nil {
		s.oauthDone(w, r, "", err.Error())
		return
	}
	if statePlatform != platform {
		s.oauthDone(w, r, "", "OAuth state does not match the platform being connected")
		return
	}

	provider, err := s.providers.Get(platform)
	if err != nil {
		s.oauthDone(w, r, "", err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		s.oauthDone(w, r, "", "developer credentials for this platform have gone missing")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tok, err := provider.Exchange(ctx, creds.ClientID, creds.ClientSecret, s.redirectURI(r, platform), q.Get("code"), verifier)
	if err != nil {
		s.oauthDone(w, r, "", "token exchange failed: "+err.Error())
		return
	}
	acct, err := provider.Account(ctx, creds.ClientID, tok.AccessToken)
	if err != nil {
		s.oauthDone(w, r, "", "could not read the account: "+err.Error())
		return
	}

	saved, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform:     platform,
		AccountName:  acct.Name,
		AccountRef:   acct.Ref,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
		Scopes:       tok.Scopes,
		// Stamped at CONNECT time, which is the only moment the granted scopes
		// and the requested scopes are known to agree. Comparing later would
		// mean trusting whatever the platform echoed back, which is exactly
		// what ScopeVersion exists to avoid.
		ScopeVer: provider.ScopeVersion(),
	})
	if err != nil {
		s.oauthDone(w, r, "", err.Error())
		return
	}

	s.log.Info("platform account connected", "platform", platform, "account", acct.Name)
	s.oauthWarn(w, r, fmt.Sprintf("Connected %s as %s", platform, saved.AccountName),
		s.entitlementWarning(ctx, platform, creds.ClientID, tok.AccessToken))
}

// entitlementWarning asks a commercially gated platform, at the earliest moment
// there is a token to ask with, whether this account reaches the gated API.
//
// IT RUNS AFTER THE ACCOUNT IS STORED AND NEVER FAILS THE CONNECTION. The
// account connected -- that is true whatever the gate says, and unwinding a
// successful sign-in because a probe came back unhappy would turn "your plan
// does not include this" into "connecting is broken", which is a worse lie than
// the silence it replaces.
//
// WHY HERE AND NOT AT GO-LIVE. This is the whole point of the mechanism: the
// alternative is a refusal arriving mid-broadcast from an API that never names
// the reason. Vimeo's live API is Enterprise-only and says so nowhere in its
// error responses, so an operator with correct credentials, granted scopes and
// a connected account has no route from what polyemesis shows them to the
// actual explanation. One extra GET, once, at connect.
//
// Returns "" for every platform without a gate, which is all of them but one.
func (s *Server) entitlementWarning(ctx context.Context, platform db.Platform, clientID, accessToken string) string {
	gated, ok := s.providers.EntitlementFor(platform)
	if !ok {
		return ""
	}
	err := gated.CheckEntitlement(ctx, clientID, accessToken)
	if err == nil {
		return ""
	}
	if errors.Is(err, oauth.ErrNotEntitled) {
		return err.Error()
	}
	// The probe did not complete. Reporting the gate on this evidence would be
	// a claim about somebody's contract made on the strength of a timeout --
	// the same defect credcheck.go describes for CheckUnreachable. Saying
	// nothing is the other half of it: a silent success reads as a clean bill
	// of health, and the operator would learn about the gate mid-broadcast
	// after all.
	return fmt.Sprintf("polyemesis could not check whether this account reaches %s's gated API (%v). %s",
		platform, err, gated.EntitlementReason())
}

// oauthDone returns the browser to the SPA with the outcome in the query
// string. The OAuth round trip is a full navigation, so there is no XHR to
// return JSON to.
func (s *Server) oauthDone(w http.ResponseWriter, r *http.Request, ok, errMsg string) {
	target := "/settings?tab=platforms"
	if errMsg != "" {
		target += "&oauth_error=" + urlEscape(errMsg)
	} else {
		target += "&oauth_ok=" + urlEscape(ok)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// oauthWarn is oauthDone with a third outcome: connected, AND there is
// something the operator has to know before they rely on it.
//
// A THIRD OUTCOME RATHER THAN A LONGER SUCCESS MESSAGE. Appending the gate to
// the ok string would render it as a green tick with a paragraph after it,
// which is the shape of message people stop reading. It is also not an error --
// the connection worked, there is nothing to retry, and colouring it red would
// send an operator back round a flow that already succeeded.
//
// An empty warn is the common case and is byte-for-byte the old behaviour, so
// every platform without a gate redirects exactly as it did.
func (s *Server) oauthWarn(w http.ResponseWriter, r *http.Request, ok, warn string) {
	if warn == "" {
		s.oauthDone(w, r, ok, "")
		return
	}
	http.Redirect(w, r, "/settings?tab=platforms&oauth_ok="+urlEscape(ok)+
		"&oauth_warn="+urlEscape(warn), http.StatusFound)
}

func urlEscape(s string) string {
	return strings.NewReplacer(
		"%", "%25", " ", "%20", "&", "%26", "#", "%23",
		// A SEMICOLON IS A SEPARATOR TO GO'S OWN PARSER, and this was found by
		// a message that happened to contain one. net/url.Values.Get silently
		// DROPS any pair whose key or value carries a `;` -- ParseQuery records
		// an error for it and u.Query() discards that error -- so an operator
		// message with a semicolon in it does not arrive truncated, it does not
		// arrive at all. Browsers stopped treating `;` as a separator years
		// ago, which is exactly why this went unnoticed: the SPA reads it fine
		// and anything Go-side reading the same URL sees nothing.
		//
		// Every string that comes through here is written for a human -- a
		// platform's refusal, a gate explanation -- and semicolons are ordinary
		// in those.
		";", "%3B",
		"?", "%3F", "+", "%2B", "\n", " ", "\r", " ",
	).Replace(s)
}

// The LiveStatter interface moved to internal/oauth (stats.go). It was declared
// here because the API layer was the only consumer and Kick the only provider;
// both stopped being true, and a capability interface outside the oauth package
// cannot carry the Set twin endpoints.go requires.

// handleAccountStats reads the live viewer count for one connected account.
//
// A platform without the capability answers 200 with supported:false rather
// than 404: "we cannot ask" and "the account is gone" are different problems
// with different fixes, and a client that cannot tell them apart shows the wrong
// one. Being offline is likewise a normal answer, not an error.
func (s *Server) handleAccountStats(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	acct, err := s.tokenFor(ctx, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if _, err := s.providers.Get(acct.Platform); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "reason": err.Error()})
		return
	}
	// Through the Set twin rather than a type assertion on a provider this
	// handler resolved itself: a Server built with a stubbed Set must not fall
	// through to a production provider for viewer numbers alone.
	st, ok := s.providers.StatsFor(acct.Platform)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": false,
			"reason":    fmt.Sprintf("polyemesis does not read a viewer count from %s", acct.Platform),
		})
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, "developer credentials are missing for "+string(acct.Platform))
		return
	}

	stats, err := st.Stats(ctx, creds.ClientID, acct.AccessToken)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "stats": stats})
}

// handleRefreshKey re-fetches a destination's ingest URL and stream key from
// the platform, refreshing the OAuth token first if it has expired.
//
// This is what keeps a rotated stream key from silently breaking a stream: the
// user clicks one button instead of hunting through a creator dashboard.
func (s *Server) handleRefreshKey(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	dest, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if dest.AccountID == nil {
		writeError(w, http.StatusBadRequest,
			"this destination is not linked to a connected account; connect one or enter the key manually")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	acct, err := s.tokenFor(ctx, *dest.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	provider, err := s.providers.Get(acct.Platform)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, "developer credentials are missing for "+string(acct.Platform))
		return
	}

	b, err := s.ingestFor(ctx, provider, creds.ClientID, acct, s.ingestOptionsForRefresh(dest))
	if err != nil {
		// A platform that publishes no key endpoint is not a transport
		// failure, and 502 invites a retry that can never succeed. The
		// operator needs the paste field, not the button again.
		if errors.Is(err, oauth.ErrNoStreamKeyAPI) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Facebook's key IS the broadcast, so this is where a running chat adapter
	// finds out which live video to read comments from.
	if b.ID != "" {
		s.setFacebookBroadcast(acct.AccountRef, b.ID)
	}

	// A PLATFORM MAY SUPPLY THE KEY AND NOT THE URL, and overwriting here would
	// destroy the half the operator supplied.
	//
	// This was an unconditional assignment, which was right while every
	// provider returned both fields. Trovo returns only the key: it publishes
	// the stream key on its channel resource and publishes the ingest hostname
	// nowhere at all -- the host is regional and lives only in the creator
	// dashboard, so the operator copies it across once. Blanking it on every
	// key refresh would take a working destination and make it unsavable,
	// reported as "an RTMP URL is required" from a button labelled Refresh
	// stream key.
	//
	// Behaviour for every other platform is unchanged: they always answer with
	// a URL, so the branch is never taken for them.
	if b.Ingest.URL != "" {
		dest.URL = b.Ingest.URL
	} else if strings.TrimSpace(dest.URL) == "" {
		writeError(w, http.StatusBadRequest,
			string(acct.Platform)+" supplies the stream key but does not publish an ingest URL, and this "+
				"destination has none yet. Copy the server URL from the platform's own dashboard into "+
				"this destination first, then fetch the key — refreshing it afterwards leaves the URL alone.")
		return
	}
	dest.StreamKey = b.Ingest.Key
	dest.Kind = db.DestRTMP
	// Recorded even when empty: a destination that used to have a backup
	// endpoint and no longer does must stop publishing to the old one, which
	// belongs to a broadcast that no longer exists.
	dest.BackupURL, dest.BackupStreamKey = firstBackup(b)
	var warnings []string
	if dest.BackupIngestWanted && dest.BackupURL == "" {
		warnings = append(warnings,
			"Facebook did not offer a backup ingest endpoint for this broadcast, so "+
				"no redundant feed will be published. The destination is otherwise "+
				"configured correctly and will go live normally.")
	}
	updated, err := s.store.UpdateDestination(dest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rw := s.reconcileNow("the refreshed stream key")
	resp := map[string]any{"destination": updated}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeMutation(w, http.StatusOK, rw, resp)
}

// firstBackup is the secondary ingest a destination will publish to, or empty.
//
// Facebook returns a LIST -- one secondary per primary form, rtmp and rtmps --
// and polyemesis publishes one redundant feed, not N. Taking the first keeps
// the choice in one place rather than at every call site; the parsing in
// internal/oauth already puts the secure form first.
func firstBackup(b *oauth.Broadcast) (url, key string) {
	if b == nil || len(b.Backups) == 0 {
		return "", ""
	}
	return b.Backups[0].URL, b.Backups[0].Key
}

// ingestOptionsFor maps a stored destination onto what a broadcast create
// needs, so refresh-key sends the privacy, crossposting and donate-button
// choices the operator already saved.
//
// Pulled out to its own function because a live Facebook create call is
// expensive to fake in a test and this mapping is not: constructing a
// db.Destination and asserting on the three fields it produces is what
// actually proves a destination stored with FBPrivacyEveryone, say, never
// reads back as FBPrivacySelf.
// scheduledFor is a PARAMETER rather than a field read off the destination,
// because the occurrence belongs to the schedule and not to the destination.
// FacebookSettings.ScheduledFor records what has already been announced;
// passing that back in would re-announce the same occurrence forever.
//
// Zero is live now, which is what every go-live passes.
func ingestOptionsFor(dest *db.Destination, scheduledFor time.Time) oauth.IngestOptions {
	return oauth.IngestOptions{
		Privacy:         dest.Compliance.FacebookPrivacy,
		Crosspost:       dest.Facebook.Crosspost,
		DonateCharityID: dest.Facebook.DonateCharityID,
		ScheduledFor:    scheduledFor,
		// The intent is read off the destination; the option it fills is
		// Facebook's own enable_backup_ingest, which is a platform fact and
		// stays named for the platform. Only the READ moved.
		BackupIngest: dest.BackupIngestWanted,
		// The key this destination is already publishing with, so a provider
		// that can re-find the stream behind it hands the SAME one back. A
		// refresh must not be a rotation: somebody has this pasted into OBS.
		// Empty for a destination that has never fetched one, which is the
		// only case where a new key is the right answer.
		HeldKey: dest.StreamKey,
		// The operator's own name for this destination, which is the only
		// string here that will mean anything to them in a platform's studio.
		// Never a key -- see oauth.IngestOptions.IngestLabel.
		IngestLabel: dest.Name,
	}
}

// ingestOptions is ingestOptionsFor plus the one create-time choice that CANNOT
// be read off a single destination: whether this one needs an ingest stream of
// its own.
//
// IT IS A METHOD BECAUSE THE ANSWER IS ABOUT THE OTHER DESTINATIONS. A provider
// is stateless about destinations and ingestOptionsFor sees exactly one row, so
// neither of them can answer "is somebody else already using the account's
// shared stream". This layer owns the table, so this layer decides; everything
// below it just carries the flag.
//
// Both callers go through this rather than through ingestOptionsFor directly.
// A go-live that skipped it would hand the shared stream to every destination
// again, which is the defect, and it would do so silently.
func (s *Server) ingestOptions(dest *db.Destination, scheduledFor time.Time) oauth.IngestOptions {
	opts := ingestOptionsFor(dest, scheduledFor)
	opts.DedicatedIngest = s.needsOwnIngestStream(dest)
	return opts
}

// ingestOptionsForRefresh is ingestOptions for the one caller that may change a
// destination's key: handleRefreshKey, driven by an operator pressing a button
// named Refresh stream key.
//
// Separate from ingestOptions rather than a bool parameter, because the
// distinction is about AUTHORITY and a bool at a call site does not read as
// one. preannounce.go sweeps every five minutes and must never move a key an
// encoder is publishing with; this path exists because somebody asked. Anything
// new that rotates keys should have to come here and say so.
func (s *Server) ingestOptionsForRefresh(dest *db.Destination) oauth.IngestOptions {
	opts := s.ingestOptions(dest, time.Time{})
	// ROTATE ONLY WHAT IS ACTUALLY SHARED. Being asked is necessary and not
	// sufficient: a destination that already has a stream of its own gains
	// nothing from a new one, and every needless rotation leaves an orphaned
	// liveStream on the operator's channel that nothing cleans up.
	//
	// Setting this unconditionally was measured doing exactly that -- the
	// end-to-end refresh test caught a second destination being moved off its
	// own perfectly good stream onto a freshly created one.
	opts.RotateKey = s.keyIsSharedWithASibling(dest)
	return opts
}

// keyIsSharedWithASibling reports whether another destination on the same
// account publishes with this destination's key.
//
// That is the whole condition under which a key may be moved: it is what
// distinguishes an upgraded install, where every YouTube destination holds the
// same key, from one already provisioned correctly. A destination with a key
// nobody else uses is already its own ingestion source and must be left alone.
func (s *Server) keyIsSharedWithASibling(dest *db.Destination) bool {
	if dest == nil || dest.AccountID == nil || strings.TrimSpace(dest.StreamKey) == "" {
		return false
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		// Unknown is treated as NOT shared, so a store failure never rotates a
		// key. The cost of guessing wrong that way is a ceiling that stays
		// where it is; the cost of guessing the other way is a live encoder
		// publishing to a stream nothing is watching.
		return false
	}
	for _, other := range rows {
		if other.ID == dest.ID || other.AccountID == nil {
			continue
		}
		if *other.AccountID == *dest.AccountID && other.StreamKey == dest.StreamKey {
			return true
		}
	}
	return false
}

// needsOwnIngestStream answers whether this destination should be given an
// ingest stream of its own rather than the one its account already shares.
//
// WHY THE QUESTION EXISTS: YouTube counts concurrent broadcasts per stream key
// as well as per channel, and the per-key ceiling is the smaller one. Every
// YouTube destination in an install has been handed the same key, so they all
// count as one ingestion source and the fourth show to start is refused with
// sharedIngestionBroadcastsExceedLimit -- polyemesis's own doing. Neither
// number is published by YouTube, and neither is written down here or anywhere
// else in the code: nothing counts, nothing caps, nothing pre-flights. This
// decides ONE thing -- which destination keeps the shared stream -- and the
// platform stays the only party that says no.
//
// THE ANSWER IS KEYED ON THE DESTINATION ID, LOWEST WINS. The first
// destination on an account keeps today's behaviour exactly: it reuses whatever
// reusable stream the channel already has, because that is the key an
// operator's Studio-scheduled events are bound to and changing it would break a
// working setup for a feature they never asked for. Every later one gets its
// own.
//
// A ROW ID RATHER THAN A COUNT, A TIMESTAMP OR A FLAG, and each rejected
// alternative is a hazard:
//
//   - A COUNT RACES. "Am I the first?" answered by counting siblings is decided
//     at refresh time, so two destinations created in the same minute and
//     refreshed together can both answer yes and both take the shared stream --
//     the exact defect, reintroduced under the fix. Ids are assigned by the
//     database at insert, are distinct, and are already decided before either
//     refresh starts, so the comparison gives the same answer no matter who
//     asks first or how many ask at once.
//   - A TIMESTAMP TIES. Two rows created in the same second sort equally.
//   - A STORED "this one is the anchor" FLAG needs a migration, a writer, and
//     an answer for what happens when the row holding it is deleted.
//
// WHAT DELETING THE FIRST DESTINATION DOES, said plainly because it is the case
// this rule does not settle on its own: the lowest surviving id becomes lowest,
// so this function starts answering false for a destination it used to answer
// true for. That does NOT rotate its key, and the reason is one layer down --
// IngestOptions.HeldKey is matched before DedicatedIngest is consulted, so a
// destination that already holds a stream keeps it whatever this returns. The
// promotion is also harmless on its own terms: the shared stream can only be
// claimed by the lowest id on the account, so a destination is only ever
// promoted onto it once the destination that was holding it is gone. The one
// case that does re-point is a promoted destination whose OWN stream YouTube no
// longer lists -- deleted in Studio -- and there the key in the encoder was
// already dead.
//
// A destination with no account is not asked about: it has no shared stream to
// contend for, and its key is typed by hand.
func (s *Server) needsOwnIngestStream(dest *db.Destination) bool {
	if dest == nil || dest.AccountID == nil {
		return false
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		// Today's behaviour is the safe fallback: sharing the account's stream
		// is a ceiling an operator can hit, and provisioning a stream for a
		// destination that did not need one hands a single-destination operator
		// a key their scheduled events are not bound to. The first is a refusal
		// with a message; the second is a broken setup.
		s.log.Warn("could not read the other destinations on this account, so this one "+
			"falls back to the account's shared ingest stream",
			"destination", dest.ID, "err", err)
		return false
	}
	for _, other := range rows {
		if other == nil || other.ID == dest.ID || other.AccountID == nil {
			continue
		}
		if *other.AccountID == *dest.AccountID && other.ID < dest.ID {
			return true
		}
	}
	return false
}

// ------------------------------------------------- the streams left behind
//
// WHY DELETING A DESTINATION DOES NOT DELETE ITS YouTube liveStream, AND WHY
// THAT IS THE ANSWER RATHER THAN A TODO.
//
// The leak is real and is stated plainly so nobody has to rediscover it: every
// destination beyond the first on an account gets a liveStream of its own
// (needsOwnIngestStream above), and deleting the destination leaves that stream
// on the channel, unused, one per deletion, for ever.
//
// A cleanup was designed against the YouTube documentation and NOT BUILT. Three
// things have to be true before a delete is safe, and polyemesis can prove none
// of them:
//
//  1. THAT THE STREAM IS POLYEMESIS'S TO DELETE. Nothing records that. The
//     destination row stores the KEY, not the stream's id, and no column
//     anywhere says "polyemesis created this". The only mark on the object is
//     its title, and oauth.ytStreamTitle documents that title as display
//     metadata for a human in Studio -- "never an identifier -- nothing matches
//     on it, so a rename in either place breaks nothing". Matching on it would
//     make a rename in Studio into a silent failure in one direction, and would
//     delete a creator's own stream that happens to be named after us in the
//     other. "A stream polyemesis did not create is not polyemesis's to delete"
//     is not a preference here; it is a fact this process cannot establish.
//
//  2. THAT IT IS NOT THE CHANNEL'S SHARED STREAM. The shared one is chosen
//     positionally -- the first reusable RTMP stream the channel lists -- so it
//     is not identifiable after the fact either, and an operator's
//     Studio-scheduled events are bound to it. Deleting it breaks things
//     polyemesis did not create.
//
//  3. THAT NO BROADCAST IS BOUND TO IT. YouTube DOES refuse this, and the
//     refusal is documented: liveStreams.delete answers 403
//     liveStreamDeletionNotAllowed, "The specified live stream cannot be deleted
//     because it is bound to a broadcast that has still not completed"
//     (developers.google.com/youtube/v3/live/docs/liveStreams/delete, read
//     2026-08-16). But whether a broadcast that is LIVE RIGHT NOW is inside that
//     condition is NOT documented: it requires equating the refusal's English
//     "completed" with the lifeCycleStatus enum value `complete`, which no page
//     states, and no page states that `live` is therefore covered. That is an
//     unresolved inference, and the cost of it being wrong is a show going off
//     air, so it is not something to lean on. The alternative -- proving the
//     stream unbound ourselves -- needs a whole-channel liveBroadcasts.list scan
//     carrying broadcastType=all (the default is `event`, which silently omits
//     every persistent broadcast) whose per-call quota cost YouTube does not
//     publish. internal/api/lifecycle.go is already rationing a shared
//     10,000-a-day allocation down to 288 calls per destination per day; adding
//     an unbounded paginated scan to a delete path is the wrong direction.
//
// And the thing being cleaned up is CLUTTER. An unused liveStream costs the
// operator nothing but a longer list in Studio, so every one of those
// ambiguities resolves the same way: leave it alone.
//
// WHAT WOULD SETTLE IT, in the order it would have to be settled:
//
//   - Record the created stream's id and the fact that polyemesis created it, at
//     the moment oauth.YouTube.createStream returns. That is a schema change and
//     a migration, and it fixes (1) and (2) outright for every destination
//     provisioned afterwards -- and for no destination provisioned before, which
//     is why it is a change with a story rather than a patch.
//   - One delete attempt against a stream bound to a `live` broadcast, recording
//     the literal error.errors[].reason, to settle whether the server refuses.
//   - The same against a `revoked` broadcast and against a pre-2020 channel's
//     default stream, which the liveStreams resource page documents as
//     undeletable in prose with no error code named anywhere.
//
// Until then this file does the one honest thing available: it NAMES what is
// being left behind, so the operator can remove it in Studio if the clutter ever
// bothers them.

// noteOrphanedIngestStream tells the operator that deleting this destination
// probably leaves a YouTube liveStream on their channel.
//
// IT COSTS NO API CALL AND MAKES NO CLAIM IT CANNOT SUPPORT. Both questions are
// answered from the destination table alone, and the answer is "probably", which
// is what the wording says:
//
//   - a sibling on the same account publishing with the same key means the
//     stream is still in use, so nothing is orphaned;
//   - a destination that was NOT given a stream of its own is holding the
//     channel's shared one, which must never be touched by anybody.
//
// Only what survives both is worth mentioning, and even then only as something
// the operator may want to look at -- polyemesis cannot prove it created the
// stream, which is the whole reason nothing is deleted here. See the block
// above.
//
// A LOG LINE RATHER THAN A RESPONSE FIELD, for the reason forgetPlatformSwitch
// gives for the same choice: it is the only place a statement this hedged can go
// without becoming an API somebody depends on.
//
// THE KEY NEVER APPEARS. The stream is named by its TITLE, which is what an
// operator actually reads in Studio and is derived from the destination's own
// name, and titles are the one string about a stream that is safe to log.
func (s *Server) noteOrphanedIngestStream(dest *db.Destination) {
	if !s.leavesAnOrphanedIngestStream(dest) {
		return
	}
	s.log.Warn("deleting this destination leaves its YouTube ingest stream on the channel. "+
		"polyemesis does not delete it, because it cannot prove the stream is one it created "+
		"and not one of yours, and a stream that is still bound to a broadcast must not be "+
		"removed. Delete it in YouTube Studio if you want it gone",
		"destination", dest.Name, "streamTitle", oauth.YouTubeStreamTitle(dest.Name))
}

// leavesAnOrphanedIngestStream is the decision behind that notice, split out so
// it can be asserted without reading log output.
//
// IT IS DELIBERATELY THE NARROWER OF THE TWO ERRORS. Saying nothing about a
// stream that was in fact orphaned costs an operator one extra line in Studio;
// saying "this is yours to delete" about the channel's shared stream, or about a
// stream a sibling is publishing to, would point somebody at an object whose
// removal breaks a working setup. So both of those answer false, and so does
// every case where the row cannot support the claim at all.
func (s *Server) leavesAnOrphanedIngestStream(dest *db.Destination) bool {
	if dest == nil || dest.Platform != db.PlatformYouTube || dest.AccountID == nil {
		// No account means the key was typed in by hand: polyemesis provisioned
		// nothing and has nothing to say about it.
		return false
	}
	if strings.TrimSpace(dest.StreamKey) == "" {
		// Never fetched one, so nothing was ever created for it.
		return false
	}
	// A sibling publishing with the same key means the stream stays in use --
	// this is what an upgraded install looks like, where every YouTube
	// destination was handed the account's one shared key.
	if s.keyIsSharedWithASibling(dest) {
		return false
	}
	// And a destination that was never given a stream of its own is holding the
	// channel's shared one, which an operator's Studio-scheduled events are bound
	// to. Never name that as removable.
	return s.needsOwnIngestStream(dest)
}

// ingestFor fetches an ingest, preferring the connected target over the login's
// default profile.
//
// Provider.Ingest always targets whatever the token's own identity is, which for
// Facebook means a Page connection would silently stream to the operator's
// personal profile. TargetsFor is the capability that knows better; it also
// returns the broadcast id, which is the handle the chat adapter needs and which
// Provider.Ingest discards. Every other platform has no targets and falls
// through to Ingest unchanged.
//
// Returns the whole Broadcast rather than the pieces, because the pieces kept
// growing: first the ingest, then the broadcast id, and now the BACKUP ingest,
// which was being parsed by internal/oauth and discarded right here. A platform
// with no broadcast object gets a synthetic one so every caller reads one shape.
func (s *Server) ingestFor(ctx context.Context, provider oauth.Provider, clientID string, acct *db.PlatformAccount, opts oauth.IngestOptions) (*oauth.Broadcast, error) {
	if tp, ok := s.providers.TargetsFor(acct.Platform); ok {
		return tp.IngestFor(ctx, clientID, acct.AccessToken, acct.AccountRef, opts)
	}
	ing, err := provider.Ingest(ctx, clientID, acct.AccessToken)
	if err != nil {
		return nil, err
	}
	return &oauth.Broadcast{Ingest: *ing}, nil
}

// refreshLocks serializes concurrent token refreshes of the SAME account. The
// 10-minute RefreshLoop tick and an on-demand tokenFor call (or two on-demand
// calls from two in-flight publishes) can both see the same account as
// expired and both call the platform's refresh endpoint; whichever write
// landed second overwrote a live token with one the platform may already have
// invalidated -- a refresh failure mid-broadcast. #6.
//
// THE LOCK IS NOT THE WHOLE DEVICE AND CANNOT BE, because it only reaches
// writers that come through here. The connect callback and the device flow
// write the same row and hold nothing: they key by (platform, account_ref) and,
// when they are creating the row, have no id to lock on. That interleaving is
// handled one layer down, by db.UpdatePlatformAccountTokens' compare-and-swap;
// read the header of internal/db/platform_account_tokens.go for what it costs
// when it is missing.
var refreshLocks = newKeyedMutex()

// keyedMutex hands out one *sync.Mutex per key, reference-counted so a key with
// no waiters is removed rather than accumulating forever (accounts get
// deleted; their lock entries must not outlive them). The refcount increment
// happens under the same map lock as lookup/creation, so a key can never be
// deleted while a waiter still holds a reference to it -- the classic race in
// a naive "delete on unlock" keyed mutex.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[int64]*refcountedMutex
}

type refcountedMutex struct {
	mu  sync.Mutex
	ref int
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[int64]*refcountedMutex)}
}

// Lock blocks until key is exclusively held and returns the func that releases
// it. Different keys never block each other.
func (k *keyedMutex) Lock(key int64) func() {
	k.mu.Lock()
	l, ok := k.locks[key]
	if !ok {
		l = &refcountedMutex{}
		k.locks[key] = l
	}
	l.ref++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.ref--
		if l.ref == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// tokenFor loads an account and refreshes its access token if it is expired or
// close to it. Refresh-on-use plus the background loop in RefreshLoop means a
// long broadcast never hits an expired token mid-stream.
func (s *Server) tokenFor(ctx context.Context, accountID int64) (*db.PlatformAccount, error) {
	acct, err := s.store.GetPlatformAccount(s.box, accountID)
	if err != nil {
		return nil, err
	}
	if !acct.Expired() {
		return acct, nil
	}

	// Serialize refreshes of this one account; other accounts proceed in
	// parallel. Re-read after acquiring the lock: the racing caller may have
	// already refreshed while we waited, in which case we reuse its result
	// instead of hitting the platform -- and overwriting it -- a second time.
	unlock := refreshLocks.Lock(accountID)
	defer unlock()
	acct, err = s.store.GetPlatformAccount(s.box, accountID)
	if err != nil {
		return nil, err
	}
	if !acct.Expired() {
		return acct, nil
	}
	// The row version this refresh is entitled to overwrite, taken HERE --
	// after the re-read and before the network call below, which is the whole
	// span a reconnect can slip into. Taking it any later would witness a row
	// this function never actually reasoned about.
	seen := acct.Revision()
	if acct.RefreshToken == "" {
		return nil, fmt.Errorf("the %s token has expired and there is no refresh token; reconnect the account",
			acct.Platform)
	}

	provider, err := s.providers.Get(acct.Platform)
	if err != nil {
		return nil, err
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		return nil, fmt.Errorf("developer credentials for %s are missing", acct.Platform)
	}

	tok, err := provider.Refresh(ctx, creds.ClientID, creds.ClientSecret, acct.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("could not refresh the %s token: %w", acct.Platform, err)
	}

	// THROUGH THE NARROW WRITER, NOT UpsertPlatformAccount. The struct in hand
	// was read before the platform call and its Scopes, ScopeVer and
	// AccountName are consent facts a refresh has no standing to restate; the
	// upsert would write all three back, and if consent landed in the meantime
	// it would write them back WRONG. UpdatePlatformAccountTokens cannot express
	// that write at all -- the columns are not in its statement.
	//
	// An empty tok.RefreshToken is passed straight through and means "keep the
	// stored one", which is the same behaviour as before and is now decided by
	// the statement rather than by this function remembering to check.
	updated, err := s.store.UpdatePlatformAccountTokens(s.box, acct.ID, seen,
		tok.AccessToken, tok.RefreshToken, tok.ExpiresAt)
	if errors.Is(err, db.ErrAccountRewritten) {
		return s.yieldToTheRowThatWon(acct)
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// yieldToTheRowThatWon is what a refresh does when its compare-and-swap finds
// somebody else has written the account underneath it.
//
// THE LOSER DOES NOT RETRY AND DOES NOT WRITE, and that is the decision worth
// stating rather than the mechanism. A refresh that reaches here has been
// beaten by the only writers that can beat it: a completed consent, from the
// connect callback or the device flow. Those writers know strictly more than
// this one does -- they carry the scopes and the scope version the operator has
// just granted, and a token minted against that grant -- so writing over them
// is precisely the defect the swap exists to stop. Retrying is the same defect
// with an extra platform call in front of it.
//
// Nor is it an error to the caller. The point of tokenFor is to hand back a
// usable token, and the winner's row holds one; the refresh this function is
// abandoning was work that turned out not to be needed. It is logged at info
// rather than warn for that reason.
//
// The one case that IS reported as a failure is a winning row that is still
// expired. That is not a consent landing -- consent always produces a live
// token -- so this process cannot say what wrote it, and handing back an
// expired token as though it were fresh would move the failure to whichever
// platform call used it next, with nothing to connect the two.
func (s *Server) yieldToTheRowThatWon(stale *db.PlatformAccount) (*db.PlatformAccount, error) {
	winner, err := s.store.GetPlatformAccount(s.box, stale.ID)
	if err != nil {
		return nil, err
	}
	if winner.Expired() {
		return nil, fmt.Errorf("the %s account was rewritten while its token was being refreshed, and "+
			"the token now stored is expired too; try again", stale.Platform)
	}
	s.log.Info("a token refresh was overtaken by a reconnect and kept the stored token",
		"platform", stale.Platform, "account", stale.AccountName)
	return winner, nil
}

// RefreshLoop proactively renews tokens that are close to expiring, so a live
// stream never depends on a token refresh succeeding at the worst moment.
func (s *Server) RefreshLoop(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			accts, err := s.store.ListPlatformAccounts()
			if err != nil {
				continue
			}
			for _, a := range accts {
				if a.ExpiresAt.IsZero() || time.Until(a.ExpiresAt) > 30*time.Minute {
					continue
				}
				rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := s.tokenFor(rctx, a.ID); err != nil {
					s.log.Warn("token refresh failed", "platform", a.Platform, "account", a.AccountName, "err", err)
				} else {
					s.log.Info("token refreshed", "platform", a.Platform, "account", a.AccountName)
				}
				cancel()
			}
		}
	}
}

// fillFacebookBackupIngest provisions a redundant ingest on the broadcast a
// Facebook destination is ALREADY publishing to, when the operator has just
// asked for one and there is none. #727.
//
// Returns the warning to attach to the response, empty when there was nothing
// to do or when it worked.
//
// THE FOUR CONDITIONS ARE THE DEVICE. Each one is a reason not to spend a
// platform call, and getting any of them wrong turns an ordinary destination
// edit into an unexpected write against somebody's live broadcast:
//
//   - the operator wants a backup, and does not have one already;
//   - the destination is Facebook, because this is Facebook's endpoint;
//   - it is linked to a connected account, because a hand-pasted key carries no
//     live-video id and nothing can be added to a broadcast we cannot name;
//   - its key carries a live-video id, which is what says the broadcast exists
//     and is ours to modify.
//
// A destination failing any of them is left exactly alone.
func (s *Server) fillFacebookBackupIngest(ctx context.Context, dest *db.Destination) string {
	if dest == nil || !dest.BackupIngestWanted || dest.BackupURL != "" {
		return ""
	}
	if dest.Platform != db.PlatformFacebook || dest.AccountID == nil {
		return ""
	}
	liveVideoID := oauth.FacebookLiveVideoID(dest.StreamKey)
	if liveVideoID == "" {
		// A pasted key. There is a broadcast, but this process cannot name it
		// from the key, and #725's adoption is deliberately read-only -- adding
		// an ingest to a broadcast we merely inferred is a write against
		// something that may not be ours.
		return "This destination's stream key was pasted rather than fetched, so polyemesis " +
			"cannot tell Facebook which broadcast to add a redundant feed to. Turn on " +
			"Backup stream in Live Producer and paste the second key instead."
	}

	p, err := s.providers.Get(db.PlatformFacebook)
	if err != nil {
		return ""
	}
	fb, ok := p.(*oauth.Facebook)
	if !ok || fb == nil {
		return ""
	}
	acct, err := s.tokenFor(ctx, *dest.AccountID)
	if err != nil {
		return "The connected Facebook account could not be used to add a redundant feed: " +
			err.Error() + ". The destination is otherwise saved and will go live normally."
	}

	ing, err := fb.AddBackupIngest(ctx, acct.AccessToken, acct.AccountRef, liveVideoID)
	if err != nil {
		s.log.Warn("could not add a backup ingest to an existing Facebook broadcast",
			"destination", dest.ID, "liveVideo", liveVideoID, "err", err)
		return "Facebook would not add a redundant ingest to this broadcast: " + err.Error() +
			". The destination is otherwise saved and will go live on its primary feed."
	}
	dest.BackupURL, dest.BackupStreamKey = ing.URL, ing.Key
	s.log.Info("added a backup ingest to an existing Facebook broadcast",
		"destination", dest.ID, "liveVideo", liveVideoID)
	return ""
}
