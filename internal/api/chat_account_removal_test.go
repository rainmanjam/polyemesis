package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// A connected account's chat outliving the account.
//
// Reported from staging on v0.12.0: the operator removed Facebook in Settings →
// Platform credentials, every platform there read "Not set up" and "No channel
// connected yet", and the Dashboard's chat card still showed a FACEBOOK chip
// reading "Connected and waiting". The database held no account, no
// credentials, no destination and no message -- the chip came from the chat
// Hub, which StartChat filled once at startup and nothing ever emptied.
//
// A Facebook adapter with no live video never calls its token, so it never
// found out the account had gone; it sat in the Hub reporting "waiting for a
// broadcast" until the process restarted. The tests below go through the real
// router and the real StartChat, because the defect is in the seam between
// them: the delete handlers did their part of the job and never told the Hub.

// idleChat is a chat adapter that connects to nothing and reads nothing until it
// is told to stop. It stands in for platforms whose real adapter would dial the
// platform on Attach (Twitch IRC, YouTube's poll), so a test can hold one
// attached beside the adapter under test without touching the network.
type idleChat struct {
	platform db.Platform
	account  string
}

func (a idleChat) Platform() db.Platform { return a.platform }
func (a idleChat) Account() string       { return a.account }
func (a idleChat) Run(ctx context.Context, _ chat.Receiver) error {
	<-ctx.Done()
	return nil
}

// chatAccounts is what GET /api/v1/chat says is attached, as "platform:account"
// pairs -- the list the Dashboard's chat card draws its platform chips from.
func chatAccounts(t *testing.T, h http.Handler, sign func(*http.Request)) map[string]bool {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/chat", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /chat: status %d, body %s", w.Code, w.Body.String())
	}
	var got chatOverview
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /chat: %v", err)
	}
	out := map[string]bool{}
	for _, st := range got.Statuses {
		out[string(st.Platform)+":"+st.Account] = true
	}
	return out
}

// hubAccounts is the same list read off the Hub directly, so a failure says
// whether the Hub kept the runner or only the response did.
func hubAccounts(h *chat.Hub) map[string]bool {
	out := map[string]bool{}
	for _, st := range h.Statuses() {
		out[string(st.Platform)+":"+st.Account] = true
	}
	return out
}

// facebookChatServer is a server with chat on, Facebook developer credentials
// stored, and the given Facebook pages connected and attached by the real
// StartChat. Facebook is the platform the bug was reported on, and the one
// whose real adapter is safe to run here: with no destination there is no live
// video id to find, so it neither adopts a broadcast nor polls Graph -- it
// reports "waiting" and sleeps, which is exactly the state that never noticed
// its account had gone.
func facebookChatServer(t *testing.T, pages ...string) (*Server, http.Handler, *db.DB, func(*http.Request), map[string]int64) {
	t.Helper()
	s, h, store := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "fb-app-id", "fb-app-secret"); err != nil {
		t.Fatalf("store facebook credentials: %v", err)
	}
	ids := map[string]int64{}
	for _, p := range pages {
		ids[p] = connectAccount(t, store, s.box, db.PlatformFacebook, p)
	}
	if n := s.StartChat(context.Background()); n != len(pages) {
		t.Fatalf("StartChat attached %d adapters, want %d (one per connected page)", n, len(pages))
	}
	return s, h, store, login(t, h), ids
}

// TestDisconnectingAnAccountDetachesItsChat is the staging report itself, and
// the narrow half of the claim: the account that was disconnected leaves the
// chat surface at once, and the one that was not stays.
//
// Mutation: replace the s.detachAccountChat call in handleDeleteAccount with a
// no-op. Observed FAIL ("facebook:page-ref is still attached after its account was
// disconnected").
func TestDisconnectingAnAccountDetachesItsChat(t *testing.T) {
	s, h, _, sign, ids := facebookChatServer(t, "page", "other")

	if got := chatAccounts(t, h, sign); !got["facebook:page-ref"] || !got["facebook:other-ref"] {
		t.Fatalf("before the disconnect /chat lists %v, want both pages", got)
	}

	w := deleteAccount(t, h, sign, ids["page"], nil)
	if w.Code != http.StatusOK {
		t.Fatalf("disconnect: status %d, body %s", w.Code, w.Body.String())
	}

	if got := hubAccounts(s.chat); got["facebook:page-ref"] {
		t.Fatalf("facebook:page-ref is still attached after its account was disconnected: hub lists %v", got)
	}
	got := chatAccounts(t, h, sign)
	if got["facebook:page-ref"] {
		t.Fatalf("GET /chat still lists the disconnected page, so the Dashboard still draws its chip: %v", got)
	}
	if !got["facebook:other-ref"] {
		t.Fatalf("disconnecting one page took the other page's chat with it: /chat lists %v", got)
	}
	if _, ok := s.chat.Adapter(db.PlatformFacebook, "page-ref"); ok {
		t.Fatal("the Hub still hands out the disconnected page's adapter, so a broadcast refresh would point it at a live video")
	}
}

