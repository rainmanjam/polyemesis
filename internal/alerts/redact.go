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
func splitTrailer(raw string) (string, string) {
	end := len(raw)
	for end > 0 && strings.ContainsRune(".,;:)]}!?", rune(raw[end-1])) {
		end--
	}
	return raw[:end], raw[end:]
}

// Redact scrubs free text: every URL in it, plus any credential written as a
// bare key=value pair.
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
