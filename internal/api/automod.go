package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Automod's configuration rides inside Settings, so GET/PUT /settings already
// carries the matrix, the rules and the model config. Only three things need
// endpoints of their own:
//
//   - the RENDERED matrix, because which cells are available is derived from
//     platform capability and must never be stored. A stored availability could
//     disagree with reality, and the failure is an operator believing a channel
//     is protected when nothing is wired to it.
//   - model spend, which is state rather than configuration.
//   - the model's API key, sealed separately for the same reason the MQTT
//     broker password is: a secret in the settings blob is a secret returned by
//     GET /settings.

// handleAutomodMatrix returns every cell with its availability and reason.
func (s *Server) handleAutomodMatrix(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m := matrixFromSettings(settings.Automod)
	caps := automod.PlatformCaps{}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         m.Enabled,
		"platformEnabled": m.PlatformEnabled,
		"cells":           m.Cells(caps),
		"summary":         m.Summary(caps),
		// The vocabularies, so the UI renders rows and columns from the server's
		// list rather than a second copy that can drift out of step.
		"actions":   automod.Actions,
		"checkers":  automod.Checkers,
		"platforms": automod.Platforms,
	})
}

// handleAutomodStats reports model spend and health.
//
// FROM THE LIVE ENGINE. This returned a hardcoded automod.ModelStats{} behind a
// comment saying "until that wiring lands this reports an honest zero rather
// than inventing numbers" -- and the wiring had since landed:
// automod.Engine.ModelStats() exists and the hub holds the engine. A zero is
// only honest while it is unknowable; once it is knowable a zero is a claim
// that nothing has been spent, which is what the operator reads it as.
//
// Still a zero when no moderator is attached, which is the genuinely unknowable
// case and is what an install with automod off should report.
func (s *Server) handleAutomodStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.automodStats())
}

// automodStats reads the current generation's model counters, or the zero value
// when nothing is moderating.
func (s *Server) automodStats() automod.ModelStats {
	if s.chat == nil {
		return automod.ModelStats{}
	}
	m := s.chat.Moderator()
	if m == nil {
		return automod.ModelStats{}
	}
	// Called directly: chat.Moderator declares ModelStats, so a type assertion
	// here would be a branch that cannot fail and cannot be tested.
	return m.ModelStats()
}

// handlePutAutomodKey sets or clears the model API key.
//
// Its own endpoint, exactly like handlePutMQTTPassword: the key is sealed and
// never appears in the settings blob, so it cannot be returned by GET /settings
// and cannot reach a log through one.
func (s *Server) handlePutAutomodKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// An empty key clears the row, which is how the model checker is turned off
	// without leaving a credential behind. Sealing happens in the store, the
	// same shape as the MQTT broker password.
	key := strings.TrimSpace(req.Key)
	if err := s.store.PutAutomodKey(s.box, key); err != nil {
		writeStoreError(w, err)
		return
	}
	// Same reasoning as the MQTT password: sealed straight into the store, so
	// PUT /settings never sees it and changedSections cannot report it. The
	// section name travels and the key never does.
	// RE-APPLIED, OR THE KEY DOES NOTHING UNTIL THE NEXT RESTART. ApplyAutomod
	// reads the sealed key when it builds the model checker, and nothing here
	// called it -- so an operator who pasted a key saw "configured", and the
	// model checker went on running without one (or kept using the old one after
	// a rotation). The engine is rebuilt on every settings save for exactly this
	// reason; setting the key is a settings save that forgot to say so.
	if set, err := s.store.GetSettings(); err != nil {
		s.log.Warn("automod key stored, but settings could not be re-read to "+
			"rebuild the model checker; it will pick the key up on restart", "err", err)
	} else {
		ApplyAutomod(s.chat, s.store, s.box, s.log, set.Automod, s.automodBudget)
	}

	s.publishAudit(auditSettingsChanged([]string{"automod"}, s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"hasApiKey": key != ""})
}

// matrixFromSettings converts the stored shape into the engine's.
//
// Two shapes rather than one because they answer different questions: the
// stored form is what round-trips through the settings blob, and the engine's
// is what the checkers consult. Keeping them apart means adding a checker does
// not change the database schema.
func matrixFromSettings(a db.AutomodSettings) automod.Matrix {
	m := automod.Matrix{
		Enabled:         a.Enabled,
		PlatformEnabled: map[db.Platform]bool{},
		On:              map[string]bool{},
	}
	for p, on := range a.PlatformEnabled {
		m.PlatformEnabled[p] = on
	}
	for k, on := range a.On {
		if !on {
			continue
		}
		// A key from a newer version is DROPPED rather than kept, so an
		// unrecognised action cannot be silently treated as one we know.
		key, err := automod.ParseKey(k)
		if err != nil ||
			!automod.KnownPlatform(key.Platform) ||
			!automod.KnownActions(key.Action) ||
			!automod.KnownChecker(key.Checker) {
			continue
		}
		m.On[k] = true
	}
	return m
}

// rulesFromSettings compiles the stored rules.
//
// A bad pattern fails the whole set rather than being skipped: silently
// dropping one leaves an operator believing a protection exists when it does
// not, which is the same silent-failure shape the capability gate prevents.
func rulesFromSettings(a db.AutomodSettings) (*automod.RuleSet, error) {
	rules := make([]automod.Rule, 0, len(a.Rules))
	for _, r := range a.Rules {
		rules = append(rules, automod.Rule{
			ID:             r.ID,
			Name:           r.Name,
			Enabled:        r.Enabled,
			Pattern:        r.Pattern,
			Action:         automod.Action(r.Action),
			TimeoutSeconds: r.TimeoutSeconds,
		})
	}
	return automod.NewRuleSet(rules)
}

