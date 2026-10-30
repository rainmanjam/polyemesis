// Package obsbridge is the OBS bridge (#16): it learns what OBS is doing and
// turns three of those facts into actions on this install.
//
//   - OBS starts streaming: the selected destinations go live.
//   - OBS stops streaming: they stop, after an optional grace period, and only
//     if the operator has said they understand what stopping does.
//   - OBS switches scene: the scene's routing preset is applied to the
//     selected destinations, as live mutes and unmutes (I4) and never as a
//     restart.
//
// TWO WAYS IN, ONE PATH THROUGH. Polyemesis either dials obs-websocket v5
// itself (client.go) when OBS is reachable from this box -- on the LAN, or over
// Tailscale -- or the OBS script in integrations/obs pushes the same three
// facts to POST /api/v1/obs/events when it is not, such as a VPS with no
// tunnel back. Both arrive at Bridge.Handle as an Event, and from there on
// nothing knows or cares which way a fact came in. That is what lets both run
// at once without doubling an action: Handle acts on EDGES (stopped to
// started, one scene to another) and a second report of the same state is not
// an edge.
//
// WHAT IS DELIBERATELY NOT HERE. Polyemesis sends obs-websocket no request
// that changes anything in OBS -- only the two reads that tell it the current
// state after a reconnect -- and no vendor requests (out of scope by
// agreement). The bridge also never treats a lost connection as OBS stopping:
// OBS crashing, the LAN dropping, or polyemesis restarting is not the
// operator saying they are finished, which is the same line the destination
// lifecycle draws for a daemon that dies (internal/api/destinations_bulk.go).
package obsbridge

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Bounds on the stored configuration. Each one is a size an operator will never
// reach by hand and a script feeding the PUT will hit long before it can make
// the row, or the scene lookup on every switch, expensive.
const (
	MaxURLLength        = 512
	MaxDestinations     = 64
	MaxSceneRules       = 64
	MaxSceneNameLength  = 256
	MaxStopGraceSeconds = 3600
)

// Config is the bridge's stored configuration: GET and PUT /api/v1/obs.
//
// The obs-websocket password is deliberately NOT here, for the reason the MQTT
// broker password is not in the settings blob: this struct is served to the
// page. It lives sealed in its own row (db.PutOBSPassword) and HasPassword is
// all the page is told.
type Config struct {
	// Enabled is the master switch. Off, the bridge acts on nothing, from
	// either way in, and the script's POST is refused with a sentence saying so.
	Enabled bool `json:"enabled"`
	// Connect makes polyemesis dial obs-websocket at URL. Off is the
	// script-only deployment: OBS cannot be reached from here, so the OBS
	// script reports to polyemesis instead.
	Connect bool `json:"connect"`
	// URL is obs-websocket's address: ws://host:4455, or wss:// behind a TLS
	// proxy. Credentials and a query string are refused rather than accepted,
	// because anything in a URL reaches logs and error strings.
	URL string `json:"url"`
	// HasPassword reports that a sealed obs-websocket password exists. Output
	// only: a PUT cannot set it, and PUT /api/v1/obs/password is the one way in.
	HasPassword bool `json:"hasPassword"`

	// SourceID is the programme the bridge drives. Every destination below must
	// belong to it, which is the rule stop-all learned the hard way: a control
	// that names a programme must not reach another programme's broadcasts.
	// Nil means the install's default programme.
	SourceID *int64 `json:"sourceId"`
	// Destinations are the ones OBS starts and stops, and the default target of
	// every scene rule. A selection, never "all": see the package comment on
	// what stopping a YouTube destination does.
	Destinations []int64 `json:"destinations"`

	// StartOnStream starts the destinations when OBS starts streaming.
	StartOnStream bool `json:"startOnStream"`
	// StopOnStream stops them when OBS stops streaming. Refused unless
	// StopAcknowledged is also set; see Problems.
	StopOnStream bool `json:"stopOnStream"`
	// StopAcknowledged is the operator saying, once and in writing, what
	// POST /destinations/stop-all makes every caller say per request: stopping
	// a destination ends its YouTube broadcast permanently. Stop-all's
	// {"confirm": true} cannot be asked of OBS at the moment it stops, so it is
	// asked of the operator when they turn the automation on.
	StopAcknowledged bool `json:"stopAcknowledged"`
	// StopGraceSeconds waits this long after OBS stops before stopping
	// anything. OBS starting again inside the window cancels the stop, so a
	// quick restart of the encoder does not end a broadcast. Zero stops at once.
	StopGraceSeconds int `json:"stopGraceSeconds"`

	// Scenes maps an OBS scene to a routing preset.
	Scenes []SceneRule `json:"scenes"`
}

// SceneRule applies one routing preset when OBS switches to one scene.
type SceneRule struct {
	// Scene is the OBS scene name, matched exactly: OBS scene names are
	// case-sensitive and two scenes may differ only in case.
	Scene string `json:"scene"`
	// Preset is a routing preset id, as GET /api/v1/routing/presets lists them.
	Preset string `json:"preset"`
	// Opts are the preset's track choices. Nil means the catalogue defaults,
	// exactly as a bodyless POST /routing/presets/{preset} does.
	Opts *routing.PresetOpts `json:"opts,omitempty"`
	// Destinations narrows the rule to some of Config.Destinations. Empty means
	// all of them. A destination listed here that is not in
	// Config.Destinations is refused: the bridge's reach is one list.
	Destinations []int64 `json:"destinations,omitempty"`
}

