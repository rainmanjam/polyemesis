package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// #12 of the 2026-11-04 lingering-state audit. A device flow carries the client
// id it was started with and never re-read the credentials, so a code issued
// before the operator removed the app still redeemed and stored a connected
// account for an app that was gone.
func TestDeletingCredentialsEndsAPendingDeviceFlow(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	start := startDeviceFlow(t, h, sign)
	send(t, h, sign, http.MethodDelete, "/api/v1/platforms/credentials/twitch", nil, http.StatusOK)

	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}
	openThePollGate(t, s, start.Handle)
	got := pollDeviceFlow(t, h, sign, start.Handle)
	if got.State != deviceExpired {
		t.Fatalf("state = %q after the credentials were removed, want %q", got.State, deviceExpired)
	}
	if accts, err := s.store.ListPlatformAccounts(); err != nil || len(accts) != 0 {
		t.Fatalf("an account was stored for a removed app: %v %v", accts, err)
	}
	stub.mu.Lock()
	calls := stub.tokenCalls
	stub.mu.Unlock()
	if calls != 0 {
		t.Errorf("%d token request(s) went out for an app that no longer exists here", calls)
	}
	if _, held := s.devices.take(start.Handle); held {
		t.Error("the flow is still held after it was expired")
	}
}

// Replacing the app with a different client id mid-flow expires it too: the
// code belongs to the old app.
func TestReplacingCredentialsEndsAPendingDeviceFlow(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	start := startDeviceFlow(t, h, sign)
	if err := s.store.PutPlatformCreds(s.box, db.PlatformTwitch, "a-different-client", "another-secret"); err != nil {
		t.Fatal(err)
	}
	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}
	openThePollGate(t, s, start.Handle)
	if got := pollDeviceFlow(t, h, sign, start.Handle); got.State != deviceExpired {
		t.Fatalf("state = %q after the app was replaced, want %q", got.State, deviceExpired)
	}
}
