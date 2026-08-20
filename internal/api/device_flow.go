package api

// The device code flow's server half: connecting an account from a box that has
// no callback URL any platform will accept.
//
// internal/oauth/device.go carries the WHY, including why exactly one platform
// implements it. This file is the two requests a browser makes, and the one
// piece of state that sits between them.
//
// THE DEVICE CODE NEVER LEAVES THIS PROCESS, which is the whole shape of the
// design. oauth.DeviceAuth tags it `json:"-"` so a handler cannot leak it by
// marshalling the struct, and that tag is only worth anything if nothing here
// copies the value back out by hand. So the browser is given an opaque HANDLE
// instead -- a random token, exactly like the OAuth `state` parameter -- and the
// device code stays beside it in memory. A poll names the handle; the server
// names the code.
//
// IN MEMORY RATHER THAN IN A TABLE, and the precedent is metadataRegistry
// rather than oauth_states. Three properties decide it:
//
//   - The state is SHORT-LIVED by the platform's own clock. Twitch's device code
//     expires in 1800 seconds, so nothing here is worth a migration, an index or
//     a retention policy.
//   - It is a BEARER-EQUIVALENT SECRET. A row in SQLite would want the same
//     secrets.Box treatment platform_accounts gets, for a value that is dead
//     within half an hour; not writing it down at all is strictly better than
//     writing it down carefully.
//   - A RESTART MID-FLOW LOSES NOTHING THAT MATTERS. The operator has typed a
//     code into a phone and is waiting; the recovery is to press the button
//     again, which is the same recovery as an expired code. There is no
//     half-connected account to reconcile, because the account is written only
//     once a token exists.
//
// ON THE SERVER PACING THE POLL. PollDeviceAuth is single-shot on purpose (see
// oauth.DeviceFlower), so the pacing is the caller's, and the caller is a
// browser. A browser that polls faster than the platform asked -- a tab
// duplicated, a reload loop, a bug in a future refactor -- spends the
// OPERATOR'S rate limit on the operator's whole app, mid-connect, for a mistake
// nobody made. So the interval is enforced HERE as well as honoured in the UI:
// a poll that arrives early is answered "pending" without a request leaving the
// process. The client's honesty is a nicety; this is the guarantee.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// deviceStartTimeout and devicePollTimeout bound the two outbound calls. Both
// are one HTTP request to an authorization server; the poll is the tighter of
// the two because a browser is waiting on it every few seconds and a hung
// request would stack polls behind it.
const (
	deviceStartTimeout = 30 * time.Second
	devicePollTimeout  = 20 * time.Second
)

// devicePollState is the closed vocabulary a polling client branches on.
//
// THREE WORDS, NOT A BOOLEAN AND NOT AN ERROR CODE. "Still waiting for the
// operator" is the answer to nearly every poll and is not a failure -- oauth's
// ErrDeviceAuthPending says so in as many words -- so it cannot arrive as a 4xx
// that a client's error path would render as something having gone wrong. And
// "this code is dead, start again" is a different instruction from "the platform
// could not be reached, keep trying", so those cannot share a word either. A
// real transport failure is the only thing that leaves this file as a non-2xx.
type devicePollState string

const (
	// devicePending is the ordinary answer. The client waits its interval and
	// asks again.
	devicePending devicePollState = "pending"
	// deviceExpired means this handle will never produce a token: the code was
	// redeemed, it timed out, or the server no longer holds it. The client
	// stops polling and offers to start over.
	deviceExpired devicePollState = "expired"
	// deviceConnected means the account is stored. There is nothing left to
	// poll.
	deviceConnected devicePollState = "connected"
)

// deviceFlow is one started, unfinished authorization.
type deviceFlow struct {
	platform db.Platform
	clientID string
	// deviceCode is the bearer-equivalent secret. It has no JSON tag because it
	// is never in a struct that gets marshalled -- see the file header.
	deviceCode string
	interval   time.Duration
	expiresAt  time.Time
	// nextPollAt is the pacing guarantee. See the file header.
	nextPollAt time.Time
}

// deviceFlows is the in-process registry, one per Server.
//
// A FIELD RATHER THAN A PACKAGE VAR, unlike metadataRegistry. The difference is
// what the state IS: a metadata job is a report, and reading somebody else's is
// harmless. A device flow holds a credential that redeems an account onto a
// server, so it belongs to the server that started it. The zero value works, so
// a test building &Server{} by hand needs no construction ceremony.
type deviceFlows struct {
	mu       sync.Mutex
	byHandle map[string]*deviceFlow
}

// put records a started flow and drops any that have died of old age.
//
// The sweep is opportunistic, on the same reasoning as PutOAuthState's: a flow
// nobody polls again is a map entry nobody would otherwise remove, and the only
// event that reliably happens is somebody starting another one.
func (d *deviceFlows) put(handle string, f *deviceFlow, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.byHandle == nil {
		d.byHandle = map[string]*deviceFlow{}
	}
	for h, old := range d.byHandle {
		if !old.expiresAt.IsZero() && now.After(old.expiresAt) {
			delete(d.byHandle, h)
		}
	}
	d.byHandle[handle] = f
}