// Rule returns the rule for a scene, if one exists.
func (c Config) Rule(scene string) (SceneRule, bool) {
	for _, r := range c.Scenes {
		if r.Scene == scene {
			return r, true
		}
	}
	return SceneRule{}, false
}

// Targets returns the destinations a rule applies to.
func (c Config) Targets(r SceneRule) []int64 {
	if len(r.Destinations) > 0 {
		return append([]int64(nil), r.Destinations...)
	}
	return append([]int64(nil), c.Destinations...)
}

// StopArmed reports whether OBS stopping may stop anything. Both halves, so a
// row written by hand or by an older build with the acknowledgement missing
// stops nothing rather than everything.
func (c Config) StopArmed() bool { return c.StopOnStream && c.StopAcknowledged }

// stopAckText is the sentence a refused PUT carries. It names the consequence
// in the words POST /destinations/stop-all uses, so an operator meets the same
// warning whichever control they reach for.
const stopAckText = "stopping destinations when OBS stops ends any live YouTube broadcast among them, " +
	"permanently -- starting them again puts video back on the wire but does not bring the " +
	`broadcast back. Set "stopAcknowledged": true once that is intended`

// Problems reports everything wrong with c, in the order an operator would fix
// it. It checks only what can be checked without the database; whether each
// destination exists and belongs to SourceID is the API's to check.
func (c Config) Problems() []string {
	var probs []string
	if c.Connect || c.URL != "" {
		if err := checkURL(c.URL); err != nil {
			probs = append(probs, err.Error())
		}
	}
	if c.StopOnStream && !c.StopAcknowledged {
		probs = append(probs, stopAckText)
	}
	if c.StopGraceSeconds < 0 || c.StopGraceSeconds > MaxStopGraceSeconds {
		probs = append(probs, fmt.Sprintf("stopGraceSeconds must be between 0 and %d", MaxStopGraceSeconds))
	}
	if len(c.Destinations) > MaxDestinations {
		probs = append(probs, fmt.Sprintf("at most %d destinations", MaxDestinations))
	}
	selected := map[int64]bool{}
	for _, id := range c.Destinations {
		if id <= 0 {
			probs = append(probs, "destination ids are positive")
			break
		}
		if selected[id] {
			probs = append(probs, fmt.Sprintf("destination %d is listed twice", id))
		}
		selected[id] = true
	}
	if len(c.Scenes) > MaxSceneRules {
		probs = append(probs, fmt.Sprintf("at most %d scene rules", MaxSceneRules))
	}
	presets := map[string]bool{}
	for _, p := range routing.Presets() {
		presets[p.ID] = true
	}
	scenes := map[string]bool{}
	for i, r := range c.Scenes {
		label := fmt.Sprintf("scene rule %d", i+1)
		switch {
		case strings.TrimSpace(r.Scene) == "":
			probs = append(probs, label+" names no scene")
		case len(r.Scene) > MaxSceneNameLength || !utf8.ValidString(r.Scene):
			probs = append(probs, fmt.Sprintf("%s: a scene name is at most %d bytes of text", label, MaxSceneNameLength))
		case scenes[r.Scene]:
			probs = append(probs, label+": that scene already has a rule")
		}
		scenes[r.Scene] = true
		if !presets[r.Preset] {
			probs = append(probs, label+": unknown routing preset; GET /api/v1/routing/presets lists them")
		}
		for _, id := range r.Destinations {
			if !selected[id] {
				probs = append(probs, fmt.Sprintf("%s: destination %d is not one of the bridge's destinations", label, id))
			}
		}
	}
	return probs
}

// ErrBadURL is wrapped by every URL refusal, so a test can tell the URL check
// from the others without matching a sentence.
var ErrBadURL = errors.New("obs-websocket address")

// checkURL validates obs-websocket's address.
//
// NO ERROR HERE QUOTES THE URL. url.Parse's own error embeds the whole input,
// and the whole point of refusing user:password@ is that it must not travel;
// echoing it back in a 400 would be that travel. The reason alone is the
// diagnostic -- the operator can see what they typed.
//
// Private and Tailscale addresses are accepted on purpose. OBS on the LAN, or
// on the tailnet, is the normal deployment; the URL is set by a signed-in
// operator or an admin token, the same trust the MQTT broker URL gets.
func checkURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w is required to connect: ws://host:4455", ErrBadURL)
	}
	if len(raw) > MaxURLLength {
		return fmt.Errorf("%w is longer than %d characters", ErrBadURL, MaxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w is not a URL", ErrBadURL)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("%w must start ws:// or wss://", ErrBadURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w must not carry a user or password; set the password on its own", ErrBadURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w must not carry a query string", ErrBadURL)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w names no host", ErrBadURL)
	}
	return nil
}