// TestARefusedDisconnectKeepsTheChat pins the ORDER: the detach belongs after a
// delete that happened, never before the guard that can refuse it. A refused
// disconnect deletes nothing, so the account's chat must carry on.
//
// Mutation: move the detach above the account_in_use guard in
// handleDeleteAccount. Observed FAIL ("a refused disconnect detached the
// account's chat").
func TestARefusedDisconnectKeepsTheChat(t *testing.T) {
	s, h, store, sign, ids := facebookChatServer(t, "page")
	acctID := ids["page"]
	if _, err := store.CreateDestination(&db.Destination{
		Name: "Facebook main", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp/", StreamKey: "FB-1-0-abc",
		AccountID: &acctID, Enabled: true,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}

	w := deleteAccount(t, h, sign, acctID, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed disconnect with an enabled destination: status %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if !hubAccounts(s.chat)["facebook:page-ref"] {
		t.Fatal("a refused disconnect detached the account's chat, though the account is still connected")
	}

	// And the confirmed retry, which does delete, does detach.
	w = deleteAccount(t, h, sign, acctID, map[string]bool{"confirm": true})
	if w.Code != http.StatusOK {
		t.Fatalf("confirmed disconnect: status %d, body %s", w.Code, w.Body.String())
	}
	if got := chatAccounts(t, h, sign); got["facebook:page-ref"] {
		t.Fatalf("a confirmed disconnect left the page's chat attached: /chat lists %v", got)
	}
}

// TestRemovingPlatformCredentialsDetachesThatPlatformsChat is the other route
// in the report: Settings → Platform credentials → Remove. Every token on the
// platform was issued to the app being removed, and nothing can refresh one
// without it, so the platform's chat ends with it -- and only that platform's.
//
// Mutation: drop the s.detachPlatformChat call from handleDeleteCreds.
// Observed FAIL ("facebook chat is still attached after its credentials were
// removed").
func TestRemovingPlatformCredentialsDetachesThatPlatformsChat(t *testing.T) {
	s, h, store, sign, _ := facebookChatServer(t, "page", "other")

	// A Twitch account on its own credentials, attached beside the pages. An
	// idle stand-in rather than the real IRC adapter, which would dial Twitch.
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "tw-app-id", "tw-app-secret"); err != nil {
		t.Fatalf("store twitch credentials: %v", err)
	}
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	if err := s.chat.Attach(context.Background(), idleChat{db.PlatformTwitch, "dj-ref"}); err != nil {
		t.Fatalf("attach twitch: %v", err)
	}

	// Confirmed: accounts still use the app, and an unconfirmed removal is
	// refused with creds_in_use (creds_in_use.go).
	r := jsonRequest(t, http.MethodDelete, "/api/v1/platforms/credentials/facebook", map[string]bool{"confirm": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("remove credentials: status %d, body %s", w.Code, w.Body.String())
	}

	got := chatAccounts(t, h, sign)
	for _, ref := range []string{"facebook:page-ref", "facebook:other-ref"} {
		if got[ref] {
			t.Fatalf("facebook chat is still attached after its credentials were removed: /chat lists %v", got)
		}
	}
	if !got["twitch:dj-ref"] {
		t.Fatalf("removing Facebook's credentials detached Twitch's chat: /chat lists %v", got)
	}
}

// TestRestartAfterCredentialsRemovedDoesNotBringTheChatBack is what keeps the
// detach above honest. The accounts outlive their credentials -- nothing
// deletes them -- so if StartChat attached every account regardless, the next
// restart would put back the very chip the removal took away: the same bug,
// waiting for a restart instead of fixed by one.
//
// Mutation: make the withCreds check in StartChat never skip. Observed FAIL
// ("StartChat attached 1 adapters, want 0").
func TestRestartAfterCredentialsRemovedDoesNotBringTheChatBack(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	connectAccount(t, store, s.box, db.PlatformFacebook, "page")

	if n := s.StartChat(context.Background()); n != 0 {
		t.Fatalf("StartChat attached %d adapters, want 0", n)
	}
	if got := hubAccounts(s.chat); got["facebook:page-ref"] {
		t.Fatalf("a restart re-attached facebook chat with no Facebook credentials stored: hub lists %v", got)
	}
}

// TestRemovingCredentialsLeavesRumbleAlone pins the one exception. Rumble's
// chat hangs off an environment variable rather than an app or an account
// (see attachRumble), so no credentials route has any claim on it -- and a
// DELETE that names it, which deletes nothing, must not end it either.
//
// Mutation: drop the Rumble guard from detachPlatformChat. Observed FAIL
// ("removing \"rumble\" credentials detached the environment-keyed Rumble chat").
func TestRemovingCredentialsLeavesRumbleAlone(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	sign := login(t, h)
	if err := s.chat.Attach(context.Background(), idleChat{db.PlatformRumble, "rumble"}); err != nil {
		t.Fatalf("attach rumble: %v", err)
	}

	r := jsonRequest(t, http.MethodDelete, "/api/v1/platforms/credentials/rumble", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("remove credentials: status %d, body %s", w.Code, w.Body.String())
	}
	if !hubAccounts(s.chat)["rumble:rumble"] {
		t.Fatal(`removing "rumble" credentials detached the environment-keyed Rumble chat`)
	}
}
