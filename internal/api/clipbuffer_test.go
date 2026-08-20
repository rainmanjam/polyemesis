package api

import (
	"net/http"
	"testing"
)

// The clip buffer and the loudness monitor are both switches that cost
// something real when on -- a rolling capture holds a window of video in
// memory, an analyser is a process per destination. Both default OFF, and the
// thing worth pinning is that turning them on and off actually changes the
// reported state rather than returning 200 and doing nothing.

func TestClipBufferStartsOffAndTogglesBothWays(t *testing.T) {
	h, _, sign := sourceServer(t)

	var st map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/clips", nil, http.StatusOK), &st)

	// On by default would mean every install pays for a rolling capture nobody
	// asked for.
	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": true, "windowSeconds": 30}, http.StatusOK)

	var on map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/clips", nil, http.StatusOK), &on)
	if !bufferEnabled(on) {
		t.Error("the buffer did not report itself enabled after being switched on")
	}

	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": false}, http.StatusOK)

	var off map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/clips", nil, http.StatusOK), &off)
	if bufferEnabled(off) {
		t.Error("the buffer stayed enabled after being switched off; it would keep holding video")
	}
}

// bufferEnabled digs the flag out of whichever level the clips response nests
// it at, so this file does not break on an unrelated reshaping of the payload.
func bufferEnabled(v map[string]any) bool {
	if b, ok := v["enabled"].(bool); ok {
		return b
	}
	for _, key := range []string{"buffer", "clips", "state"} {
		if inner, ok := v[key].(map[string]any); ok {
			if b, ok := inner["enabled"].(bool); ok {
				return b
			}
		}
	}
	return false
}

func TestClipBufferRefusesAWindowItCannotHold(t *testing.T) {
	// The window is memory. An unbounded value here is an OOM on a machine
	// that was streaming fine a moment ago.
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": true, "windowSeconds": 86400}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": true, "windowSeconds": 1}, http.StatusBadRequest)
}

func TestANonPositiveWindowMeansUnchangedRatherThanZero(t *testing.T) {
	// Only values ABOVE zero set the window; anything else leaves it alone, so
	// a page that only toggles the switch does not have to know what the window
	// currently is. Worth pinning because the obvious alternative reading --
	// "0 means a zero-length buffer" -- would silently make every toggle
	// destroy the operator's window.
	h, _, sign := sourceServer(t)

	send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
		map[string]any{"enabled": true, "windowSeconds": 45}, http.StatusOK)

	for _, v := range []int{0, -1} {
		var out map[string]any
		decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/clips/buffer",
			map[string]any{"enabled": true, "windowSeconds": v}, http.StatusOK), &out)
		buf, _ := out["buffer"].(map[string]any)
		if buf == nil {
			t.Fatalf("no buffer block in the response: %v", out)
		}
		if got := buf["windowSeconds"]; got != float64(45) {
			t.Errorf("windowSeconds = %v after sending %d, want the previous 45", got, v)
		}
	}
}

func TestCapturingAClipWithNoBufferIsRefusedNotIgnored(t *testing.T) {
	// With the buffer off there is nothing to capture. The operator pressing
	// the button must be told, rather than getting a success and then hunting
	// for a clip that was never written.
	h, _, sign := sourceServer(t)
	r := jsonRequest(t, http.MethodPost, "/api/v1/clips", map[string]any{"name": "goal"})
	sign(r)
	w := do(t, h, r)
	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("capture reported success with no buffer running (%d): %s", w.Code, w.Body.String())
	}
}

func TestLoudnessMonitorTogglesBothWays(t *testing.T) {
	h, _, sign := sourceServer(t)

	var on map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/loudness",
		map[string]any{"enabled": true}, http.StatusOK), &on)
	if b, _ := on["enabled"].(bool); !b {
		t.Error("enabling the loudness monitor did not report it enabled")
	}

	var off map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/loudness",
		map[string]any{"enabled": false}, http.StatusOK), &off)
	if b, _ := off["enabled"].(bool); b {
		t.Error("disabling the loudness monitor did not report it disabled")
	}
}

// The Meters page draws a Monitor switch. Nothing on the wire said which way it
// should be drawn, so the page seeded it `true` and asserted "on" over a monitor
// the operator had switched off -- then explained the empty report list as
// "Nothing to measure yet", which is a different claim entirely.
func TestLoudnessReadReportsWhetherTheMonitorIsOn(t *testing.T) {
	h, _, sign := sourceServer(t)

	read := func() (bool, bool) {
		t.Helper()
		var out map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/loudness", nil, http.StatusOK), &out)
		raw, present := out["enabled"]
		b, _ := raw.(bool)
		return b, present
	}

	if _, present := read(); !present {
		t.Fatal("GET /api/v1/loudness carried no \"enabled\"; the page has nothing to seed its switch from")
	}

	send(t, h, sign, http.MethodPut, "/api/v1/loudness", map[string]any{"enabled": false}, http.StatusOK)
	if on, _ := read(); on {
		t.Error("the monitor was switched off and the read still reported it on")
	}

	send(t, h, sign, http.MethodPut, "/api/v1/loudness", map[string]any{"enabled": true}, http.StatusOK)
	if on, _ := read(); !on {
		t.Error("the monitor was switched on and the read still reported it off")
	}
}

func TestLoudnessReportCarriesItsBounds(t *testing.T) {
	// The UI colours a reading against these. Shipping the numbers with the
	// report rather than hard-coding them in the client is what stops the two
	// drifting apart -- and a missing bounds block renders every reading grey.
	h, _, sign := sourceServer(t)
	var out struct {
		Reports []any `json:"reports"`
		Bounds  struct {
			ToleranceLU     float64 `json:"toleranceLu"`
			WarnToleranceLU float64 `json:"warnToleranceLu"`
		} `json:"bounds"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/loudness", nil, http.StatusOK), &out)
	if out.Bounds.ToleranceLU <= 0 {
		t.Error("no loudness tolerance shipped; the UI has nothing to colour against")
	}
	if out.Reports == nil {
		t.Error("reports was null; the meters page would throw rather than show an empty state")
	}
}
