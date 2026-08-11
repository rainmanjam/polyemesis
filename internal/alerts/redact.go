package alerts

import (
	"net/url"
	"regexp"
	"strings"
)

// Mask replaces anything that must not leave this process.
const Mask = "[redacted]"

// secretParam is a query parameter, form key or field label whose value is a
// credential. Matched case-insensitively with separators stripped, so
// stream_key, streamKey and STREAMKEY are all the same name.
var secretParam = map[string]bool{
	"key": true, "streamkey": true, "streamid": true, "streamname": true,
	"token": true, "accesstoken": true, "refreshtoken": true, "idtoken": true,
	"password": true, "passwd": true, "pass": true, "passphrase": true,
	"secret": true, "clientsecret": true, "apikey": true, "auth": true,
	"authorization": true, "signature": true, "sig": true, "credential": true,
}

// normalizeParam strips the separators that distinguish stream_key from
// streamKey so one table entry covers every spelling.
func normalizeParam(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SecretName reports whether a label names a credential, so a caller that
// builds fields from arbitrary data can drop the value rather than mask it.
func SecretName(s string) bool { return secretParam[normalizeParam(s)] }

// keyCarrying are the schemes whose last path segment is a stream key. An RTMP
// URL is host + application + key, and the key is the part that lets a stranger
// take over the broadcast.
var keyCarrying = map[string]bool{
	"rtmp": true, "rtmps": true, "rtsp": true, "rtsps": true,
	"srt": true, "udp": true, "rtp": true,
}

// urlish finds anything that looks like a URL inside free text. Deliberately
// greedy about the trailing characters a sentence would add, so a key at the
// end of "publishing to rtmp://host/app/KEY." is still masked.
var urlish = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

// bareSecret catches a credential written as key=value or key: value outside a
// URL, which is how it would arrive if it came from an FFmpeg log line.
var bareSecret = regexp.MustCompile(`(?i)\b(stream[_-]?key|stream[_-]?id|api[_-]?key|access[_-]?token|refresh[_-]?token|passphrase|password|secret|token|authorization)\b\s*[:=]\s*("[^"]*"|'[^']*'|\S+)`)

// bearerToken catches an HTTP credential, where the scheme word sits between
// the label and the secret and would otherwise be all that gets masked.
var bearerToken = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)

// RedactURL masks the credential parts of a URL while leaving enough of it for
// an operator to recognise which endpoint is meant.
//
// It is conservative in the direction that matters: an unparseable string is
// masked past the authority rather than passed through, because the failure
// mode of guessing wrong here is a stream key in somebody's Slack channel.
func RedactURL(raw string) string {
	trimmed, trailer := splitTrailer(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return maskUnparseable(trimmed) + trailer
	}
	if u.User != nil {
		u.User = url.User(Mask)
	}
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			if secretParam[normalizeParam(k)] {
				q.Set(k, Mask)
			}
		}
		u.RawQuery = q.Encode()
	}
	if keyCarrying[strings.ToLower(u.Scheme)] {
		u.Path = maskLastSegment(u.Path)
	}
	return unescapeMask(u.String()) + trailer
}

// unescapeMask puts the mask back the way a human reads it. url.String escapes
// the brackets, and "%5Bredacted%5D" in a Slack message looks like a bug rather
// than a deliberate omission.
func unescapeMask(s string) string {
	for _, escaped := range []string{url.QueryEscape(Mask), url.PathEscape(Mask)} {
		if escaped != Mask {
			s = strings.ReplaceAll(s, escaped, Mask)
		}
	}
	return s
}

// RedactWebhookURL masks everything after the host. Used for a rule's own
// endpoint: a Slack or Discord webhook URL carries its secret in the PATH, so
// nothing below the host may ever be shown or logged.
func RedactWebhookURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return Mask
	}
	return u.Scheme + "://" + u.Host + "/" + Mask
}

