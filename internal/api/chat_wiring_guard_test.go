package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// testConfigWithPublicURL is the minimum that makes publicBaseURL non-empty,
// which the Kick adapter needs before it will build.
func testConfigWithPublicURL() config.Config {
	return config.Config{TLS: config.TLS{
		Mode:     config.ModeManual,
		Enabled:  true,
		Hostname: "stream.example.com",
		CertFile: "cert.pem",
		KeyFile:  "key.pem",
	}}
}

// The wiring guard for the Kick webhook verifier.
//
// This test exists because of a specific way the original bug survived review.
// The verification hook was declared on KickConfig, the handler nil-checked it,
// and every unit test in internal/chat passed — because the adapter under test
// was constructed by the test itself, with whatever config the test wanted. The
// production construction site, in chatAdapter below, simply never set the
// field, and no test looked at that construction site at all.
//
// A guard on the adapter's behaviour therefore proves nothing about the running
// server. This one goes through the real constructor and asserts on what the
// real endpoint does, so removing `Verify:` from chatAdapter fails here.
func TestKickAdapterIsBuiltWithSignatureVerification(t *testing.T) {
	s := &Server{
		cfg:      testConfigWithPublicURL(),
		kickKeys: &chat.KickKeyFetcher{},
	}

	adapter, err := s.chatAdapter(t.Context(), db.PlatformAccount{
		ID:          1,
		Platform:    db.PlatformKick,
		AccountRef:  "99",
		AccountName: "mychannel",
	})
	if err != nil {
		t.Fatalf("chatAdapter: %v", err)
	}

	k, ok := adapter.(*chat.KickAdapter)
	if !ok {
		t.Fatalf("chatAdapter returned %T, want *chat.KickAdapter", adapter)
	}

	srv := httptest.NewServer(k.Handler())
	defer srv.Close()

	// An unsigned POST, which is what anybody who learned the callback URL
	// would send. It must not be accepted.
	//
	// 401 is the pass condition. 503 would mean the adapter was built with no
	// verifier at all — safe, but not wired. 200 is the original bug.
	resp, err := srv.Client().Post(srv.URL, "application/json",
		strings.NewReader(`{"content":"forged"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// Verified and refused: correct.
	case http.StatusServiceUnavailable:
		t.Fatal("the Kick adapter was built without a Verify function; " +
			"chatAdapter must pass chat.KickVerifier(s.kickKeys)")
	default:
		t.Fatalf("an unsigned delivery returned %d, want 401: the production "+
			"Kick adapter is accepting unauthenticated chat injection",
			resp.StatusCode)
	}
}
