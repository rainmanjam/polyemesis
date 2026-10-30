package automod

// Model-checker providers: which wire protocol a checker speaks, and the
// presets an operator picks from instead of typing a base URL from memory.
//
// TWO CHECKERS, ONE COLUMN. The matrix has a single "model" column and still
// does: the operator arms "timeout / model", not "timeout / Jev". Which of the
// two configured checkers answered is recorded on the Finding (DecidedBy), so
// the review queue and the moderation event can say who decided without the
// matrix -- and every stored cell -- growing a dimension.
//
// PRESETS ARE DATA, NOT URLS. What made the spike's local and hosted runs
// differ was never the URL. It was what the endpoint does with response_format
// (Cerebras enforces a strict json_schema; Ollama maps json_object to
// format:json), whether a reasoning model has to be told not to think before it
// answers inside the 4 s timeout, and where the message ends up. Each preset
// carries those, and the server reads them from here by ID -- a client cannot
// send its own JSON mode or switch a local preset's flag-only lock off.

// Protocol is the wire protocol a checker speaks.
type Protocol string

const (
	// ProtocolJev is TypeSafe's typed-decision API (POST /v1/systemone), also
	// served locally by Ollama for Jev-compatible decision models such as
	// laya. It answers typed questions with probabilities, so the confidence
	// floor gates a calibrated number rather than one the model wrote.
	ProtocolJev Protocol = "jev"
	// ProtocolOpenAI is any OpenAI-compatible chat-completions endpoint.
	ProtocolOpenAI Protocol = "openai"
)

// Protocols is every protocol, in the order the UI offers them.
var Protocols = []Protocol{ProtocolJev, ProtocolOpenAI}

// KnownProtocol reports whether p is a protocol this build speaks.
func KnownProtocol(p Protocol) bool {
	for _, k := range Protocols {
		if p == k {
			return true
		}
	}
	return false
}

// Slot names one of the two checkers.
type Slot string

const (
	// SlotPrimary is asked first.
	SlotPrimary Slot = "primary"
	// SlotSecondary is asked only when the primary could not answer.
	SlotSecondary Slot = "secondary"
)

// Privacy is where a preset sends the messages it is asked about. The UI
// turns it into the sentence on the card; it is a class rather than prose so
// the sentence can be translated.
type Privacy string

const (
	// PrivacyLocal: the message stays on this machine or this network.
	PrivacyLocal Privacy = "local"
	// PrivacyHostedUS: the message is sent to the provider, in the US.
	PrivacyHostedUS Privacy = "hosted_us"
	// PrivacyRelay: the message is sent to a router that forwards it to a
	// third party it chooses.
	PrivacyRelay Privacy = "relay"
	// PrivacyUnknown: a custom endpoint. Wherever the URL points.
	PrivacyUnknown Privacy = "unknown"
)

// JSONMode is the response_format an OpenAI-compatible preset is sent.
type JSONMode string

const (
	// JSONModeSchema sends a strict json_schema, which the provider enforces
	// by constrained decoding: the verdict cannot be missing a field.
	JSONModeSchema JSONMode = "json_schema"
	// JSONModeObject asks for any JSON object. The presence check in ask() is
	// what makes it the verdict's shape.
	JSONModeObject JSONMode = "json_object"
)

// Preset is one provider choice.
type Preset struct {
	ID       string   `json:"id"`
	Protocol Protocol `json:"protocol"`
	// Name is the provider's own name, which is a brand and not translated.
	Name string `json:"name"`
	// Endpoint is the full URL a checker on this preset posts to. Empty for
	// the Custom presets, where the operator supplies it.
	Endpoint string `json:"endpoint"`
	// Model is a suggestion, filled into the form when the preset is picked.
	Model string `json:"model"`
	// JSONMode and ReasoningEffort apply to the OpenAI-compatible protocol.
	JSONMode JSONMode `json:"jsonMode,omitempty"`
	// ReasoningEffort is sent as reasoning_effort when set. "none" is what
	// keeps a reasoning model (Cerebras qwen-3.8) inside the timeout: the
	// spike measured 15 s and an empty answer at the default, 0.42 s at none.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// Compact selects the short two-question Jev schema, for a local
	// decision model with a small context window (laya's is 512 tokens).
	Compact bool `json:"compact,omitempty"`
	// Privacy is where messages go.
	Privacy Privacy `json:"privacy"`
	// Local presets are FLAG-ONLY. No local model in the spike was good
	// enough to act on its own (the best had 1.8% false positives and 26%
	// recall), so a human decides, whatever the category table says.
	Local bool `json:"local"`
}

