package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
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
		// Counted over the cells that can FIRE, not the cells that are stored
		// on. A cell whose checker is not configured is kept -- turning the
		// model off for an evening must not forget which cells it was allowed
		// -- but it decides nothing, because automod.New is handed a nil
		// checker for it. Counting it anyway is what put "irreversible action
		// armed" in the banner over a matrix the console drew as "nothing
		// automatic". See checkerConfigured.
		"summary": liveMatrix(m, settings.Automod).Summary(caps),
		// The vocabularies, so the UI renders rows and columns from the server's
		// list rather than a second copy that can drift out of step.
		"actions":   automod.Actions,
		"checkers":  automod.Checkers,
		"platforms": automod.Platforms,
		// And the model-checker vocabulary, for the same reason: the preset
		// table carries each provider's privacy class and the local
		// flag-only lock, and a UI copy of it could disagree with the one the
		// server enforces.
		"protocols":       automod.Protocols,
		"presets":         automod.Presets(),
		"modelCategories": automod.ModelCategories(),
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

// handlePutAutomodKey sets or clears a model checker's API key.
//
// Its own endpoint, exactly like handlePutMQTTPassword: the key is sealed and
// never appears in the settings blob, so it cannot be returned by GET /settings
// and cannot reach a log through one.
//
// `checker` picks which: "primary" (or absent, which is what every client
// written before there were two sends) or "secondary". Each is sealed in its
// own row, and setting either forgets that checker's Test pass -- see
// db.PutAutomodKeyFor.
func (s *Server) handlePutAutomodKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key     string `json:"key"`
		Checker string `json:"checker"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	slot, ok := parseSlot(req.Checker)
	if !ok {
		writeError(w, http.StatusBadRequest, `checker must be "primary" or "secondary"`)
		return
	}
	// An empty key clears the row, which is how the model checker is turned off
	// without leaving a credential behind. Sealing happens in the store, the
	// same shape as the MQTT broker password.
	key := strings.TrimSpace(req.Key)
	if err := s.store.PutAutomodKeyFor(s.box, string(slot), key); err != nil {
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
		ApplyAutomod(s.chat, s.store, s.box, s.log, set.Automod, s.automodBudget, s.automodSecondaryBudget)
	}

	// A key set while debug recording is on is a literal the recording's
	// scrub set has never held. Rebuild it now rather than at the next toggle.
	s.refreshDiagSecrets()

	s.publishAudit(auditSettingsChanged([]string{"automod"}, s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]any{"hasApiKey": key != "", "checker": slot})
}

// parseSlot reads a checker name from a request. Empty means the primary.
func parseSlot(v string) (automod.Slot, bool) {
	switch automod.Slot(v) {
	case "", automod.SlotPrimary:
		return automod.SlotPrimary, true
	case automod.SlotSecondary:
		return automod.SlotSecondary, true
	}
	return "", false
}

// slotSettings is one checker's stored block.
func slotSettings(a db.AutomodSettings, slot automod.Slot) db.AutomodModel {
	if slot == automod.SlotSecondary {
		return a.Secondary
	}
	return a.Model
}

// protocolOf is a stored block's protocol. Anything but "jev" is the
// OpenAI-compatible connector, which is what an empty protocol has always
// meant (db.AutomodSettings.UnmarshalJSON).
func protocolOf(m db.AutomodModel) automod.Protocol {
	if m.Protocol == db.AutomodProtocolJev {
		return automod.ProtocolJev
	}
	return automod.ProtocolOpenAI
}

// checkerFingerprint identifies a checker's CONNECTION: protocol, preset,
// endpoint and model. A Test pass is bound to it, so changing any of the four
// -- pointing a tested checker somewhere new -- needs a new test. The key is
// not in it; setting a key deletes the pass instead (db.PutAutomodKeyFor), so
// no digest of a credential is ever stored.
func checkerFingerprint(m db.AutomodModel) string {
	h := sha256.New()
	for _, part := range []string{string(protocolOf(m)), m.Preset, strings.TrimSpace(m.Endpoint), strings.TrimSpace(m.Model)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// checkerTested reports whether a checker's stored connection has a matching
// Test pass.
func (s *Server) checkerTested(slot automod.Slot, m db.AutomodModel) bool {
	fp, err := s.store.AutomodTestPass(string(slot))
	return err == nil && fp != "" && fp == checkerFingerprint(m)
}

// newlyEnabledUntested refuses a save that turns a model checker ON -- or
// moves an enabled one to a new connection -- without a Test pass for exactly
// that connection.
//
// SERVER-SIDE, because the console's disabled switch is only a courtesy: every
// field of the settings document is writable through PUT /settings, so a
// rule only the UI kept is a rule any API client skips. The pass lives in its
// own table for the same reason.
//
// Scoped to a CHANGE, like newlyArmedOnUnconfigured: an install upgrading with
// a checker already enabled was enabled under the old rules, and refusing
// every unrelated save until somebody found the Test button would be its own
// outage. Switching a checker off is never refused.
func newlyEnabledUntested(stored, sent db.AutomodSettings, pass func(slot string) (string, error)) error {
	var bad []string
	for _, slot := range []automod.Slot{automod.SlotPrimary, automod.SlotSecondary} {
		was, now := slotSettings(stored, slot), slotSettings(sent, slot)
		if !now.Enabled {
			continue
		}
		fp := checkerFingerprint(now)
		if was.Enabled && checkerFingerprint(was) == fp {
			continue
		}
		got, err := pass(string(slot))
		if err != nil {
			return err
		}
		if got != fp {
			bad = append(bad, string(slot))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return badRequestError{"automod: run Test on the " + strings.Join(bad, " and ") +
		" model checker with this protocol, preset, endpoint, model and key before enabling it"}
}

// automodProviderProblems refuses a preset that is not this protocol's, and a
// category table naming a category the model cannot answer with or an action
// this build does not know. Here rather than in Settings.Validate because the
// vocabularies live in internal/automod, which db cannot import.
func automodProviderProblems(a db.AutomodSettings) error {
	var probs []string
	for _, slot := range []automod.Slot{automod.SlotPrimary, automod.SlotSecondary} {
		m := slotSettings(a, slot)
		if m.Preset == "" {
			continue
		}
		p, ok := automod.PresetByID(m.Preset)
		if !ok || p.Protocol != protocolOf(m) {
			probs = append(probs, fmt.Sprintf("%s preset %q is not a %s preset", slot, m.Preset, protocolOf(m)))
		}
	}
	keys := make([]string, 0, len(a.Model.CategoryActions))
	for k := range a.Model.CategoryActions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := automod.ParseModelCategory(k); !ok {
			probs = append(probs, fmt.Sprintf("category %q is not one the model answers with", k))
		}
		if !automod.KnownActions(automod.Action(a.Model.CategoryActions[k])) {
			probs = append(probs, fmt.Sprintf("category %q asks for unknown action %q", k, a.Model.CategoryActions[k]))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return badRequestError{"automod: " + strings.Join(probs, "; ")}
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

// checkerConfigured reports whether a checker exists at all in the engine
// ApplyAutomod would build from these settings, and in the operator's terms why
// not when it does not.
//
// automod.New takes each checker as a pointer and a nil one contributes
// nothing. Two of the three are nil by default: rules until a usable rule is
// written, the model unless it is enabled. A cell in either column can be
// stored on and still never fire -- until somebody configures the checker,
// when it goes live as an action nobody knowingly chose that day.
//
// THE CONSOLE HAS THE SAME RULE, in ui/src/lib/automodConfig.ts checkerReady,
// which renders these columns inert. The two must agree: where they did not,
// the console said "nothing automatic" and the banner said a ban was armed.
func checkerConfigured(a db.AutomodSettings, c automod.Checker) (bool, string) {
	switch c {
	case automod.CheckerRules:
		// A DISABLED rule is no rule: Rule.Match returns false for one, so the
		// checker would be built and find nothing.
		for _, r := range a.Rules {
			if r.Enabled && strings.TrimSpace(r.Pattern) != "" {
				return true, ""
			}
		}
		return false, "no enabled rule is written"
	case automod.CheckerModel:
		// EITHER checker makes the column live: the matrix has one model
		// column, and a secondary running alone (its primary switched off)
		// still decides. See automod.Chain.
		if !a.Model.Enabled && !a.Secondary.Enabled {
			return false, "the model checker is switched off"
		}
		// modelConfigFrom copies an empty endpoint over the default, so the
		// checker is built and every call is a POST to "" that fails open.
		for _, m := range []db.AutomodModel{a.Model, a.Secondary} {
			if m.Enabled && strings.TrimSpace(m.Endpoint) != "" {
				return true, ""
			}
		}
		return false, "the model checker has no endpoint"
	}
	// History is built from DefaultHistoryLimits on every save and is never
	// nil. A checker this build does not know is dropped by matrixFromSettings
	// before it could matter.
	return true, ""
}

// liveMatrix is m without the cells whose checker is not configured -- the
// cells that can actually decide something.
func liveMatrix(m automod.Matrix, a db.AutomodSettings) automod.Matrix {
	live := m
	live.On = map[string]bool{}
	for k, on := range m.On {
		key, err := automod.ParseKey(k)
		if err != nil || !on {
			continue
		}
		if ok, _ := checkerConfigured(a, key.Checker); ok {
			live.On[k] = true
		}
	}
	return live
}

// newlyArmedOnUnconfigured refuses a save that switches ON a cell whose checker
// is not configured by that same save.
//
// Refusal rather than a warning, because the console never offers the switch
// (the column is inert), so the only way to reach this is the API -- and a 200
// there stored an armed ban the console would then hide.
//
// Scoped to cells this save TURNS ON, for the reason the rule-compile check is
// scoped to a change. A cell already stored on over a checker that has since
// been switched off is kept, and does nothing while it stays off (liveMatrix);
// refusing it would make every unrelated save fail until the operator found a
// column the console draws inert. Configuring the checker and arming the cell
// in one save is allowed, because readiness is judged on the document being
// saved.
func newlyArmedOnUnconfigured(stored map[string]bool, sent db.AutomodSettings) error {
	var bad []string
	for k, on := range sent.On {
		if !on || stored[k] {
			continue
		}
		key, err := automod.ParseKey(k)
		if err != nil || !automod.KnownPlatform(key.Platform) ||
			!automod.KnownActions(key.Action) || !automod.KnownChecker(key.Checker) {
			// matrixFromSettings drops it; there is nothing to arm.
			continue
		}
		if ok, why := checkerConfigured(sent, key.Checker); !ok {
			bad = append(bad, k+" ("+why+")")
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return badRequestError{"automod: cannot arm a cell whose checker is not configured: " +
		strings.Join(bad, ", ") + "; configure the checker in the same save, or leave the cell off"}
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
	return modelConfigFor(a.Model, a.Model)
}

// modelConfigFor is modelConfigFrom for either checker. conn is the checker's
// own block -- endpoint, model, budget, timeout, floor, preset -- and shared is
// the primary's, which carries the policy both checkers apply: the
// Instruction, the legacy Action, TimeoutForBan and the category table.
func modelConfigFor(conn, shared db.AutomodModel) (automod.ModelConfig, error) {
	var confidenceProblem error
	cfg := automod.DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = conn.Endpoint
	cfg.Model = conn.Model
	cfg.MaxCallsPerHour = conn.MaxCallsPerHour
	// The preset's wire details, from the server's table by ID -- never from
	// the document, so a client cannot unlock a local preset's flag-only
	// lock or send a JSON mode the preset does not have.
	preset := automod.ResolvePreset(automod.ProtocolOpenAI, conn.Preset)
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
	if c, err := automod.ParseConfidence(conn.MinConfidence); err == nil {
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
	if automod.KnownActions(automod.Action(shared.Action)) {
		cfg.Action = automod.Action(shared.Action)
	}
	return cfg, confidenceProblem
}

// jevConfigFor is modelConfigFor for a Jev-protocol checker. The same split
// between the checker's own connection and the shared policy, read through
// modelConfigFor so the two connectors cannot come to different conclusions
// about a floor, a timeout or an action.
func jevConfigFor(conn, shared db.AutomodModel) (automod.JevConfig, error) {
	m, err := modelConfigFor(conn, shared)
	preset := automod.ResolvePreset(automod.ProtocolJev, conn.Preset)
	return automod.JevConfig{
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
func categoryActionsFrom(shared db.AutomodModel) map[automod.Category]automod.Action {
	if len(shared.CategoryActions) == 0 {
		return nil
	}
	out := map[automod.Category]automod.Action{}
	for k, v := range shared.CategoryActions {
		c, ok := automod.ParseModelCategory(k)
		if ok && automod.KnownActions(automod.Action(v)) {
			out[c] = automod.Action(v)
		}
	}
	return out
}

// buildChecker is one slot's connector, or nil when it is switched off.
func buildChecker(store *db.DB, box *secrets.Box, log *slog.Logger, a db.AutomodSettings,
	slot automod.Slot, budget *automod.Budget) automod.ModelChecker {
	conn := slotSettings(a, slot)
	if !conn.Enabled {
		return nil
	}
	key, binding, err := store.GetAutomodKeyBound(box, string(slot))
	if err != nil {
		// Fail open, consistent with the connector itself: a key that cannot
		// be read means this checker cannot run, not that chat stops.
		log.Warn("automod model key unreadable; this checker will fail open", "checker", slot, "err", err)
	}
	return newChecker(conn, a.Model, key, binding, budget, func(err error) {
		// Named loudly rather than swallowed, for the same reason a rule that
		// does not compile is: a confidence floor an operator set and this
		// server ignored is a moderation posture they believe they have. The
		// checker runs on the default rather than not at all.
		log.Error("automod model confidence floor rejected; using the default", "checker", slot, "err", err)
	})
}

// newChecker builds the connector a block's protocol asks for.
//
// binding is the endpoint key was issued for. The connector sends the key only
// to that endpoint, so a settings document and a key read a moment apart --
// with a save moving the endpoint in between -- cannot pair the old key with
// the new address: the call is refused instead.
func newChecker(conn, shared db.AutomodModel, key, binding string, budget *automod.Budget, onFloorErr func(error)) automod.ModelChecker {
	if protocolOf(conn) == automod.ProtocolJev {
		cfg, err := jevConfigFor(conn, shared)
		if err != nil && onFloorErr != nil {
			onFloorErr(err)
		}
		cfg.APIKey, cfg.KeyBinding = key, binding
		return automod.NewJev(cfg, budget)
	}
	cfg, err := modelConfigFor(conn, shared)
	if err != nil && onFloorErr != nil {
		onFloorErr(err)
	}
	cfg.APIKey, cfg.KeyBinding = key, binding
	return automod.NewModel(cfg, budget)
}

func ApplyAutomod(hub *chat.Hub, store *db.DB, box *secrets.Box, log *slog.Logger, a db.AutomodSettings,
	budget, secondaryBudget *automod.Budget) {
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

	// Two connectors, each with its OWN budget -- the same budget every time
	// this runs, because rebuilding a connector on a settings save is correct
	// and refilling its hourly allowance was not. See automod.Budget.
	primary := buildChecker(store, box, log, a, automod.SlotPrimary, budget)
	secondary := buildChecker(store, box, log, a, automod.SlotSecondary, secondaryBudget)
	var model automod.ModelChecker
	if primary != nil || secondary != nil {
		model = automod.NewChain(primary, secondary)
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
