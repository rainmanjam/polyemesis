package mqtt

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultPrefix is the root of the topic tree. Everything polyemesis publishes
// lives under it, so an operator can wipe the lot with one wildcard.
const DefaultPrefix = "polyemesis"

// HADiscoveryPrefix is where Home Assistant looks for discovery payloads. It is
// fixed by Home Assistant, not by us, and is deliberately NOT under Prefix.
const HADiscoveryPrefix = "homeassistant"

// maxTopicBytes is the MQTT limit on a topic name: it is a two-byte-length
// encoded UTF-8 string, so 65535 bytes. Nothing here will approach it, but a
// name field with no length limit reaching a topic builder is exactly how a
// publish starts failing for a reason no log line explains.
const maxTopicBytes = 65535

// ErrDollarPrefix is returned for a topic prefix beginning with `$`.
//
// Refused rather than escaped because the failure is invisible: brokers reserve
// `$`-prefixed topics for their own metrics, and a subscriber using `#` --
// which is what anyone debugging reaches for first -- is specified never to
// receive them. The telemetry would be published successfully, acknowledged by
// the broker, and absent from the one view the operator would use to look for
// it.
var ErrDollarPrefix = errors.New("topic prefix must not begin with $: brokers reserve those topics and a '#' subscription never receives them")

// Topics builds every topic this instance publishes to.
//
// Instance is already slugged on construction. It is what distinguishes two
// polyemesis installs sharing one broker, and it is what a Home Assistant
// device is keyed on.
type Topics struct {
	prefix   string
	instance string
}

// NewTopics validates the prefix and slugs the instance.
//
// The prefix is NOT slugged. An operator who writes `home/av` means a two-level
// prefix, and collapsing the separator into a hyphen would silently relocate
// their entire tree. It is validated instead, which fails loudly.
func NewTopics(prefix, instance string) (*Topics, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if strings.HasPrefix(prefix, "$") {
		return nil, ErrDollarPrefix
	}
	if strings.ContainsAny(prefix, "+#\x00") {
		return nil, fmt.Errorf("topic prefix %q contains a wildcard or NUL; those are legal in a subscription filter but not in a published topic name", prefix)
	}
	return &Topics{prefix: prefix, instance: Slug(instance)}, nil
}

// Instance is the slugged instance segment, needed by the Home Assistant
// discovery payload as a device identifier.
func (t *Topics) Instance() string { return t.instance }

// join assembles a topic from already-slugged segments.
//
// It takes a slice rather than a format string on purpose. `fmt.Sprintf` over a
// possibly-empty name yields `.../source//state`, which is a *distinct* topic
// from `.../source/state` and from the intended one -- MQTT treats an empty
// level as a real level. Nothing downstream would report it; the telemetry
// would simply appear on a topic no filter matches. Refusing an empty segment
// here is the only place that can catch it.
func (t *Topics) join(segments ...string) string {
	parts := make([]string, 0, len(segments)+2)
	parts = append(parts, t.prefix, t.instance)
	for _, s := range segments {
		if s == "" {
			// Unreachable via the exported builders -- Slug never returns "" --
			// so this is a guard against a future caller, not a runtime path.
			s = "x"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "/")
}

// Status is the availability topic carrying "online" or "offline". The will
// message is set on it, which is the entire reason a consumer can tell a clean
// shutdown from a power cut.
func (t *Topics) Status() string { return t.join("status") }

// State is the host-level snapshot.
func (t *Topics) State() string { return t.join("state") }

// Source is one programme's state topic.
func (t *Topics) Source(source string) string {
	return t.join("source", source, "state")
}

// SourceRoot is the wildcard covering everything published beneath one source,
// used to clear retained state when a source is deleted.
func (t *Topics) SourceRoot(source string) string {
	return t.join("source", source)
}

// Dest is one destination's state topic, nested under its source because a
// destination has no meaning without one and because deleting a source must be
// able to clear its destinations in the same sweep.
func (t *Topics) Dest(source, dest string) string {
	return t.join("source", source, "dest", dest, "state")
}

// Rendition is one shared encode's state topic.
func (t *Topics) Rendition(source, rend string) string {
	return t.join("source", source, "rendition", rend, "state")
}

// All is the wildcard matching everything under this instance. Used by the
// retained-cleanup sweep and worth having in one place, because getting it
// wrong means either missing orphans or clearing another install's tree.
func (t *Topics) All() string { return t.join("#") }

// Discovery is the Home Assistant device-discovery topic for this instance.
func (t *Topics) Discovery() string {
	return strings.Join([]string{HADiscoveryPrefix, "device", t.instance, "config"}, "/")
}

// Valid reports whether a topic is publishable. Wildcards are legal in a
// subscription filter and illegal in a published topic name, which is the
// distinction most easily got wrong.
func Valid(topic string) error {
	switch {
	case topic == "":
		return errors.New("empty topic")
	case len(topic) > maxTopicBytes:
		return fmt.Errorf("topic is %d bytes, over the %d-byte MQTT limit", len(topic), maxTopicBytes)
	case strings.ContainsAny(topic, "+#\x00"):
		return fmt.Errorf("topic %q contains a wildcard or NUL", topic)
	}
	return nil
}