// take returns the flow for a handle. Not single-use: a poll happens many times
// per flow, which is the one way this differs from TakeOAuthState.
func (d *deviceFlows) take(handle string) (*deviceFlow, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.byHandle[handle]
	return f, ok
}

// forget drops a handle. Called the moment a flow can no longer produce a
// token, whether that is because it produced one or because it never will.
func (d *deviceFlows) forget(handle string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byHandle, handle)
}

// markPolled records that a poll went out and when the next one may.
func (d *deviceFlows) markPolled(handle string, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f, ok := d.byHandle[handle]; ok {
		f.nextPollAt = at
	}
}

// deviceAuthView is what a browser receives from a start.
//
// It EMBEDS oauth.DeviceAuth rather than restating its fields, which is what
// makes the `json:"-"` on DeviceCode load-bearing: a field copied out by hand
// here would be a field this file decided to publish, and the next person adding
// one to the provider struct would have to remember not to. Embedding means the
// provider's own decision about what is publishable is the one that ships.
type deviceAuthView struct {
	*oauth.DeviceAuth
	// Handle is the opaque name for the device code the server is holding.
	Handle string `json:"handle"`
	// IntervalSeconds is how long the client must wait between polls, already
	// floored by internal/oauth. Seconds because a JSON number of nanoseconds
	// is a trap for whoever reads it next.
	IntervalSeconds int `json:"intervalSeconds"`
}

// handleStartDeviceAuth begins a device authorization and hands back the code
// the operator types, the page they type it at, and a handle to poll with.
//
// A POST despite creating no row, for the reason handleCheckCreds is one: it
// makes an outbound call to a third party, so it is neither safe nor idempotent,
// and POST puts it behind requireCSRF with the rest of the state-changing group.
func (s *Server) handleStartDeviceAuth(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	if _, err := s.providers.Get(platform); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Through the Set twin rather than oauth.DeviceFor, on endpoints.go's own
	// terms: the package function would hand a stubbed server the PRODUCTION
	// Twitch provider and then poll id.twitch.tv every five seconds.
	df, ok := s.providers.DeviceFor(platform)
	if !ok {
		// A refusal rather than a 200 with supported:false, and the difference
		// from handleAccountStats is the verb. That route is a READ a dashboard
		// takes on every account it lists, so "we cannot ask this one" is a row
		// it has to render. This is a button an operator pressed for one
		// platform, and there is no useful screen for "the thing you just asked
		// for does not exist here" other than saying so.
		writeError(w, http.StatusBadRequest, deviceUnsupportedReason(platform))
		return
	}
	creds, err := s.store.GetPlatformCreds(s.box, platform)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed,
			fmt.Sprintf("no %s developer credentials configured. Add them in Settings → Platform credentials first.", platform))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), deviceStartTimeout)
	defer cancel()

	authz, err := df.StartDeviceAuth(ctx, creds.ClientID)
	if err != nil {
		// 502: the platform answered badly or not at all. Not 500 -- nothing
		// here failed -- and not 400, which would read as the operator's
		// credentials being wrong when the usual cause is the far end.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	handle, err := auth.RandomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	now := time.Now()
	s.devices.put(handle, &deviceFlow{
		platform:   platform,
		clientID:   creds.ClientID,
		deviceCode: authz.DeviceCode,
		interval:   authz.Interval,
		expiresAt:  authz.ExpiresAt,
		// The FIRST poll waits too. Twitch has just issued the code and the
		// operator has not begun typing; a poll fired the instant the dialog
		// opens is a guaranteed authorization_pending and one request of the
		// operator's budget spent on a foregone conclusion.
		nextPollAt: now.Add(authz.Interval),
	}, now)

	s.log.Info("device authorization started", "platform", platform)
	writeJSON(w, http.StatusOK, deviceAuthView{
		DeviceAuth:      authz,
		Handle:          handle,
		IntervalSeconds: int(authz.Interval / time.Second),
	})
}

// deviceUnsupportedReason names the platform rather than saying "unsupported".
//
// The three platforms without a device flow are absent for three different
// reasons and each is written out at the top of internal/oauth/device.go. This
// is not the place to restate them -- a second copy drifts -- but it IS the
// place to make sure the operator is told which platform was refused, since the
// answer differs per platform and a generic sentence would read as a fault.
func deviceUnsupportedReason(p db.Platform) string {
	return fmt.Sprintf("%s has no device authorization flow polyemesis can use; "+
		"connect this account with the ordinary Connect button instead", p)
}

// devicePollView is what a browser receives from a poll.
type devicePollView struct {
	State devicePollState `json:"state"`
	// RetryInSeconds is repeated on every pending answer rather than only at
	// the start, so a client that lost the start response -- a reload, a
	// restored tab -- still paces itself correctly.
	RetryInSeconds int `json:"retryInSeconds,omitempty"`
	// Account is present only on "connected".
	Account *db.PlatformAccount `json:"account,omitempty"`
	// Reason explains an "expired" in words the operator can act on. Never a
	// credential: the strings that reach it are oauth's own sentinels.
	Reason string `json:"reason,omitempty"`
}

