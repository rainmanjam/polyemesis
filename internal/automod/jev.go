package automod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Jev connector: a typed-decision moderation model on TypeSafe's System
// One protocol, POST /v1/systemone. The same protocol is served locally by
// Ollama for Jev-compatible decision models such as laya, so this one
// connector is both the hosted path and a private one.
//
// Sources: https://docs.typesafe.ai/api/system-one.md (request and response),
// https://docs.typesafe.ai/models.md (pinned IDs), https://docs.typesafe.ai/
// api/errors.md (429/529 and Retry-After), https://docs.typesafe.ai/
// confidence.md (what the Choice "confidence" field is, and why it is not
// gated on).
//
// WHY A SECOND CONNECTOR rather than pointing model.go at it: Jev is not
// OpenAI-compatible. It takes a state and named typed questions and answers
// with probabilities. That suits moderation better than a chat completion, for
// three reasons the spike measured:
//
//   - The floor gates a PROBABILITY from the model's own distribution (AUC
//     0.999 on the 372-message set), not a number the model wrote in prose.
//     Most LLMs answer 0.9 or 0.95 for everything they flag, so the same floor
//     on them filters very little.
//   - There is no prose to inject. Every answer is a number or a key from a
//     list this file sent, so the #495 defence -- the model chooses from a
//     list and the parse rejects anything else -- holds by construction, and
//     the Note is built from numbers.
//   - Malformed JSON is not a failure mode of the service. A reply missing an
//     answer still is, and it fails open loudly like model.go's.
//
// The three properties in model.go hold unchanged: FAIL OPEN, NEVER ON THE HOT
// PATH, BOUNDED SPEND. A 429 or 529 pauses calls for the Retry-After the
// service names, failing open meanwhile instead of retrying into the limit
// while chat keeps arriving -- and with a secondary configured, the paused
// period is exactly when the secondary answers instead.

// JevDefaultModel is the pinned model. Pinned, not jev-latest: the docs say an
// alias can move and change answers under a threshold someone tuned.
const JevDefaultModel = "jev-1.14.0"

// jevMaxPause bounds a Retry-After. The header is the far end's word, and an
// endpoint that answered "Retry-After: 99999999" must not switch a checker off
// for three years.
const jevMaxPause = 5 * time.Minute

// jevDefaultPause is used for a 429/529 with no usable Retry-After.
const jevDefaultPause = 5 * time.Second

// JevConfig configures the connector. Its fields mean what ModelConfig's do;
// the differences are named where they occur.
type JevConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	// APIKey is sealed at rest and never serialised.
	APIKey          string        `json:"-"`
	Timeout         time.Duration `json:"timeout"`
	MaxCallsPerHour int           `json:"maxCallsPerHour"`
	Action          Action        `json:"action"`
	TimeoutSeconds  int           `json:"timeoutSeconds"`
	// MinConfidence gates P(act) -- the probability that the message breaks
	// the operator's rule. NOT the Choice answer's "confidence" field: that is
	// a normalised margin, (p_max - 1/n) / (1 - 1/n), so with seven options a
	// winner at p=0.5 reads as about 0.42. Gating on it would silently make
	// the floor far stricter than the number the operator typed.
	MinConfidence   Confidence          `json:"minConfidence"`
	Instruction     string              `json:"instruction"`
	Preset          string              `json:"preset,omitempty"`
	Compact         bool                `json:"compact,omitempty"`
	FlagOnly        bool                `json:"flagOnly,omitempty"`
	CategoryActions map[Category]Action `json:"categoryActions,omitempty"`
}

// Jev is the connector.
type Jev struct {
	cfg    JevConfig
	client *http.Client
	budget *Budget

	mu          sync.Mutex
	failures    int
	lastErr     string
	lastCall    time.Time
	pausedUntil time.Time
	now         func() time.Time
}

