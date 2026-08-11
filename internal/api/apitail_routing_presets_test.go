package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The routing preset routes, and the one property nobody was checking: that
// what GET /routing/presets ADVERTISES as its defaults is what POST
// /routing/presets/{preset} actually applies when it is given nothing.
//
// It was not. See handleApplyPreset -- a bodyless apply ran on
// routing.PresetOpts' zero value, so "mic only" compiled the FULL MIX while the
// catalogue two routes away said track 3. Both routes answered 200 and neither
// could be caught by a status assertion, which is why this is written as a
// consistency claim between two endpoints rather than as a fixture of expected
// bytes.

// apitailCompiled is the shape both preset routes answer with: the compiled
// graph beside the profile that produced it.
type apitailCompiled struct {
	Routing struct {
		FilterComplex string `json:"filterComplex"`
		OutLabel      string `json:"outLabel"`
		Summary       string `json:"summary"`
		Tracks        []int  `json:"tracks"`
	} `json:"routing"`
	Profile routing.Profile `json:"profile"`
}

// apitailReached fails when a request never got as far as the handler.
//
// Thirteen routes are denied to read-scoped tokens and several groups are
// session-only, so a test driving a handler with the wrong principal is
// answered by requireScope BEFORE the handler runs. A loose assertion then
// passes having exercised nothing. Every test in the apitail_ files opens with
// this so that failure is named rather than tolerated.
func apitailReached(t *testing.T, w *httptest.ResponseRecorder, principal, route string) {
	t.Helper()
	if w.Code == http.StatusForbidden {
		t.Fatalf("%s never reached the handler for %s: 403 from the scope middleware, "+
			"so everything below this line would have asserted on a refusal: %s",
			principal, route, w.Body.String())
	}
	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("%s got 503 on %s: the handler answered on a nil dependency and "+
			"exercised none of the feature: %s", principal, route, w.Body.String())
	}
}

// apitailPresetDefaults reads the catalogue's advertised defaults. The whole
// point of this file is that these numbers are never written down here.
func apitailPresetDefaults(t *testing.T, h http.Handler, sign func(*http.Request)) routing.PresetOpts {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/routing/presets", nil)
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "GET /routing/presets")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /routing/presets: status %d, body %s", w.Code, w.Body.String())
	}
	var cat struct {
		Defaults routing.PresetOpts `json:"defaults"`
		Presets  []routing.Preset   `json:"presets"`
	}
	decodeInto(t, w.Body.Bytes(), &cat)
	if len(cat.Presets) == 0 {
		t.Fatal("GET /routing/presets advertised no presets at all")
	}
	return cat.Defaults
}

