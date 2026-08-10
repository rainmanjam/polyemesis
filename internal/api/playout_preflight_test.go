package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestPlayoutPreflightGateMatrix is TestPlayoutGateMatrix's missing column
// (#170): OPTIONS, across Enabled x Public x Protection x principal x
// AllowCrossOrigin.
//
// WHAT WAS WRONG. The OPTIONS branch sat ABOVE the gate and answered 204 to
// anyone, on any configuration, including a server with playout switched OFF --
// where GET deliberately answers 404 and discloses nothing. With
// AllowCrossOrigin on it also attached Access-Control-Allow-Origin and
// -Allow-Methods, because internal/playout sets CORS above its own Enabled
// check. So an anonymous caller could establish, on a server hiding its playout
// entirely, both that the origin is mounted and that cross-origin embedding is
// enabled.
//
// WHAT MUST NOT BREAK. Answering the preflight from BELOW the full gate would
// 401 it whenever the stream is token-protected, and a failed preflight means
// the browser never sends the real request -- so a token-protected PUBLIC stream
// could not be embedded cross-origin at all, and the SameSite=None cookie
// handoff that flow depends on would never happen. That is why only the
// CONFIGURATION half (Enabled, Public, operator) holds the preflight, and it is
// the "public token / anonymous" row below that proves the carve-out is doing
// its job: 204 for OPTIONS while GET on the same URL is 401.
func TestPlayoutPreflightGateMatrix(t *testing.T) {
	const media = "/playout/master.m3u8"

	// want[config][principal] -- OPTIONS status.
	want := map[string]map[string]int{
		// Enabled, NOT public. Only the operator; everybody else gets the same
		// 404 GET gives them.
		"private": {
			"anonymous":                    404,
			"query token":                  404,
			"cookie token":                 404,
			"basic auth token":             404,
			"read bearer":                  404,
			"read bearer with query token": 404,
			"admin bearer":                 204,
			"session":                      204,
			"garbage bearer":               404,
		},
		// Published to everyone: every preflight succeeds, as it must.
		"public open": {
			"anonymous":                    204,
			"query token":                  204,
			"cookie token":                 204,
			"basic auth token":             204,
			"read bearer":                  204,
			"read bearer with query token": 204,
			"admin bearer":                 204,
			"session":                      204,
			"garbage bearer":               204,
		},
		// Published behind the playback token. THE ROW THE CARVE-OUT EXISTS
		// FOR. Every one of these is 204 even though GET as "anonymous" is 401,
		// because a preflight carries no credentials by specification and a
		// browser that is refused one never sends the real request. Public is
		// true here, so the configuration half is satisfied and the credential
		// half is deliberately not consulted.
		"public token": {
			"anonymous":                    204,
			"query token":                  204,
			"cookie token":                 204,
			"basic auth token":             204,
			"read bearer":                  204,
			"read bearer with query token": 204,
			"admin bearer":                 204,
			"session":                      204,
			"garbage bearer":               204,
		},
	}

	for _, crossOrigin := range []bool{false, true} {
		label := "cors off"
		if crossOrigin {
			label = "cors on"
		}
		t.Run(label, func(t *testing.T) {
			for _, cfg := range playoutConfigs() {
				t.Run(cfg.name, func(t *testing.T) {
					set := cfg.set
					set.AllowCrossOrigin = crossOrigin
					_, h, sign := playoutOriginServer(t, set, cfg.pub)
					for _, p := range playoutPrincipals() {
						t.Run(p.name, func(t *testing.T) {
							r := httptest.NewRequest(http.MethodOptions, media, nil)
							r.RemoteAddr = "203.0.113.9:5555"
							r.Header.Set("Origin", "https://embedder.example")
							r.Header.Set("Access-Control-Request-Method", "GET")
							p.apply(t, h, sign, r)
							w := do(t, h, r)

							wantCode := want[cfg.name][p.name]
							if got := w.Code; got != wantCode {
								t.Fatalf("OPTIONS %s as %s on a %s stream (%s) = %d, want %d",
									media, p.name, cfg.name, label, got, wantCode)
							}

							// THE DISCLOSURE, stated as a header assertion. A
							// denied preflight must not carry the CORS bit
							// either: the headers are set above the Enabled
							// check inside internal/playout, so reaching that
							// handler at all is what leaked them.
							acao := w.Header().Get("Access-Control-Allow-Origin")
							if wantCode == http.StatusNotFound && acao != "" {
								t.Errorf("a DENIED preflight still carried "+
									"Access-Control-Allow-Origin=%q. That header answers "+
									"\"is cross-origin embedding switched on here\" on a "+
									"server that is refusing to admit playout exists.", acao)
							}
							if wantCode == http.StatusNoContent && crossOrigin && acao == "" {
								t.Errorf("an ALLOWED preflight lost its CORS headers with " +
									"AllowCrossOrigin on; a cross-origin embed cannot fetch " +
									"a segment it is not allowed to read")
							}
						})
					}
				})
			}
		})
	}
}

// TestPreflightAndGetAgreeOnADisabledServer is the disclosure stated at its
// narrowest: with playout OFF, OPTIONS must be as silent as GET.
//
// Separate from the matrix because "disabled" is not one of playoutConfigs'
// three rows -- all three have Enabled true -- and disabled is the case where
// the old behaviour was worst: everything else about playout answered 404, and
// OPTIONS answered 204.
func TestPreflightAndGetAgreeOnADisabledServer(t *testing.T) {
	const media = "/playout/master.m3u8"

	for _, crossOrigin := range []bool{false, true} {
		set := db.PlayoutSettings{Enabled: false, AllowCrossOrigin: crossOrigin}
		_, h, _ := playoutOriginServer(t, set,
			playoutPublish{Protection: PlayoutProtectOpen, Token: testToken})

		get := httptest.NewRequest(http.MethodGet, media, nil)
		get.RemoteAddr = "203.0.113.9:5555"
		gw := do(t, h, get)

		opt := httptest.NewRequest(http.MethodOptions, media, nil)
		opt.RemoteAddr = "203.0.113.9:5555"
		opt.Header.Set("Origin", "https://embedder.example")
		opt.Header.Set("Access-Control-Request-Method", "GET")
		ow := do(t, h, opt)

		if ow.Code != gw.Code {
			t.Errorf("playout disabled (crossOrigin=%v): OPTIONS = %d but GET = %d. "+
				"A preflight that answers differently from the method it is asking about "+
				"IS the disclosure -- it says a playout origin is mounted on a server "+
				"that is refusing to say so.", crossOrigin, ow.Code, gw.Code)
		}
		if got := ow.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("playout disabled (crossOrigin=%v): the preflight carried "+
				"Access-Control-Allow-Origin=%q, which additionally discloses that the "+
				"operator has switched cross-origin embedding on", crossOrigin, got)
		}
	}
}
