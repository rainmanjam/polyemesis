package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// A stubbed provider set has to capture the credential check too.
//
// This was the last hole in the seam removal. Both credential handlers
// validated the platform through s.providers and then called the PACKAGE-level
// oauth.CheckCredentialsFor, which resolves its own provider against the real
// platform hosts. A server built with oauth.NewSet(oauth.WithBaseURL(stub))
// therefore had a partially redirected provider set -- everything stubbed
// except this -- which is precisely what internal/oauth/endpoints.go opens by
// warning about.
//
// It survived the refactor because the only credential-check test in this
// package used YouTube, whose check is format-only and never opens a socket.
// So this uses TWITCH, which really does POST to a token endpoint.
//
// Mutation proving it can fail: in handleCheckCreds, change
// `s.providers.CheckCredentialsFor(...)` back to
// `oauth.CheckCredentialsFor(...)`. Measured: FAIL, no call reached the stub --
// the request went to id.twitch.tv.
func TestTheCredentialCheckGoesThroughTheInjectedProviderSet(t *testing.T) {
	stub := newPlatformStub(t)
	s, h, store := testServerWith(t, Options{Config: config.Config{}, Providers: stub.set()})
	sign := login(t, h)

	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed twitch credentials: %v", err)
	}

	send(t, h, sign, http.MethodPost, "/api/v1/platforms/credentials/twitch/check", nil, http.StatusOK)

	// Assert on the CALL rather than on the verdict. A verdict is reachable
	// without the stub -- an unreachable real host produces one too, and that
	// is exactly the state this guards against.
	if got := stub.matching(http.MethodPost, "/oauth2/token"); len(got) == 0 {
		t.Fatalf("the credential check never reached the stub, so it went to the real "+
			"platform. Calls seen: %v", stub.calls())
	}
	// Deliberately NOT asserting the client id and secret here. Twitch's check
	// goes through postForm, which sends them form-encoded in the body, and the
	// stub records only a JSON body -- so both come back empty and an assertion
	// on them would be measuring the stub's parser, not the handler. Which
	// credentials travel is already guarded by TestRecheckUsesTheStoredCredential.
	// The claim this test exists for is where the request went.
}