func TestApplyingAPresetWithNoBodyUsesTheDefaultsTheListingAdvertises(t *testing.T) {
	h, _, sign := sourceServer(t)

	advertised := apitailPresetDefaults(t, h, sign)

	// The mic-only preset is the one that can tell the two answers apart,
	// because it selects exactly one track and names it in the graph. That
	// only works while the advertised mic track is not ALSO the zero value: at
	// micTrack 0 a handler applying the defaults and a handler applying
	// PresetOpts{} compile the identical string and this test proves nothing.
	// Failing loudly beats passing vacuously.
	if advertised.MicTrack == 0 {
		t.Fatalf("the catalogue now advertises micTrack 0, which is also "+
			"routing.PresetOpts' zero value, so this test can no longer tell "+
			"\"applied the advertised defaults\" from \"applied nothing\". "+
			"Point it at a preset whose advertised option is non-zero. "+
			"Advertised defaults: %+v", advertised)
	}

	const preset = routing.PresetMicOnly
	route := "/api/v1/routing/presets/" + preset

	apply := func(t *testing.T, label string, mk func() *http.Request) apitailCompiled {
		t.Helper()
		r := mk()
		sign(r)
		w := do(t, h, r)
		apitailReached(t, w, "the session principal", "POST "+route)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: POST %s answered %d, body %s", label, route, w.Code, w.Body.String())
		}
		var out apitailCompiled
		decodeInto(t, w.Body.Bytes(), &out)
		return out
	}

	// The reference: an apply that states the advertised defaults explicitly,
	// with a Content-Length, which is the only form that has ever worked.
	want := apply(t, "explicit advertised defaults", func() *http.Request {
		return jsonRequest(t, http.MethodPost, route, advertised)
	})

	// It must genuinely name the advertised track, or the comparisons below are
	// comparing two wrong answers to each other.
	wantLabel := fmt.Sprintf("[0:a:%d]", advertised.MicTrack)
	if !strings.Contains(want.Routing.FilterComplex, wantLabel) {
		t.Fatalf("applying %s with the advertised defaults (micTrack %d) compiled a "+
			"graph that does not map %s at all: %q",
			preset, advertised.MicTrack, wantLabel, want.Routing.FilterComplex)
	}
	if len(want.Routing.Tracks) != 1 || want.Routing.Tracks[0] != advertised.MicTrack {
		t.Fatalf("applying %s with the advertised defaults contributed tracks %v, "+
			"want exactly [%d] -- the advertised mic track",
			preset, want.Routing.Tracks, advertised.MicTrack)
	}

	cases := []struct {
		name string
		mk   func() *http.Request
		why  string
	}{
		{
			name: "no body at all",
			why: "a curl one-liner or a script that just names the preset. " +
				"The handler's own comment promises the OBS-convention defaults here",
			mk: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, route, http.NoBody)
				r.Header.Set("Content-Type", "application/json")
				r.RemoteAddr = "203.0.113.5:44444"
				return r
			},
		},
		{
			name: "chunked, empty",
			why: "ContentLength is set to -1 BY HAND because httptest.NewRequest " +
				"always fills in a real length and a chunked request never carries " +
				"one. -1 is what net/http reports for every chunked body, whatever " +
				"it holds, so a handler that gates on ContentLength > 0 treats " +
				"every chunked request as bodyless",
			mk: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(""))
				r.ContentLength = -1
				r.Header.Set("Content-Type", "application/json")
				r.RemoteAddr = "203.0.113.5:44444"
				return r
			},
		},
		{
			// NOTE: this case cannot discriminate on its own, and the sub-test
			// below it is what does. A body carrying the advertised defaults is
			// indistinguishable from a body that was thrown away, because the
			// discard path applies those same defaults. Kept because it pins
			// that a chunked body is at least not an ERROR; see
			// TestAChunkedPresetBodyIsNotDiscarded for the half that bites.
			name: "chunked, carrying the advertised defaults",
			why: "the other half of the same -1: a chunked body that IS there. " +
				"A length-gated handler throws it away and applies whatever its " +
				"bodyless path applies, so this case and the one above have to " +
				"agree with the explicit reference for opposite reasons",
			mk: func() *http.Request {
				body := fmt.Sprintf(`{"musicTrack":%d,"micTrack":%d,"surroundTrack":%d,"cleanTrack":%d}`,
					advertised.MusicTrack, advertised.MicTrack,
					advertised.SurroundTrack, advertised.CleanTrack)
				r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
				r.ContentLength = -1
				r.Header.Set("Content-Type", "application/json")
				r.RemoteAddr = "203.0.113.5:44444"
				return r
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := apply(t, tc.name, tc.mk)
			if got.Routing.FilterComplex != want.Routing.FilterComplex {
				t.Errorf("POST %s (%s) compiled a different graph from the same "+
					"preset applied with the advertised defaults.\n"+
					"  advertised micTrack: %d\n"+
					"  explicit body compiled: %q  (%s)\n"+
					"  %s compiled:            %q  (%s)\n"+
					"why this case exists: %s",
					route, tc.name, advertised.MicTrack,
					want.Routing.FilterComplex, want.Routing.Summary,
					tc.name, got.Routing.FilterComplex, got.Routing.Summary,
					tc.why)
			}
			if len(got.Routing.Tracks) != len(want.Routing.Tracks) {
				t.Errorf("POST %s (%s) contributed tracks %v, want %v",
					route, tc.name, got.Routing.Tracks, want.Routing.Tracks)
				return
			}
			for i := range want.Routing.Tracks {
				if got.Routing.Tracks[i] != want.Routing.Tracks[i] {
					t.Errorf("POST %s (%s) contributed tracks %v, want %v -- the "+
						"advertised mic track is %d",
						route, tc.name, got.Routing.Tracks, want.Routing.Tracks,
						advertised.MicTrack)
					return
				}
			}
		})
	}

	// The other half of the fix, asserted so it cannot be lost: a body is a
	// FULL REPLACEMENT, not a patch over the defaults.
	//
	// The body below says nothing about the mic track, so the mic track is
	// zero -- which is what every partial-body client written against the old
	// handler already relies on. The obvious-looking alternative, decoding the
	// body ON TOP of the defaults, would make this same request select the
	// advertised mic track instead, silently changing the meaning of every
	// field a client has ever omitted. That is a bigger break than the one
	// being fixed, so it is pinned here rather than left to a comment.
	partial := apply(t, "a body that omits the mic track", func() *http.Request {
		return jsonRequest(t, http.MethodPost, route,
			map[string]int{"musicTrack": advertised.MusicTrack})
	})
	if len(partial.Routing.Tracks) != 1 || partial.Routing.Tracks[0] != 0 {
		t.Errorf("a body carrying only musicTrack contributed tracks %v, want [0]. "+
			"An omitted micTrack means zero; %v means the body was decoded over "+
			"the defaults (advertised micTrack %d) instead of replacing them.",
			partial.Routing.Tracks, partial.Routing.Tracks, advertised.MicTrack)
	}
}

