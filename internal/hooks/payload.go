package hooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// SpecVersion is the envelope contract version. A receiver is written against
// it, so any change to a field's name or meaning bumps this rather than
// silently breaking somebody's script at 3am.
const SpecVersion = "1"

// Delivery headers. Prefixed rather than bare, because a hook endpoint is very
// often a shared automation runner receiving from several systems.
const (
	SignatureHeader = "X-Polyemesis-Signature"
	TimestampHeader = "X-Polyemesis-Timestamp"
	TriggerHeader   = "X-Polyemesis-Trigger"
	DeliveryHeader  = "X-Polyemesis-Delivery"
	SequenceHeader  = "X-Polyemesis-Sequence"
)

// Envelope is the JSON body of every delivery.
//
// Flat on purpose. A hook is consumed by a shell script with jq as often as by
// a program, and `.destination.name` is the deepest anybody should have to
// reach.
type Envelope struct {
	SpecVersion string  `json:"specVersion"`
	ID          string  `json:"id"`
	Trigger     Trigger `json:"trigger"`
	// Sequence counts deliveries to THIS endpoint, from 1, and resets when the
	// process restarts. A receiver that sees it go backwards knows polyemesis
	// restarted -- which matters, because a restarted process has observed
	// nothing and republishes the current state as fresh events.
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	// Missed is how many deliveries to this endpoint were dropped because its
	// queue was full since the last successful one. Zero is omitted. A gap
	// admitted is a gap a receiver can go and reconcile; a gap hidden is a
	// receiver quietly out of date.
	Missed uint64 `json:"missed,omitempty"`
	// Test marks a delivery raised by the test button rather than by anything
	// that happened, so a script can refuse to act on it.
	Test        bool            `json:"test,omitempty"`
	Source      SourceRef       `json:"source"`
	Destination *DestinationRef `json:"destination,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Encode marshals an envelope. The bytes it returns are exactly what is signed
// and exactly what is sent -- re-marshalling between signing and sending is how
// a signature scheme quietly stops verifying.
func Encode(e Envelope) ([]byte, error) { return json.Marshal(e) }

// Sign returns the value for SignatureHeader.
//
// The timestamp is signed WITH the body rather than merely sent beside it. A
// digest over the body alone means a request captured off the wire can be
// replayed an hour later against a receiver that only compares digests; with
// the timestamp inside the MAC, a receiver can reject anything older than its
// own tolerance and the attacker cannot re-stamp it.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// NewSecret mints a signing key. Called whenever a hook is created without one,
// because an unsigned webhook is one that anybody who learns the URL can forge
// -- and a URL leaks through proxy logs, browser history and screenshots.
func NewSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// redacted scrubs the free text on an event.
//
// Applied by Dispatcher.Publish rather than left to the watcher, for the reason
// stated at alerts/redact.go:179 -- the strings arrive from a dozen places and
// exactly one careless one puts a stream key in somebody's automation log.
// Error in particular is FFmpeg stderr, which prints the full publish URL.
func (e Event) redacted() Event {
	e.Reason = alerts.Redact(e.Reason)
	e.Error = alerts.Redact(e.Error)
	if e.Destination != nil {
		d := *e.Destination
		d.Name = alerts.Redact(d.Name)
		e.Destination = &d
	}
	e.Source.Name = alerts.Redact(e.Source.Name)
	return e
}