// handlePollDeviceAuth redeems the device code once, or says why it did not.
func (s *Server) handlePollDeviceAuth(w http.ResponseWriter, r *http.Request) {
	platform := db.Platform(chi.URLParam(r, "platform"))
	var req struct {
		Handle string `json:"handle"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	flow, ok := s.devices.take(req.Handle)
	// A handle this server does not hold is EXPIRED rather than 404, and the
	// two causes are indistinguishable from here: a restart dropped it, or the
	// sweep did. Both have the same recovery and the client already has a
	// branch for it.
	if !ok {
		writeJSON(w, http.StatusOK, devicePollView{
			State:  deviceExpired,
			Reason: "this device authorization is no longer being tracked; start it again",
		})
		return
	}
	// The handle is scoped to the platform it was started for. Not a security
	// boundary -- the handle is unguessable and both halves are behind the same
	// session -- but a mismatch means the client has muddled two dialogs, and
	// redeeming a Twitch code against whatever provider the URL named would
	// store the wrong thing.
	if flow.platform != platform {
		writeError(w, http.StatusBadRequest,
			"this device authorization belongs to a different platform")
		return
	}

	now := time.Now()
	if !flow.expiresAt.IsZero() && now.After(flow.expiresAt) {
		s.devices.forget(req.Handle)
		writeJSON(w, http.StatusOK, devicePollView{
			State:  deviceExpired,
			Reason: "the code expired before it was entered; start again to get a new one",
		})
		return
	}
	// EARLY POLLS ARE ANSWERED WITHOUT A REQUEST LEAVING THE PROCESS. See the
	// file header: this is the guarantee, and the client honouring the interval
	// is the nicety.
	if now.Before(flow.nextPollAt) {
		writeJSON(w, http.StatusOK, devicePollView{
			State:          devicePending,
			RetryInSeconds: secondsUntil(flow.nextPollAt, now),
		})
		return
	}

	df, ok := s.providers.DeviceFor(flow.platform)
	if !ok {
		// Only reachable if the build changed under a live flow, which is a
		// restart, which drops the registry. Answered rather than left to
		// panic on a nil interface.
		s.devices.forget(req.Handle)
		writeError(w, http.StatusBadRequest, deviceUnsupportedReason(flow.platform))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), devicePollTimeout)
	defer cancel()

	s.devices.markPolled(req.Handle, now.Add(flow.interval))
	tok, err := df.PollDeviceAuth(ctx, flow.clientID, flow.deviceCode)
	switch {
	case errors.Is(err, oauth.ErrDeviceAuthPending):
		writeJSON(w, http.StatusOK, devicePollView{
			State:          devicePending,
			RetryInSeconds: int(flow.interval / time.Second),
		})
		return
	case errors.Is(err, oauth.ErrDeviceCodeSpent):
		s.devices.forget(req.Handle)
		writeJSON(w, http.StatusOK, devicePollView{
			State: deviceExpired,
			Reason: "this code has already been used or has expired; start again to get " +
				"a new one",
		})
		return
	case err != nil:
		// A real failure -- 429, a 5xx, a network fault. The flow is KEPT: the
		// code is still good and the operator may still be typing, so the next
		// poll is worth making. classifyTwitchDeviceError is what guarantees a
		// pending authorization never arrives here.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	acct, err := df.Account(ctx, flow.clientID, tok.AccessToken)
	if err != nil {
		// The token is real and the account read failed. The device code is
		// spent either way -- Twitch will not mint a second token from it --
		// so the handle goes, and the operator starts over rather than polling
		// a code that can only answer "invalid device code" from here on.
		s.devices.forget(req.Handle)
		writeError(w, http.StatusBadGateway, "could not read the account: "+err.Error())
		return
	}

	saved, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform:     flow.platform,
		AccountName:  acct.Name,
		AccountRef:   acct.Ref,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
		Scopes:       tok.Scopes,
		// Stamped at CONNECT time for the same reason handleOAuthCallback
		// stamps it there: this is the only moment the granted scopes and the
		// requested ones are known to agree. StartDeviceAuth asks for exactly
		// Scopes(), so an account connected this way is indistinguishable from
		// a code-flow one afterwards.
		ScopeVer: df.ScopeVersion(),
	})
	if err != nil {
		s.devices.forget(req.Handle)
		writeStoreError(w, err)
		return
	}

	s.devices.forget(req.Handle)
	s.log.Info("platform account connected by device flow",
		"platform", flow.platform, "account", acct.Name)
	writeJSON(w, http.StatusOK, devicePollView{State: deviceConnected, Account: saved})
}

// secondsUntil rounds UP, and the rounding direction is the point.
//
// A client told to wait 0 seconds polls immediately, which is the hot loop the
// interval exists to prevent; 900ms of remaining wait must read as 1 rather than
// as 0. It never returns a negative, so a clock that moved backwards produces a
// wait rather than an immediate retry.
func secondsUntil(at, now time.Time) int {
	d := at.Sub(now)
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
