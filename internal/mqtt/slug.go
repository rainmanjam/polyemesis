// Package mqtt publishes retained telemetry to an MQTT broker.
//
// It speaks **MQTT 5.0 only**. The client underneath is eclipse/paho.golang,
// whose own README states it implements the Version 5.0 specification and
// nothing earlier. A broker pinned to 3.1.1 will not complete a connection at
// all, so this is a deployment fact rather than a detail -- see docs/MQTT.md.
//
// The one thing this package exists for is `retain`. polyemesis already pushes
// state over a WebSocket and already posts alerts to a webhook, and both
// require the consumer to be connected at the moment something changed.
// Nothing here could answer "is the ingest up?" to a dashboard that restarted
// five minutes ago. A retained message can, and that is the whole design.
package mqtt

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// hashHexLen is how many hex characters of sha256 are appended to a slug that
// had to be altered.
//
// The design called for 4. Four is too few and the arithmetic is not close:
// 16 bits gives a birthday collision probability of roughly n^2/2/65536, which
// is ~1.9% across 50 altered names and ~68% across 300. A collision here is not
// cosmetic -- two destinations silently share one retained topic and one Home
// Assistant entity, and the loser's telemetry is overwritten by the winner's
// on every tick, with nothing anywhere reporting it.
//
// Eight hex characters is 32 bits: ~0.0001% across 100 names. The cost is four
// bytes in a topic string nobody types by hand.
const hashHexLen = 8

// safe matches a run of characters that need no substitution. MQTT topic names
// forbid `+`, `#` and NUL; Home Assistant's node and object ids are documented
// as `[a-zA-Z0-9_-]`. The intersection, lowercased, is this.
var safe = regexp.MustCompile(`[^a-z0-9_-]+`)

// alreadySuffixed matches a name that already ends in something shaped like the
// hash this function appends. Such a name is hashed too, even when it is
// otherwise clean -- see Slug.
var alreadySuffixed = regexp.MustCompile(`-[0-9a-f]{` + strconv.Itoa(hashHexLen) + `}$`)

// Slug converts an operator's free-text name into one topic segment.
//
// It is the single chokepoint for every name that reaches a topic or a Home
// Assistant entity id, and it is a correctness problem rather than a cosmetic
// one. `Twitch (main)` and `Twitch [main]` both reduce to `twitch-main`; if
// that were the answer, one destination's retained state would overwrite the
// other's forever, and the symptom -- a dashboard entity that flickers between
// two streams' numbers -- looks nothing like its cause.
//
// So: whenever the reduction is not byte-identical to the input, the original
// name's hash is appended. Two different names can then never produce the same
// segment except through an actual sha256 collision in 32 bits.
//
// The `alreadySuffixed` case closes the remaining structural hole. Without it a
// source literally named `twitch-1a2b3c4d` would slug to itself unaltered, and
// some other name whose hash happened to be `1a2b3c4d` would slug to the same
// thing. Hashing anything already shaped like a suffix means both sides carry a
// hash, so equality of the output implies equality of both the cleaned prefix
// and the hash.
//
// An empty or entirely-unsafe name yields `x` plus a hash rather than an empty
// string. An empty segment would produce a topic with `//` in it, which is a
// *distinct* topic from the one intended and matches no subscriber filter the
// operator would think to write.
func Slug(name string) string {
	clean := safe.ReplaceAllString(strings.ToLower(name), "-")
	clean = collapse(clean)
	clean = strings.Trim(clean, "-")

	altered := clean != name || clean == "" || alreadySuffixed.MatchString(clean)
	if clean == "" {
		clean = "x"
	}
	if !altered {
		return clean
	}
	sum := sha256.Sum256([]byte(name))
	return clean + "-" + hex.EncodeToString(sum[:])[:hashHexLen]
}

// collapse squeezes runs of `-` down to one. Done here rather than in the
// regexp because the regexp replaces runs of *unsafe* characters, and a name
// like `a - b` leaves adjacent hyphens from two separate replacements.
func collapse(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
