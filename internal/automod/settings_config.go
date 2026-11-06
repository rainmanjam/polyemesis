package automod

import (
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The stored settings -> connector translation. It lives here, beside the
// connector it configures, because two callers outside the HTTP layer need it:
// the settings handlers and the highlight scan worker (highlights.ModelFrom).

// ProtocolOf is a stored block's protocol. Anything but "jev" is the
// OpenAI-compatible connector, which is what an empty protocol has always
// meant (db.AutomodSettings.UnmarshalJSON).
func ProtocolOf(m db.AutomodModel) Protocol {
	if m.Protocol == db.AutomodProtocolJev {
		return ProtocolJev
	}
	return ProtocolOpenAI
}

// ModelConfigFor turns one checker's stored model settings into the engine's
// config. conn is the checker's own block -- endpoint, model, budget, timeout,
// floor, preset -- and shared is the primary's, which carries the policy both
// checkers apply: the Instruction, the legacy Action, TimeoutForBan and the
// category table.
//
// EXTRACTED SO IT CAN BE TESTED WITHOUT A STORE, because two fields here were
// silently not wired and nothing could see it. The stored shape and the
// engine's are deliberately different -- see matrixFromSettings for the same
// reasoning -- and that gap is exactly where a field gets forgotten.
//
// THE TWO TIMEOUTS ARE NOT THE SAME TIMEOUT, and conflating them is what went
// wrong. Model.TimeoutSeconds is how long to wait for the model to ANSWER;
// Model.TimeoutForBan is how long a viewer the model flags stays timed out.
// Only the first was applied, so every model-decided timeout ran at the
// built-in default and the operator's configured duration did nothing --
// while the reload table in internal/engine/reload.go classified timeoutForBan as ClassLive
// through this function, which is a written promise that saving it takes
// effect.
func ModelConfigFor(conn, shared db.AutomodModel) (ModelConfig, error) {
	var confidenceProblem error
	cfg := DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = conn.Endpoint
	cfg.Model = conn.Model
	cfg.MaxCallsPerHour = conn.MaxCallsPerHour
	// The preset's wire details, from the server's table by ID -- never from
	// the document, so a client cannot unlock a local preset's flag-only
	// lock or send a JSON mode the preset does not have.
	preset := ResolvePreset(ProtocolOpenAI, conn.Preset)
	cfg.Preset = preset.ID
	cfg.JSONMode = preset.JSONMode
	cfg.ReasoningEffort = preset.ReasoningEffort
	cfg.FlagOnly = preset.Local
	cfg.CategoryActions = categoryActionsFrom(shared)
	// PARSED, NOT COPIED, and the compiler now insists. This line used to be a
	// bare assignment sitting directly above a neighbour that DID carry a
	// `> 0` guard -- the hazard understood on one line and not the one above
	// it. A stored 0 removed the confidence floor entirely and every model
	// opinion acted; a stored 80, from reading the scale as a percentage, sat
	// above every verdict the model can return and silently retired the
	// checker. Out of range keeps DefaultModelConfig's 0.8, which is a floor
	// that works, rather than either failure.
	if c, err := ParseConfidence(conn.MinConfidence); err == nil {
		cfg.MinConfidence = c
	} else {
		confidenceProblem = err
	}
	cfg.Instruction = shared.Instruction
	if conn.TimeoutSeconds > 0 {
		cfg.Timeout = time.Duration(conn.TimeoutSeconds) * time.Second
	}
	// Left at DefaultModelConfig's value when unset rather than zeroed: zero is
	// a PERMANENT ban at every adapter, which is the same trap the rule path
	// carried until it was fixed alongside this.
	if shared.TimeoutForBan > 0 {
		cfg.TimeoutSeconds = shared.TimeoutForBan
	}
	if KnownActions(Action(shared.Action)) {
		cfg.Action = Action(shared.Action)
	}
	return cfg, confidenceProblem
}

// JevConfigFor is ModelConfigFor for a Jev-protocol checker. The same split
// between the checker's own connection and the shared policy, read through
// ModelConfigFor so the two connectors cannot come to different conclusions
// about a floor, a timeout or an action.
func JevConfigFor(conn, shared db.AutomodModel) (JevConfig, error) {
	m, err := ModelConfigFor(conn, shared)
	preset := ResolvePreset(ProtocolJev, conn.Preset)
	return JevConfig{
		Enabled:         true,
		Endpoint:        m.Endpoint,
		Model:           m.Model,
		Timeout:         m.Timeout,
		MaxCallsPerHour: m.MaxCallsPerHour,
		Action:          m.Action,
		TimeoutSeconds:  m.TimeoutSeconds,
		MinConfidence:   m.MinConfidence,
		Instruction:     m.Instruction,
		Preset:          preset.ID,
		Compact:         preset.Compact,
		FlagOnly:        preset.Local,
		CategoryActions: m.CategoryActions,
	}, err
}

// categoryActionsFrom parses the stored table, dropping anything this build
// does not recognise rather than guessing at it. The settings handler refuses
// such entries on save (automodProviderProblems); this is for a blob written
// by a newer version.
func categoryActionsFrom(shared db.AutomodModel) map[Category]Action {
	if len(shared.CategoryActions) == 0 {
		return nil
	}
	out := map[Category]Action{}
	for k, v := range shared.CategoryActions {
		c, ok := ParseModelCategory(k)
		if ok && KnownActions(Action(v)) {
			out[c] = Action(v)
		}
	}
	return out
}