// NewJev returns a connector. The budget is a required parameter for the same
// reason it is on NewModel.
func NewJev(cfg JevConfig, budget *Budget) *Jev {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 4 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = JevDefaultModel
	}
	return &Jev{cfg: cfg, budget: budget, client: &http.Client{Timeout: cfg.Timeout, CheckRedirect: refuseRedirect}, now: time.Now}
}

// Enabled reports whether this connector will make calls. Nil-safe.
func (j *Jev) Enabled() bool { return j != nil && j.cfg.Enabled }

// JevVerdict is the typed answer set. Every field is a number or a key from a
// list this file sent; nothing here is model prose.
type JevVerdict struct {
	Model string
	// PAct is the Noul answer: the probability the message breaks the rule.
	PAct float64
	// Choice is the winning key of the category question.
	Choice string
	// PCategory is every category's probability, keyed as sent.
	PCategory map[string]float64
	// ChoiceConfidence is the Choice answer's own confidence field. Kept so a
	// test can prove it is NOT what the floor gates.
	ChoiceConfidence float64
	PTargeted        float64
	Severity         float64
}

// jevOK is the category key for an allowed message.
const jevOK = "ok"

// errJevPaused is returned while a 429/529 back-off is in force.
var errJevPaused = errors.New("the Jev endpoint asked for a pause (429/529); the message passes until it ends")

// Check asks the model about one message. Same contract as Model.Check: on any
// error the findings are empty and the message passes.
func (j *Jev) Check(ctx context.Context, text string) ([]Finding, error) {
	if j == nil || !j.cfg.Enabled || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	j.mu.Lock()
	paused := j.now().Before(j.pausedUntil)
	j.mu.Unlock()
	if paused {
		// Recorded, but not charged: no call was made. Recorded because a
		// checker that is paused fails every message open, and that has to be
		// visible on the card.
		j.record(errJevPaused)
		return nil, errJevPaused
	}
	if j.budget != nil && !j.budget.reserve(j.cfg.MaxCallsPerHour) {
		return nil, fmt.Errorf("hourly ceiling of %d calls reached", j.cfg.MaxCallsPerHour)
	}

	v, err := j.ask(ctx, text)
	var (
		cat  Category
		band jevBand
	)
	if err == nil {
		cat, band, err = jevDecide(v, j.cfg.MinConfidence)
	}
	j.record(err)
	if err != nil || band == jevBandNone {
		return nil, err
	}

	action := categoryAction(j.cfg.Action, j.cfg.CategoryActions, j.cfg.FlagOnly, cat)
	if band == jevBandFlag {
		// Under the operator's floor: a human looks, nothing else happens.
		// Nothing irreversible is ever chosen from a probability under the
		// floor, whatever the category table says.
		action = ActionFlag
	}
	return []Finding{{
		Checker:        CheckerModel,
		Action:         action,
		TimeoutSeconds: j.cfg.TimeoutSeconds,
		Confidence:     v.PAct,
		Category:       cat,
		Reason:         cat.Reason(),
		Provider:       j.cfg.Preset,
		// Numbers and keys we sent. There is no model prose to sanitise.
		Note: fmt.Sprintf("jev p(act)=%.2f p(%s)=%.2f targeted=%.2f severity=%.1f",
			v.PAct, cat, v.PCategory[string(cat)], v.PTargeted, v.Severity),
	}}, nil
}

// jevBand is what a verdict amounts to.
type jevBand int

const (
	jevBandNone jevBand = iota // under the flag band: nothing
	jevBandFlag                // FlagBandFloor <= P(act) < floor: flag only
	jevBandAct                 // P(act) >= floor: the category's action
)

