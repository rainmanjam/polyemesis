package api

// THE PLAYOUT COOKIE'S FLAGS, PINNED BY BEHAVIOUR.
//
// Sonar reports go:S2092 against setPlayoutTokenCookie -- "omitting the Secure
// flag set to true makes cookie insecure". Secure is not a literal here for the
// same reason it is not one in internal/auth: polyemesis is self-hosted and
// runs on plain HTTP in a lab and behind a terminating proxy in production.
// Hardcoding it would drop the player's credential on exactly the deployments
// that work today.
//
// What must hold regardless of deployment is that this cookie is never readable
// by script, never escapes the media prefix, and only becomes SameSite=None --
// which browsers require Secure for -- when the connection actually is secure.
// A None cookie without Secure is rejected outright by every current browser,
// so getting that pair wrong breaks embedding silently.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func playoutCookie(t *testing.T, cfg config.Config, crossOrigin bool) *http.Cookie {
	t.Helper()
	s, _, _ := testServer(t, cfg)
	w := httptest.NewRecorder()
	s.setPlayoutTokenCookie(w, "tok", crossOrigin)
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("setPlayoutTokenCookie wrote %d cookies, want 1", len(cs))
	}
	return cs[0]
}

func TestThePlayoutCookieIsScopedAndNeverScriptReadable(t *testing.T) {
	for _, crossOrigin := range []bool{false, true} {
		c := playoutCookie(t, config.Config{}, crossOrigin)
		if !c.HttpOnly {
			t.Errorf("crossOrigin=%v: the playout token cookie is readable by script; "+
				"it is a credential for the media prefix", crossOrigin)
		}
		if c.Path != PlayoutPrefix {
			t.Errorf("crossOrigin=%v: cookie Path is %q, want %q -- widening it hands "+
				"the admin API a credential it should never see", crossOrigin, c.Path, PlayoutPrefix)
		}
	}
}

func TestSameSiteNoneIsNeverSetWithoutSecure(t *testing.T) {
	// Browsers reject `SameSite=None` unless `Secure` is also set, so this pair
	// is not a style preference -- getting it wrong makes the embed stop
	// working with no error anyone sees server-side.
	c := playoutCookie(t, config.Config{}, true)
	if c.SameSite == http.SameSiteNoneMode && !c.Secure {
		t.Fatal("the playout cookie is SameSite=None without Secure. Every current " +
			"browser drops such a cookie, so cross-origin embedding would fail " +
			"silently rather than fall back to the per-URL token.")
	}
}