// historyFromSettings converts the stored bounds.
func historyFromSettings(a db.AutomodSettings) automod.HistoryLimits {
	h := a.History
	lim := automod.DefaultHistoryLimits()
	if h.WindowSeconds > 0 {
		lim.Window = time.Duration(h.WindowSeconds) * time.Second
	}
	if h.MaxMessages > 0 {
		lim.MaxMessages = h.MaxMessages
	}
	if h.MaxRepeats > 0 {
		lim.MaxRepeats = h.MaxRepeats
	}
	if h.MaxLinks > 0 {
		lim.MaxLinks = h.MaxLinks
	}
	if h.MaxMentionsPerMessage > 0 {
		lim.MaxMentionsPerMessage = h.MaxMentionsPerMessage
	}
	if h.MinLengthForCaps > 0 {
		lim.MinLengthForCaps = h.MinLengthForCaps
	}
	if h.MaxCapsRatio > 0 {
		lim.MaxCapsRatio = h.MaxCapsRatio
	}
	if h.Action != "" && automod.KnownActions(automod.Action(h.Action)) {
		lim.Action = automod.Action(h.Action)
	}
	if h.TimeoutSeconds > 0 {
		lim.TimeoutSeconds = h.TimeoutSeconds
	}
	if h.RetainPerAuthor > 0 {
		lim.Retain = h.RetainPerAuthor
	}
	if h.IdleEvictionSeconds > 0 {
		lim.IdleEviction = time.Duration(h.IdleEvictionSeconds) * time.Second
	}
	return lim
}

// ApplyAutomod builds the engine from stored settings and attaches it.
//
// Called at startup and again on every settings save, for the same reason
// ApplyChatRetention is: a value that works until the first restart and then
// silently reverts is worse than one that never worked, because the operator
// has already stopped checking.
//
// Every failure here DEGRADES rather than stops. A build that cannot compile
// one rule pattern must still serve chat, still flag, and still run the history
// detectors — refusing to moderate at all because one regex is malformed is the
// wrong trade in both directions.
// modelConfigFrom turns the stored model settings into the engine's config.
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
// while internal/engine/reload.go:301 classified timeoutForBan as ClassLive
// through this function, which is a written promise that saving it takes
// effect.
func modelConfigFrom(a db.AutomodSettings) (automod.ModelConfig, error) {
	var confidenceProblem error
	cfg := automod.DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = a.Model.Endpoint
	cfg.Model = a.Model.Model
	cfg.MaxCallsPerHour = a.Model.MaxCallsPerHour
	// PARSED, NOT COPIED, and the compiler now insists. This line used to be a
	// bare assignment sitting directly above a neighbour that DID carry a
	// `> 0` guard -- the hazard understood on one line and not the one above
	// it. A stored 0 removed the confidence floor entirely and every model
	// opinion acted; a stored 80, from reading the scale as a percentage, sat
	// above every verdict the model can return and silently retired the
	// checker. Out of range keeps DefaultModelConfig's 0.8, which is a floor
	// that works, rather than either failure.
	if c, err := automod.ParseConfidence(a.Model.MinConfidence); err == nil {
		cfg.MinConfidence = c
	} else {
		confidenceProblem = err
	}
	cfg.Instruction = a.Model.Instruction
	if a.Model.TimeoutSeconds > 0 {
		cfg.Timeout = time.Duration(a.Model.TimeoutSeconds) * time.Second
	}
	// Left at DefaultModelConfig's value when unset rather than zeroed: zero is
	// a PERMANENT ban at every adapter, which is the same trap the rule path
	// carried until it was fixed alongside this.
	if a.Model.TimeoutForBan > 0 {
		cfg.TimeoutSeconds = a.Model.TimeoutForBan
	}
	if automod.KnownActions(automod.Action(a.Model.Action)) {
		cfg.Action = automod.Action(a.Model.Action)
	}
	return cfg, confidenceProblem
}

func ApplyAutomod(hub *chat.Hub, store *db.DB, box *secrets.Box, log *slog.Logger, a db.AutomodSettings, budget *automod.Budget) {
	if hub == nil {
		return
	}
	if !a.Enabled {
		// Detaching rather than leaving a disabled engine attached: the global
		// switch should stop the work, not merely stop the actions.
		hub.SetModerator(nil)
		return
	}

	rules, err := rulesFromSettings(a)
	if err != nil {
		// Named loudly. A rule that does not compile is a protection the
		// operator believes they have.
		log.Error("automod rules did not compile; running without them", "err", err)
		rules = nil
	}

	var model *automod.Model
	if a.Model.Enabled {
		cfg, err := modelConfigFrom(a)
		if err != nil {
			// Named loudly rather than swallowed, for the same reason a rule
			// that does not compile is: a confidence floor an operator set and
			// this server ignored is a moderation posture they believe they
			// have. The checker runs on the default rather than not at all.
			log.Error("automod model confidence floor rejected; using the default", "err", err)
		}
		if key, err := store.GetAutomodKey(box); err == nil {
			cfg.APIKey = key
		} else {
			// Fail open, consistent with the connector itself: a key that
			// cannot be read means the model checker cannot run, not that chat
			// stops.
			log.Warn("automod model key unreadable; the model checker will fail open", "err", err)
		}
		// The SAME budget every time this runs. Rebuilding the connector on a
		// settings save is correct; refilling its hourly allowance was not.
		model = automod.NewModel(cfg, budget)
	}

	engine := automod.New(
		matrixFromSettings(a),
		automod.PlatformCaps{},
		rules,
		automod.NewHistory(historyFromSettings(a)),
		model,
	)
	hub.SetModerator(engine)
}