// maskLastSegment blanks the final path element when there is an application
// above it, which is the shape every RTMP ingest uses. A single-segment path is
// the application on its own and carries no key, so it survives.
//
// There is deliberately NO "skip a segment that is already the mask" guard
// here: the assignment below writes Mask, so a segment that already equals Mask
// is unchanged by definition. The double-mask this looked like it should fix
// came from splitTrailer eating the mask's closing bracket before this function
// ever saw it, and it is fixed there.
// RedactURLForPrincipal is RedactURL for a reader who must see NO credential,
// as opposed to an operator reading a diagnostic.
//
// The difference is every path segment, and it exists because the two callers
// want opposite things. A diagnostic wants to stay readable: blanking the path
// of every URL in every log line destroys the message, which is what
// TestWebhookPathIsNotDisclosed's own comment warns about, and it is why
// RedactURL masks only the LAST segment and only for the schemes that put a key
// there. A response body handed to a read-scoped token wants the opposite: it
// must not carry a credential, and NOTHING IN A URL SAYS WHICH SEGMENT IS ONE.
//
// Measured, and this is the bug it closes. An HLS pull URL puts the credential
// in the MIDDLE and the filename last:
//
//	https://cdn.example/live/SUPERSECRETPATHSEG/stream1/index.m3u8
//	RedactURL -> unchanged. https is not in keyCarrying, so no path masking runs
//	             at all; and where it does run it masks index.m3u8, the one
//	             segment that is not a secret.
//
// GET /api/v1/sources handed that verbatim to a read-scoped bearer, through
// readSafeIngest -> maskURL -> RedactURL, while the identical credential was
// correctly masked on GET /api/v1/processes. Same class as #229 one layer up:
// a mask built from where credentials usually live rather than from what the
// URL carries.
//
// Every segment, every scheme. Over-masking a path a low-privilege reader was
// never entitled to costs nothing; under-masking it is the disclosure.
func RedactURLForPrincipal(raw string) string {
	trimmed, trailer := splitTrailer(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return maskUnparseable(trimmed) + trailer
	}
	if u.User != nil {
		u.User = url.User(Mask)
	}
	if q := u.Query(); len(q) > 0 {
		// EVERY parameter, not the ones in secretParam. Same argument as the
		// path: CDN pull URLs use authcode, hdnts and policy, none of which are
		// in that table, and the table is a list of names somebody remembered.
		for k := range q {
			q.Set(k, Mask)
		}
		u.RawQuery = q.Encode()
	}
	parts := strings.Split(u.Path, "/")
	for i, seg := range parts {
		if seg != "" {
			parts[i] = Mask
		}
	}
	u.Path = strings.Join(parts, "/")
	return u.String() + trailer
}

func maskLastSegment(path string) string {
	parts := strings.Split(path, "/")
	last := -1
	nonEmpty := 0
	for i, p := range parts {
		if p != "" {
			nonEmpty++
			last = i
		}
	}
	if nonEmpty < 2 {
		return path
	}
	parts[last] = Mask
	return strings.Join(parts, "/")
}

// maskUnparseable keeps the scheme and whatever looks like a host, and blanks
// the rest.
func maskUnparseable(raw string) string {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return Mask
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		return raw[:i+3] + rest[:j] + "/" + Mask
	}
	return raw
}

// splitTrailer peels the punctuation a sentence puts after a URL, so it is not
// swallowed into the masked path and does not break url.Parse.
//
// It stops at a mask THIS package already wrote. Mask ends in ']', which is
// also sentence punctuation, so peeling it left maskLastSegment looking at
// "[redacted" -- a segment it did not recognise, masked again, and the trailing
// ']' put back afterwards: "rtmps://h/app/[redacted]]". That is not cosmetic
// noise from a contrived input, it is what the MQTT path produces every time,
// because Process.scrub runs SecretSet.Scrub (which writes the mask) and THEN
// Redact (which reads it back) over the same string. See
// TestRedactIsIdempotentOverItsOwnMask.
func splitTrailer(raw string) (string, string) {
	end := len(raw)
	for end > 0 && strings.ContainsRune(".,;:)]}!?", rune(raw[end-1])) {
		if raw[end-1] == ']' && strings.HasSuffix(raw[:end], Mask) {
			break
		}
		end--
	}
	return raw[:end], raw[end:]
}

