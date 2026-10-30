// Package failreason turns a failed FFmpeg run into the reason an operator
// needs: not "error", which is all supervisor.classify could ever say, but
// "the platform refused your key", "the hostname does not resolve", "the
// upload cannot keep up".
//
// It reads two things and nothing else. The tail of the child's stderr, which
// holds a lower-layer line ([tcp @], [tls @], [rtmp @] Server error:, [http @],
// the SRT handshake) for most causes. And timing: how long the run lasted,
// whether media ever moved, and how fast it was moving at the end. The second
// is not decoration. Two of the causes have no sentence of their own -- a slow
// upload prints nothing at all, and a refusal at publish prints the same
// "I/O error" whichever check the ingest failed -- and only the clock tells
// them apart from everything else.
//
// The fixtures in testdata are real FFmpeg stderr captured against local
// ingest servers, and testdata/README.md records how each was made and which
// of them cannot be told apart. Every rule below exists because one of them
// needed it.
//
// A LEAF PACKAGE ON PURPOSE. The supervisor classifies, and alerts, hooks,
// mqtt and the engine all carry the result; alerts is already imported by the
// supervisor, so the type has to live somewhere every one of them can import
// without a cycle.
package failreason

import (
	"fmt"
	"strings"
	"time"
)

// Cause names why a run failed. The strings are a published contract: they
// appear in the status JSON, the MQTT state topic, the webhook envelope and
// the alert payload, and the events API (#17) is to carry them as-is. Do not
// rename them.
type Cause string

const (
	// KeyRejected: the ingest answered and refused to let this stream
	// publish. Usually the stream key, but see the README: an unknown RTMP
	// application or an unconfigured path prints exactly the same, so the
	// remedy the UI offers says "check the key, then the server URL".
	KeyRejected Cause = "key_rejected"
	// PlatformNotReady: the ingest is up and not taking this stream yet --
	// an HTTP 5xx, or "Already publishing", which is the platform still
	// holding the previous session open.
	PlatformNotReady Cause = "platform_not_ready"
	// DNS, ConnectionRefused and Timeout are the network. They clear on
	// their own often enough that retrying them with the normal backoff is
	// the right answer, and they are never held.
	DNS               Cause = "dns"
	ConnectionRefused Cause = "connection_refused"
	Timeout           Cause = "timeout"
	// TLS: the encrypted connection itself failed -- the wrong scheme for
	// the port, or a certificate that does not verify.
	TLS Cause = "tls"
	// CodecRejected: the stream's codec or container was refused. Only
	// FFmpeg's own muxer says so in words; an ingest that drops a codec it
	// will not carry does so silently, and that is Unknown.
	CodecRejected Cause = "codec_rejected"
	// UploadTooSlow: media was moving, and the delivery rate had collapsed
	// below realtime when the run ended. From the timing alone.
	UploadTooSlow Cause = "upload_too_slow"
	// Unknown: none of the above. Reason.Line carries FFmpeg's own last
	// words so the operator still has something to search for.
	Unknown Cause = "unknown"
)

// AllCauses is every cause, in the order a legend should list them.
func AllCauses() []Cause {
	return []Cause{
		KeyRejected, PlatformNotReady, DNS, ConnectionRefused, Timeout,
		TLS, CodecRejected, UploadTooSlow, Unknown,
	}
}

// Label is the English phrase for a cause, used where there is no catalogue to
// translate from: alert text, the timeline, the webhook and log lines. The UI
// and the Stream Deck plugin translate the code themselves.
func (c Cause) Label() string {
	switch c {
	case KeyRejected:
		return "stream key rejected"
	case PlatformNotReady:
		return "platform not ready"
	case DNS:
		return "server name does not resolve"
	case ConnectionRefused:
		return "connection refused"
	case Timeout:
		return "connection timed out"
	case TLS:
		return "TLS connection failed"
	case CodecRejected:
		return "codec or format rejected"
	case UploadTooSlow:
		return "upload too slow"
	default:
		return "unknown failure"
	}
}

// Holdable reports whether a quick repeat of this cause should stop retrying.
//
// Only a refused key and a refused codec. Retrying either is guaranteed to fail
// until somebody changes something, and every attempt is a publish against a
// platform that may rate-limit or flag a key it keeps refusing. Every other
// cause can clear on its own: the network comes back, and a platform that is
// "already publishing" lets go of the old session within a minute.
func (c Cause) Holdable() bool { return c == KeyRejected || c == CodecRejected }

// Network reports whether the cause is the network between here and the
// ingest. These keep the normal backoff and are never held.
func (c Cause) Network() bool { return c == DNS || c == ConnectionRefused || c == Timeout }