// TestAReadTokenCompilesARealRoutingGraph pins the unusual half of the scope
// rules: POST /routing/compile is on readScopeWritePatterns, so a read-scoped
// token really can drive it. It is one of TWO -- POST /version/check is the
// other (api.go's readScopeWritePatterns) -- and this comment said "every other
// POST is 403" until a reviewer checked. Nearly-true is the shape that gets
// quoted; the list is two entries long and is worth naming rather than
// summarising.
//
// The route is already SWEPT by the ledger, but a sweep asserts a 2xx and the
// absence of planted credentials. Neither notices a handler that answers 200
// with an empty result, which is exactly what a broken compile looks like from
// the outside: the preview pane in the routing editor goes blank and nothing
// anywhere reports an error.
func TestAReadTokenCompilesARealRoutingGraph(t *testing.T) {
	h, _, sign := sourceServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	// Track 1 rather than 0, so the label in the graph cannot be confused with
	// a zero value that happened to be printed.
	const track = 1
	r := jsonRequest(t, http.MethodPost, "/api/v1/routing/compile", map[string]any{
		"profile": map[string]any{
			"mode":   "simple",
			"tracks": []map[string]any{{"track": track, "gain": 1, "enabled": true}},
		},
	})
	bearer(read)(r)
	w := do(t, h, r)
	apitailReached(t, w, "a read-scoped token", "POST /routing/compile")
	if w.Code != http.StatusOK {
		t.Fatalf("a read token got %d from POST /routing/compile; it is on "+
			"readScopeWritePatterns and must reach the handler: %s",
			w.Code, w.Body.String())
	}

	var out apitailCompiled
	decodeInto(t, w.Body.Bytes(), &out)

	wantLabel := "[0:a:" + strconv.Itoa(track) + "]"
	if !strings.Contains(out.Routing.FilterComplex, wantLabel) {
		t.Errorf("the compiled graph never maps the one enabled track. "+
			"want a %s in filterComplex, got %q -- a read token asked what its "+
			"routing compiles to and was handed a graph that ignores it",
			wantLabel, out.Routing.FilterComplex)
	}
	if out.Routing.OutLabel == "" {
		t.Error("the compiled graph carries no outLabel; there is nothing for a " +
			"destination to -map and the preview renders an empty string")
	}
	if len(out.Routing.Tracks) != 1 || out.Routing.Tracks[0] != track {
		t.Errorf("the compiled graph reports contributing tracks %v, want [%d] -- "+
			"the only track the profile enabled", out.Routing.Tracks, track)
	}
	if out.Routing.Summary == "" {
		t.Error("the compiled graph carries no summary; the destination card has " +
			"nothing to say about what it is sending")
	}
}

// TestAChunkedPresetBodyIsNotDiscarded is the half of the chunked case that can
// fail.
//
// The sub-case above it sends a chunked body carrying the ADVERTISED DEFAULTS,
// and a reviewer showed that cannot discriminate: revert the ContentLength fix
// while keeping the defaults fix, and the old gate throws that body away and
// applies exactly the defaults it was carrying. Same graph, green test. An
// assertion whose expected value equals what the bug produces is not an
// assertion.
//
// So this one sends a body the defaults CANNOT produce -- micTrack one below
// the advertised value -- and requires the compiled graph to be the one that
// body asks for. Under the length gate the body is discarded, the defaults
// apply, and the graph names the advertised track instead.
func TestAChunkedPresetBodyIsNotDiscarded(t *testing.T) {
	h, _, sign := sourceServer(t)
	advertised := apitailPresetDefaults(t, h, sign)
	if advertised.MicTrack == 0 {
		t.Fatalf("the catalogue advertises micTrack 0, so there is no lower "+
			"non-default value to distinguish a carried body from a discarded "+
			"one: %+v", advertised)
	}
	off := advertised
	off.MicTrack = advertised.MicTrack - 1

	route := "/api/v1/routing/presets/mic-only"
	body := fmt.Sprintf(`{"musicTrack":%d,"micTrack":%d,"surroundTrack":%d,"cleanTrack":%d}`,
		off.MusicTrack, off.MicTrack, off.SurroundTrack, off.CleanTrack)

	r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	r.ContentLength = -1 // every chunked request reports this, whatever it holds
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	sign(r)

	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "POST "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("chunked apply answered %d: %s", w.Code, w.Body.String())
	}
	var got apitailCompiled
	decodeInto(t, w.Body.Bytes(), &got)

	wantLabel := fmt.Sprintf("[0:a:%d]", off.MicTrack)
	if !strings.Contains(got.Routing.FilterComplex, wantLabel) {
		t.Errorf("a CHUNKED body asking for micTrack %d compiled %q, which does not "+
			"map %s. The body was discarded and the advertised defaults (micTrack "+
			"%d) applied instead -- which is the ContentLength > 0 gate, still in "+
			"place. A chunked request reports length -1 whatever it carries.",
			off.MicTrack, got.Routing.FilterComplex, wantLabel, advertised.MicTrack)
	}
	if len(got.Routing.Tracks) != 1 || got.Routing.Tracks[0] != off.MicTrack {
		t.Errorf("a chunked body asking for micTrack %d contributed tracks %v, want [%d]",
			off.MicTrack, got.Routing.Tracks, off.MicTrack)
	}
}