// Redact scrubs free text: every URL in it, plus any credential written as a
// bare key=value pair.
//
// IT IS A RESIDUAL PASS, NOT A BOUNDARY. The boundary is alerts.SecretSet,
// which removes the EXACT literals a process was configured with and cannot be
// defeated by how they were spelled. Redact runs after that, over the same
// bytes, for the credentials nobody could have declared: a token an endpoint
// echoed back, a URL FFmpeg synthesised from parts. Anything that treats a
// Redact call as the reason a sink is safe is wrong, and the four limits below
// are why. All four are MEASURED and pinned by TestRedactKnownResiduals and
// TestRedactPerElementIsStrictlyWorse in redact_test.go.
//
//  1. THE GRAMMAR IS `label SEP value`, SEP in {':', '='}, and the label side is
//     a CLOSED table (bareSecret / secretParam). FFmpeg's grammar is
//     `-flag SP value` over an OPEN option namespace, and it is invisible here:
//
//     Redact("-passphrase KEY")   == "-passphrase KEY"     // unchanged
//     Redact("-streamid KEY")     == "-streamid KEY"       // unchanged
//     Redact("-rtmp_conn S:KEY")  == "-rtmp_conn S:KEY"    // unchanged
//
//     `passphrase` and `streamid` ARE both in the table. They still leak. The
//     failure is GRAMMATICAL, not lexical, so ENLARGING THE TABLE CANNOT FIX
//     IT -- the set of FFmpeg flag spellings is not enumerable, and each new
//     regex only moves the boundary of a thing that has no boundary.
//
//  2. IT DOES NOT MASK AN https PATH SEGMENT. Only the keyCarrying schemes
//     (rtmp/rtmps/rtsp/rtsps/srt/udp/rtp) have their last path element blanked,
//     because for those the path IS the stream key. A Slack or Discord webhook
//     carries its secret in an https path and survives untouched:
//
//     Redact("https://host/a/b/KEY") == "https://host/a/b/KEY"
//
//     Use RedactWebhookURL for that shape. Running Redact over a webhook URL,
//     or over an error wrapping one, is a NO-OP and must never be recorded as
//     a fix; see alerts.ClientErrorText.
//
//  3. IT DOES NOT SEE JSON. `{"token":"KEY"}` is unchanged: the quotes sit
//     between the separator and the value, so the bare-pair regex does not
//     reach it, and a third party's error body is exactly where that shape
//     arrives.
//
//  4. APPLYING IT PER ELEMENT OF AN ARGV IS STRICTLY WORSE THAN APPLYING IT TO
//     THE JOINED TEXT, AND NEVER BETTER. Its matches are substrings of the
//     input, so splitting on whitespace can only sever a label from its value,
//     never create a match. Three shapes measurably differ, all of them
//     `label: value` where the split lands on the space:
//
//     "Authorization: Bearer KEY"  whole -> masked   per-element -> LEAKED
//     "Authorization: Basic KEY"   whole -> masked   per-element -> LEAKED
//     "token: KEY"                 whole -> masked   per-element -> LEAKED
//
//     This is the bug #150 fixed structurally rather than lexically: argv
//     egresses now go through supervisor.Spec.Secrets, and a new caller looping
//     Redact over tokens re-creates it. TestRedactIsCalledOnlyFromTheAllowlist
//     fails the build on a bare-Redact caller outside a short, reasoned list.
//
// The correct reading of a Redact call is "the declared secrets are already
// gone; this is the best-effort pass over what is left".
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = urlish.ReplaceAllStringFunc(s, RedactURL)
	// Before the key=value pass, so "Authorization: Bearer xyz" loses the token
	// rather than only the word "Bearer".
	s = bearerToken.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Fields(m)[0] + " " + Mask
	})
	return bareSecret.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.IndexAny(m, ":=")
		if i < 0 {
			return Mask
		}
		return m[:i+1] + " " + Mask
	})
}

// Redacted returns a copy of the event with nothing secret left in it.
//
// Applied by Publish rather than left to callers: the engine builds these
// events in a dozen places and exactly one of them has to be careless for a
// stream key to end up in a chat room that outlives the broadcast.
func (e Event) Redacted() Event {
	e.Title = Redact(e.Title)
	e.Text = Redact(e.Text)
	if len(e.Fields) == 0 {
		return e
	}
	fields := make([]Field, len(e.Fields))
	for i, f := range e.Fields {
		if SecretName(f.Name) {
			fields[i] = Field{Name: f.Name, Value: Mask}
			continue
		}
		fields[i] = Field{Name: f.Name, Value: Redact(f.Value)}
	}
	e.Fields = fields
	return e
}
