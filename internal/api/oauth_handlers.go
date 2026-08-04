package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	if _, err := oauth.Get(platform); err != nil {
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
	check := oauth.CheckCredentialsFor(r.Context(), platform, req.ClientID, req.ClientSecret)
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
	if _, err := oauth.Get(platform); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		oauth.CheckCredentialsFor(r.Context(), platform, creds.ClientID, creds.ClientSecret))
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

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeletePlatformAccount(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

// handleOAuthStart redirects the browser to the platform's consent screen.
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	provider, err := oauth.Get(platform)
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

	provider, err := oauth.Get(platform)
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
	s.oauthDone(w, r, fmt.Sprintf("Connected %s as %s", platform, saved.AccountName), "")
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

func urlEscape(s string) string {
	return strings.NewReplacer(
		"%", "%25", " ", "%20", "&", "%26", "#", "%23",
		"?", "%3F", "+", "%2B", "\n", " ", "\r", " ",
	).Replace(s)
}

// LiveStatter is the optional capability for a platform that will tell us how
// many people are watching. Declared here rather than in internal/oauth because
// it is the API layer that needs to discover it; the provider just has the
// method. Kick is the only one today.
type LiveStatter interface {
	Stats(ctx context.Context, clientID, accessToken string) (*oauth.KickStats, error)
}

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
	provider, err := oauth.Get(acct.Platform)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "reason": err.Error()})
		return
	}
	st, ok := provider.(LiveStatter)
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
	provider, err := oauth.Get(acct.Platform)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, acct.Platform)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, "developer credentials are missing for "+string(acct.Platform))
		return
	}

	ing, broadcastID, err := s.ingestForFn(ctx, provider, creds.ClientID, acct, ingestOptionsFor(dest))
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
	if broadcastID != "" {
		s.setFacebookBroadcast(acct.AccountRef, broadcastID)
	}

	dest.URL = ing.URL
	dest.StreamKey = ing.Key
	dest.Kind = db.DestRTMP
	updated, err := s.store.UpdateDestination(dest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng().Reconcile(); err != nil {
		s.log.Warn("reconcile after key refresh", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"destination": updated})
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
func ingestOptionsFor(dest *db.Destination) oauth.IngestOptions {
	return oauth.IngestOptions{
		Privacy:         dest.Compliance.FacebookPrivacy,
		Crosspost:       dest.Facebook.Crosspost,
		DonateCharityID: dest.Facebook.DonateCharityID,
	}
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
func (s *Server) ingestFor(ctx context.Context, provider oauth.Provider, clientID string, acct *db.PlatformAccount, opts oauth.IngestOptions) (*oauth.Ingest, string, error) {
	if tp, ok := oauth.TargetsFor(acct.Platform); ok {
		b, err := tp.IngestFor(ctx, clientID, acct.AccessToken, acct.AccountRef, opts)
		if err != nil {
			return nil, "", err
		}
		ing := b.Ingest
		return &ing, b.ID, nil
	}
	ing, err := provider.Ingest(ctx, clientID, acct.AccessToken)
	if err != nil {
		return nil, "", err
	}
	return ing, "", nil
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
	if acct.RefreshToken == "" {
		return nil, fmt.Errorf("the %s token has expired and there is no refresh token; reconnect the account",
			acct.Platform)
	}

	provider, err := oauth.Get(acct.Platform)
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
	acct.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		acct.RefreshToken = tok.RefreshToken
	}
	acct.ExpiresAt = tok.ExpiresAt
	return s.store.UpsertPlatformAccount(s.box, acct)
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
