package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// automodTestRequest is the form a Test button sends: the connection the
// operator is LOOKING AT, saved or not. Testing what is on screen rather than
// what is stored is the point -- the switch it unlocks is for that
// connection, and making the operator save an untested checker first would
// mean storing exactly the thing the test exists to stop.
//
// The key is NOT in it. The test reads the checker's sealed key, so a key
// travels once, through PUT /settings/automod-key, and never back out.
type automodTestRequest struct {
	Checker        string  `json:"checker"`
	Protocol       string  `json:"protocol"`
	Preset         string  `json:"preset"`
	Endpoint       string  `json:"endpoint"`
	Model          string  `json:"model"`
	TimeoutSeconds int     `json:"timeoutSeconds"`
	MinConfidence  float64 `json:"minConfidence"`
	Instruction    string  `json:"instruction"`
}

// automodTestMaxTimeout bounds one probe call. A Test that may hold a request
// open for minutes is a slow-loris the operator aimed at themselves.
const automodTestMaxTimeout = 30

// automodTestResponse is the probe report plus whether the pass was recorded.
type automodTestResponse struct {
	automod.ProbeResult
	Checker automod.Slot `json:"checker"`
	// Tested is true when this connection may now be enabled.
	Tested bool `json:"tested"`
}

// handleAutomodTest runs the three canned probe lines against one checker's
// connection and, when they pass, records the pass that lets it be enabled.
//
// Every string in the response is the connector's own: fixed sentences and a
// status code, with the endpoint run through redactEndpoint. No upstream body
// is ever relayed -- an OpenAI 401 body carries a masked copy of the key, and
// a Jev 422 body previews the request.
func (s *Server) handleAutomodTest(w http.ResponseWriter, r *http.Request) {
	var req automodTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	slot, ok := parseSlot(req.Checker)
	if !ok {
		writeError(w, http.StatusBadRequest, `checker must be "primary" or "secondary"`)
		return
	}
	proto := automod.Protocol(req.Protocol)
	if !automod.KnownProtocol(proto) {
		writeError(w, http.StatusBadRequest, `protocol must be "jev" or "openai"`)
		return
	}
	if req.Preset != "" {
		if p, ok := automod.PresetByID(req.Preset); !ok || p.Protocol != proto {
			writeError(w, http.StatusBadRequest, "preset is not one of this protocol's presets")
			return
		}
	}
	// HOSTILE INPUT, even from an admin: an absolute http(s) URL and nothing
	// else. Private addresses are allowed on purpose -- see ModelConfig.Endpoint
	// for why a local model is the recommended deployment, not an SSRF.
	u, err := url.Parse(strings.TrimSpace(req.Endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "endpoint must be an absolute http or https URL")
		return
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > automodTestMaxTimeout {
		writeError(w, http.StatusBadRequest, "timeoutSeconds must be between 1 and 30")
		return
	}
	if req.MinConfidence != 0 {
		if _, err := automod.ParseConfidence(req.MinConfidence); err != nil {
			writeError(w, http.StatusBadRequest, "minConfidence must be between 0.01 and 1")
			return
		}
	}

	mu := &s.automodTestPrimary
	if slot == automod.SlotSecondary {
		mu = &s.automodTestSecondary
	}
	if !mu.TryLock() {
		writeError(w, http.StatusTooManyRequests, "a test of this checker is already running")
		return
	}
	defer mu.Unlock()

	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	conn := db.AutomodModel{
		Enabled: true, Protocol: string(proto), Preset: req.Preset,
		Endpoint: strings.TrimSpace(req.Endpoint), Model: strings.TrimSpace(req.Model),
		TimeoutSeconds: req.TimeoutSeconds, MinConfidence: req.MinConfidence,
	}
	if conn.MinConfidence == 0 {
		conn.MinConfidence = 0.8
	}
	// The shared policy is the stored primary's, with the instruction on
	// screen when there is one: the test should ask the question the checker
	// will be asked.
	shared := settings.Automod.Model
	if strings.TrimSpace(req.Instruction) != "" {
		shared.Instruction = req.Instruction
	}
	key, err := s.store.GetAutomodKeyFor(s.box, string(slot))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the checker's key could not be read; set it again")
		return
	}
	// No budget: a spent hourly allowance must not make a working endpoint
	// look broken, and the per-checker lock above bounds what a test costs.
	checker := newChecker(conn, shared, key, nil, nil)

	ctx, cancel := context.WithTimeout(r.Context(),
		time.Duration(len(automod.ProbeCases)*(automodTestMaxTimeout+1))*time.Second)
	defer cancel()
	res := automod.Probe(ctx, checker)

	out := automodTestResponse{ProbeResult: res, Checker: slot}
	if res.Passed {
		if err := s.store.PutAutomodTestPass(string(slot), checkerFingerprint(conn)); err != nil {
			writeStoreError(w, err)
			return
		}
		out.Tested = true
	}
	writeJSON(w, http.StatusOK, out)
}