// QuickWindow is how soon after spawning a run must fail to count as a quick
// rejection. A refusal at publish arrives in well under a second on a local
// ingest and a few seconds on a distant one (the captures measured 0.3-3.4 s);
// fifteen leaves room for a slow TLS handshake to a platform on another
// continent and is still far short of anything that delivered.
const QuickWindow = 15 * time.Second

// SlowRate is the windowed realtime ratio under which a run that was moving
// media is "too slow". Well under 1.0, because a passthrough dips briefly on
// every keyframe burst and a reason that flickered with them would be noise.
const SlowRate = 0.8

// Reason is a classified failure.
//
// The field names are the contract every outlet publishes -- see Cause. Line is
// SCRUBBED by whoever stores it (the supervisor runs it through the process's
// own secret set): FFmpeg prints the full publish URL when a connect fails, so
// the very line that explains a refused key is the line that carries it.
type Reason struct {
	Cause Cause `json:"cause"`
	// Line is the stderr line the verdict rests on, or for Unknown the last
	// meaningful thing FFmpeg said.
	Line string `json:"line,omitempty"`
	// Quick is set when the run ended within QuickWindow of its spawn.
	Quick bool `json:"quick,omitempty"`
	// Held is set when the supervisor stopped retrying after repeated quick
	// rejections. One operator action -- Retry -- clears it.
	Held bool `json:"held,omitempty"`
	// Rejections counts the consecutive quick, holdable failures this reason
	// ends. Zero for anything else.
	Rejections int `json:"rejections,omitempty"`
	// KeyChanged is set by the engine, not the classifier: the destination's
	// connected platform account reports a stream key that differs from the
	// stored one, so the remedy is one click on "update the key".
	KeyChanged bool `json:"keyChanged,omitempty"`
	// At is when the run ended.
	At time.Time `json:"at"`
}

// Input is what the classifier is given about one run.
type Input struct {
	// Stderr is the tail of the child's stderr, oldest first, UNSCRUBBED:
	// classification reads the text, and storing the result is the caller's
	// job.
	Stderr []string
	// Failed is false for a clean exit, which has no reason.
	Failed bool
	// Exit is the error the wait returned ("exit status 251"), used as the
	// line when stderr is empty.
	Exit string
	// RanFor is how long the run lasted.
	RanFor time.Duration
	// Moved is whether this run's output time ever advanced.
	Moved bool
	// Rate is the realtime ratio over the last few seconds of the run --
	// NOT FFmpeg's speed=, which is averaged over the whole run and so says
	// nothing about how the run ended. RateKnown is false when there were
	// not enough samples to measure one.
	Rate      float64
	RateKnown bool
}

// rule matches one cause on one line.
type rule struct {
	cause Cause
	match func(l, lower string) bool
}

func anyOf(subs ...string) func(string, string) bool {
	return func(_, lower string) bool {
		for _, s := range subs {
			if strings.Contains(lower, s) {
				return true
			}
		}
		return false
	}
}

// rules, in precedence order. Precedence is by CAUSE, not by line: a capture
// whose TLS line is followed by FFmpeg's generic "Error opening output ...:
// I/O error" must stay TLS, and the generic line must only ever decide the
// cause when nothing more specific was said.
var rules = []rule{
	// FFmpeg's own muxer refusing the codec. Said in words, before any byte
	// reaches the network, so it is the most certain thing in this list.
	{CodecRejected, anyOf(
		"not compatible with",
		"codec not currently supported in container",
		"could not write header (incorrect codec parameters",
		"incompatible with output codec",
		"unsupported codec",
		"codec not supported",
	)},
	// TLS. "[tls @" covers the wrong-scheme case, where the only TLS-level
	// line is the plain-text server resetting a ClientHello it cannot read.
	{TLS, func(l, lower string) bool {
		return strings.Contains(lower, "[tls @") ||
			strings.Contains(lower, "ssl routines") ||
			strings.Contains(lower, "certificate verify failed") ||
			strings.Contains(lower, "tls handshake") ||
			strings.Contains(lower, "gnutls")
	}},
	{DNS, anyOf(
		"failed to resolve hostname",
		"name does not resolve",
		"name or service not known",
		"nodename nor servname",
		"temporary failure in name resolution",
		"no address associated with hostname",
	)},
	{ConnectionRefused, anyOf("connection refused")},
	// Up, and not taking this stream yet. BEFORE KeyRejected, because
	// "Already publishing" arrives as EPERM ("Operation not permitted") on
	// the very next line, which on its own would read as a refused key.
	{PlatformNotReady, anyOf(
		"server error: already publishing",
		"netstream.publish.badname",
		"server returned 5xx",
		"503 service",
		"server returned 404",
		"404 not found",
		"not ready",
	)},
	{KeyRejected, func(l, lower string) bool {
		switch {
		case strings.Contains(lower, "server error:") && containsAny(lower,
			"auth", "denied", "unauthorized", "forbidden", "invalid", "key", "not allowed", "permission"):
			return true
		case containsAny(lower, "401 unauthorized", "403 forbidden", "authorization failed",
			"peer rejected connection", "netstream.publish.denied", "netconnection.connect.rejected"):
			return true
		case strings.Contains(lower, "error opening output") &&
			containsAny(lower, ": operation not permitted", ": permission denied"):
			return true
		}
		return false
	}},
}