// jevDecide is the whole decision, and it reads PAct -- never
// ChoiceConfidence.
//
// When the category winner is "ok" while P(act) says otherwise (the two
// questions disagree), the most probable non-ok category is used. A winning
// key that is neither "ok" nor a model category is a reply this connector did
// not ask for, and it fails open as an error -- the same treatment model.go
// gives an unknown category.
func jevDecide(v JevVerdict, floor Confidence) (Category, jevBand, error) {
	band := jevBandNone
	switch {
	case v.PAct >= floor.Float():
		band = jevBandAct
	case v.PAct >= FlagBandFloor.Float():
		band = jevBandFlag
	}
	if band == jevBandNone {
		return "", band, nil
	}
	if v.Choice != jevOK {
		cat, ok := ParseModelCategory(v.Choice)
		if !ok {
			return "", jevBandNone, errUnknownCategory
		}
		return cat, band, nil
	}
	// Highest non-ok probability, ties broken by ModelCategories order so the
	// same reply always gets the same label.
	best, bestP := CategoryOther, -1.0
	for _, c := range ModelCategories() {
		if p, ok := v.PCategory[string(c)]; ok && p > bestP {
			best, bestP = c, p
		}
	}
	return best, band, nil
}

// jevCategoryCriteria: the keys are "ok" plus exactly ModelCategories(), so the
// reply is checked against the same set ParseModelCategory accepts.
func jevCategoryCriteria(compact bool) map[string]string {
	long := map[Category]string{
		CategoryHarassment:    "Targeted abuse or insults aimed at a person, including telling someone to harm or kill themselves",
		CategoryHateSpeech:    "Abuse aimed at a protected characteristic such as race, religion, gender, sexuality, disability or nationality, including masked or obfuscated slurs",
		CategoryThreat:        "Threatens or encourages violence against someone, or threatens doxxing or swatting",
		CategorySexualContent: "Sexual content, sexual solicitation, or sexual remarks aimed at a person",
		CategorySpam:          "Advertising, scams, crypto schemes, fake giveaways, phishing, follow-for-follow, or promoting another channel",
		CategoryOther:         "Breaks the community rule in a way none of the other options describes",
	}
	short := map[Category]string{
		CategoryHarassment:    "insulting or bullying a person",
		CategoryHateSpeech:    "hate or slurs against a group",
		CategoryThreat:        "threat of violence",
		CategorySexualContent: "sexual",
		CategorySpam:          "spam, scam or advertising",
		CategoryOther:         "breaks chat rules in another way",
	}
	desc, ok := long, "Allowed: ordinary chat, questions, slang, banter, gaming trash talk about gameplay, emotes, quotes, in any language"
	if compact {
		desc, ok = short, "normal chat"
	}
	out := map[string]string{jevOK: ok}
	for _, c := range ModelCategories() {
		out[string(c)] = desc[c]
	}
	return out
}

// jevRequestBody builds the documented SystemOneRequest. One request carries
// every question, so the message is ingested once and the call is charged
// once against the budget.
//
// The compact form is two short questions with the message as plain state,
// for a local decision model whose context window the full form overflows
// (laya: 512 tokens; the full request averages about 560). It does not carry
// the operator's Instruction, which is the cost of fitting.
func jevRequestBody(model, instruction, text string, compact bool) map[string]any {
	if compact {
		return map[string]any{
			"model": model,
			"state": text,
			"questions": map[string]any{
				"act": map[string]any{"type": "noul",
					"instructions": "Is this live-stream chat message abusive, hateful, threatening, sexual, or spam?"},
				"category": map[string]any{"type": "choice",
					"instructions": "What kind of chat message is this?",
					"criteria":     jevCategoryCriteria(true)},
			},
		}
	}
	return map[string]any{
		"model": model,
		"state": map[string]any{
			"context": "One message from the public chat of a live stream.",
			"message": text,
		},
		"questions": map[string]any{
			"act": map[string]any{
				"type":         "noul",
				"instructions": "Does `message` break this community rule? Rule: " + instruction,
				"criteria": map[string]string{
					"true":  "The message breaks the community rule.",
					"false": "The message is allowed by the community rule. Ordinary criticism, banter, gaming trash talk about gameplay and strong language that is not aimed at a person are allowed.",
				},
			},
			"category": map[string]any{
				"type":         "choice",
				"instructions": "Which option best describes `message`?",
				"criteria":     jevCategoryCriteria(false),
			},
			"targeted": map[string]any{
				"type":         "noul",
				"instructions": "Is `message` aimed at a specific person, such as the streamer or another chatter?",
			},
			"severity": map[string]any{
				"type":         "score",
				"instructions": "How serious a chat-rule violation is `message`?",
				"criteria": []string{
					"Harmless",
					"Rude or borderline; a human moderator might want to look",
					"Clearly breaks the rules",
					"Severe: a threat, hate speech, sexual harassment or self-harm incitement",
				},
			},
		},
	}
}