// presets is the table. Order is the order the UI offers them in.
var presets = []Preset{
	{ID: "jev", Protocol: ProtocolJev, Name: "TypeSafe Jev",
		Endpoint: "https://api.typesafe.ai/v1/systemone", Model: "jev-1.14.0",
		Privacy: PrivacyHostedUS},
	{ID: "jev-ollama", Protocol: ProtocolJev, Name: "Ollama (laya)",
		Endpoint: "http://localhost:11434/v1/systemone", Model: "laya",
		Compact: true, Privacy: PrivacyLocal, Local: true},
	{ID: "jev-custom", Protocol: ProtocolJev, Name: "Custom",
		Privacy: PrivacyUnknown},
	{ID: "openai", Protocol: ProtocolOpenAI, Name: "OpenAI",
		Endpoint: "https://api.openai.com/v1/chat/completions", Model: "gpt-4o-mini",
		JSONMode: JSONModeSchema, Privacy: PrivacyHostedUS},
	{ID: "cerebras", Protocol: ProtocolOpenAI, Name: "Cerebras",
		Endpoint: "https://api.cerebras.ai/v1/chat/completions", Model: "qwen-3.8-27b",
		JSONMode: JSONModeSchema, ReasoningEffort: "none", Privacy: PrivacyHostedUS},
	{ID: "groq", Protocol: ProtocolOpenAI, Name: "Groq",
		Endpoint: "https://api.groq.com/openai/v1/chat/completions", Model: "llama-3.1-8b-instant",
		JSONMode: JSONModeObject, Privacy: PrivacyHostedUS},
	{ID: "openrouter", Protocol: ProtocolOpenAI, Name: "OpenRouter",
		Endpoint: "https://openrouter.ai/api/v1/chat/completions", Model: "openai/gpt-4o-mini",
		JSONMode: JSONModeObject, Privacy: PrivacyRelay},
	{ID: "ollama", Protocol: ProtocolOpenAI, Name: "Ollama",
		Endpoint: "http://localhost:11434/v1/chat/completions", Model: "llama3.2:3b",
		JSONMode: JSONModeObject, Privacy: PrivacyLocal, Local: true},
	{ID: "custom", Protocol: ProtocolOpenAI, Name: "Custom",
		JSONMode: JSONModeObject, Privacy: PrivacyUnknown},
}

// Presets returns a copy of the table, for the API to serve.
func Presets() []Preset { return append([]Preset(nil), presets...) }

// CustomPreset is the protocol's Custom entry: the fallback for an empty or
// unknown preset ID, because it promises nothing about the endpoint -- it is
// not local (so not locked to flag, which would silently change what an
// upgraded install does) and it sends the plainest JSON mode.
func CustomPreset(p Protocol) Preset {
	id := "custom"
	if p == ProtocolJev {
		id = "jev-custom"
	}
	pr, _ := PresetByID(id)
	return pr
}

// PresetByID looks a preset up. ok is false for an ID this build does not
// know.
func PresetByID(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ResolvePreset is the preset a stored (protocol, id) pair means: the named
// one when it exists and speaks that protocol, otherwise that protocol's
// Custom entry. A preset of the wrong protocol is NOT honoured -- its JSON
// mode and lock describe a different wire.
func ResolvePreset(p Protocol, id string) Preset {
	if pr, ok := PresetByID(id); ok && pr.Protocol == p {
		return pr
	}
	return CustomPreset(p)
}

// FlagBandFloor is the bottom of the Jev flag band. A Jev probability at or
// above it but under the operator's floor puts the message in front of a
// human and does nothing else. The spike measured the band's worth: at 0.5
// recall rose from 72% to 82% with harmless false positives still at 1%.
const FlagBandFloor Confidence = 0.5

// categoryAction is the action a model finding in category c asks for.
//
// The per-category table wins when it names a known action for c. A category
// it does not name falls back to base, the checker's single legacy Action --
// which is flag on every fresh install, and is whatever an upgraded operator
// had chosen before the table existed, so an upgrade cannot silently turn
// their timeouts into flags. A local preset is flag-only whatever either says.
func categoryAction(base Action, table map[Category]Action, flagOnly bool, c Category) Action {
	if flagOnly {
		return ActionFlag
	}
	if a, ok := table[c]; ok && KnownActions(a) {
		return a
	}
	if base == "" || !KnownActions(base) {
		return ActionFlag
	}
	return base
}