// openRefused is the last-resort refusal: "Error opening output <url>: I/O
// error" with no lower-layer line above it. The TCP connection was made (or a
// [tcp @] line would have said otherwise) and the session was closed when the
// stream asked to publish, which is what an ingest does with a key it will not
// take. Checked only after every rule has had its turn.
//
// BOTH SPELLINGS OF EIO. The text is the C library's strerror, not FFmpeg's:
// musl (the Alpine image) says "I/O error", while glibc and Darwin -- every
// binary install -- say "Input/output error". Matching only the first turned a
// refused key on every binary install into "unknown" (ffmpeg9-* fixtures).
func openRefused(lower string) bool {
	return strings.Contains(lower, "error opening output") &&
		(strings.HasSuffix(lower, ": i/o error") || strings.HasSuffix(lower, ": input/output error"))
}

var timeoutSubs = []string{
	"operation timed out", "connection timed out", "timed out", "connection setup failure: connection time",
}

// Classify reports why a run failed, or ok=false for a run that did not.
func Classify(in Input) (r Reason, ok bool) {
	if !in.Failed {
		return Reason{}, false
	}
	r.Quick = in.RanFor < QuickWindow
	lowers := make([]string, len(in.Stderr))
	for i, l := range in.Stderr {
		lowers[i] = strings.ToLower(l)
	}
	for _, ru := range rules {
		// The FIRST matching line, not the last: the lower layer speaks
		// first and FFmpeg's generic summary follows it.
		for i, l := range in.Stderr {
			if ru.match(l, lowers[i]) {
				r.Cause, r.Line = ru.cause, strings.TrimSpace(l)
				return r, true
			}
		}
	}
	for i, l := range in.Stderr {
		if openRefused(lowers[i]) {
			r.Cause, r.Line = KeyRejected, strings.TrimSpace(l)
			return r, true
		}
	}
	// Crawling, not stopped. A rate of zero is a stall, and every destination
	// stalls when the INGEST stops, which is not this destination's upload.
	if in.Moved && in.RateKnown && in.Rate > 0 && in.Rate < SlowRate {
		r.Cause, r.Line = UploadTooSlow, lastMeaningful(in.Stderr, in.Exit)
		return r, true
	}
	for i, l := range in.Stderr {
		if containsAny(lowers[i], timeoutSubs...) {
			r.Cause, r.Line = Timeout, strings.TrimSpace(l)
			return r, true
		}
	}
	r.Cause, r.Line = Unknown, lastMeaningful(in.Stderr, in.Exit)
	return r, true
}

// lastMeaningful is the last stderr line that is about the failure rather than
// FFmpeg's bookkeeping, falling back to the exit error.
func lastMeaningful(lines []string, exit string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || noise(strings.ToLower(l)) {
			continue
		}
		return l
	}
	return exit
}

// noise is a line that ends healthy runs as often as failed ones.
func noise(lower string) bool {
	return strings.HasPrefix(lower, "last message repeated") ||
		strings.Contains(lower, "failed to update header with correct") ||
		strings.Contains(lower, "resumed reading at pts")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Sentence is the reason in English, for the outlets that have no catalogue to
// translate from: the alert text (and so the timeline, which stores it), the
// webhook and the server log. It names the cause, then the two facts that
// change what the operator does next -- held, and a rotated key -- and for an
// unknown cause ends with FFmpeg's own line, because "unknown failure" alone is
// a message nobody can act on.
func (r Reason) Sentence() string {
	s := r.Cause.Label()
	if r.Held {
		s += fmt.Sprintf("; held after %d quick rejections, press Retry once it is fixed", r.Rejections)
	}
	if r.KeyChanged {
		s += "; the connected platform account reports a different stream key, refresh it"
	}
	if r.Cause == Unknown && r.Line != "" {
		s += ": " + r.Line
	}
	return s
}