// probability reports whether p is a usable probability.
func probability(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && *p >= 0 && *p <= 1
}

// ask performs one POST.
func (j *Jev) ask(ctx context.Context, text string) (JevVerdict, error) {
	ctx, cancel := context.WithTimeout(ctx, j.cfg.Timeout)
	defer cancel()

	buf, err := json.Marshal(jevRequestBody(j.cfg.Model, j.cfg.Instruction, text, j.cfg.Compact))
	if err != nil {
		return JevVerdict{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.cfg.Endpoint, bytes.NewReader(buf))
	if err != nil {
		return JevVerdict{}, redactEndpoint(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if j.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return JevVerdict{}, redactEndpoint(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return JevVerdict{}, err
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
		pause := jevDefaultPause
		if s, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && s > 0 {
			pause = min(time.Duration(s)*time.Second, jevMaxPause)
		}
		j.mu.Lock()
		j.pausedUntil = j.now().Add(pause)
		j.mu.Unlock()
	}
	if resp.StatusCode != http.StatusOK {
		// The status only, never the body. A 422 body previews the request,
		// and the request carries a viewer's message; a 401 body from some
		// gateways echoes part of the key.
		return JevVerdict{}, fmt.Errorf("Jev API returned %d", resp.StatusCode)
	}

	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Noul          *float64           `json:"noul"`
			Choice        string             `json:"choice"`
			Confidence    *float64           `json:"confidence"`
			Score         *float64           `json:"score"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return JevVerdict{}, errors.New("Jev response was not JSON")
	}
	// PRESENCE, for the reason model.go checks it: an object without our
	// answers would otherwise decode into P(act)=0, "not abusive", with no
	// error -- a healthy-looking checker that never fires. Fixed messages:
	// they reach LastError, and the reply is the far end's text.
	act, okA := out.Answers["act"]
	cat, okC := out.Answers["category"]
	if !okA || !probability(act.Noul) {
		return JevVerdict{}, errors.New("Jev response is missing the act probability, so it cannot be read as a verdict")
	}
	if !okC || cat.Choice == "" {
		return JevVerdict{}, errors.New("Jev response is missing the category answer, so it cannot be read as a verdict")
	}
	v := JevVerdict{Model: out.Model, PAct: *act.Noul, Choice: cat.Choice, PCategory: cat.Probabilities}
	if cat.Confidence != nil {
		v.ChoiceConfidence = *cat.Confidence
	}
	if t, ok := out.Answers["targeted"]; ok && probability(t.Noul) {
		v.PTargeted = *t.Noul
	}
	if s, ok := out.Answers["severity"]; ok && s.Score != nil {
		v.Severity = *s.Score
	}
	return v, nil
}

func (j *Jev) record(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastCall = j.now()
	if err != nil {
		j.failures++
		j.lastErr = err.Error()
	}
}

// Stats reports spend and health for the operator.
func (j *Jev) Stats() ModelStats {
	if j == nil {
		return ModelStats{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return ModelStats{
		CallsThisHour: j.budget.Spent(),
		Ceiling:       j.cfg.MaxCallsPerHour,
		Failures:      j.failures,
		LastError:     j.lastErr,
		LastCallAt:    j.lastCall,
		Protocol:      ProtocolJev,
		Preset:        j.cfg.Preset,
	}
}
