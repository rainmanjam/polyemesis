package alerts

import (
	"errors"
	"net/url"
	"strings"
)

// ClientErrorText renders an outbound HTTP error for a place an operator will
// read it, with the endpoint's PATH removed and everything else kept.
//
// THE PROBLEM IT SOLVES. net/http wraps every transport failure in a
// *url.Error, whose Error() is `Op "FULL URL": inner`. The full URL. A Slack or
// Discord webhook carries its ENTIRE credential in that path --
// https://hooks.slack.com/services/T00/B00/XXXXXXXX -- so one DNS blip put a
// working webhook secret into Notifier.Stats.LastError, which is served
// verbatim at GET /api/v1/alerts/meta, which a READ-SCOPED token may call. That
// is a read token escalating to "can post into your Slack".
//
// WHY NOT Redact. Running alerts.Redact over this string is A NO-OP and must
// never be recorded as a fix: Redact only masks the last path segment of the
// KEY-CARRYING schemes (rtmp, srt, ...), because for those the path IS the
// stream key. An https path is left alone deliberately -- masking every https
// path would blank half of every diagnostic in the process. See limit 2 on the
// doc for Redact, and TestRedactKnownResiduals which pins it.
//
// WHY NOT JUST THE HOST. Returning "delivery failed" or RedactedURL() alone was
// the other candidate and it is worse than it looks. The three failures an
// operator has to tell apart -- a name that does not resolve, a connection
// refused, a certificate that does not verify -- differ ONLY in the inner
// error's wording. Dropping the Op prefix and the inner text turns every
// support conversation into "it says delivery failed". So the shape is
// preserved exactly, with one substitution:
//
//	Post "https://hooks.slack.com/services/T00/B00/SECRET": dial tcp: ...
//	Post https://hooks.slack.com/[redacted]: dial tcp: ...
//
// The host survives because the host is not the secret and is the first thing
// anyone needs.
//
// THIS IS NOT THE WHOLE FIX. It handles the wrapper, which is where the URL
// arrives by construction. A credential the ENDPOINT chose to echo back in its
// own error body is a different shape and is covered by the per-caller
// alerts.SecretSet at the call site. Both layers, in that order.
func ClientErrorText(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err.Error()
	}

	inner := ""
	if ue.Err != nil {
		inner = ue.Err.Error()
	}
	// Belt and braces: an inner error is free to quote the URL back (a redirect
	// error names the target, for one), and that copy is not covered by
	// rebuilding the wrapper. Replaced by value, which needs no grammar.
	if ue.URL != "" {
		inner = strings.ReplaceAll(inner, ue.URL, RedactWebhookURL(ue.URL))
	}

	out := RedactWebhookURL(ue.URL)
	if ue.Op != "" {
		out = ue.Op + " " + out
	}
	if inner != "" {
		out += ": " + inner
	}
	return out
}

// EndpointSecrets are the parts of a webhook endpoint that are a CREDENTIAL, as
// exact literals for a SecretSet.
//
// The HOST IS NOT INCLUDED, and that is the whole design of this function. It
// would be easy to seed the set with the whole URL and be done, and it would
// make every diagnostic useless: "cannot reach [redacted]" does not tell an
// operator whether their Slack workspace is down or their DNS is. The host is
// the first thing anyone needs and it is not secret -- Rule.RedactedURL and
// Hook.RedactedURL both publish it deliberately.
//
// What IS returned:
//
//   - The whole path+query as one literal. This is the shape an endpoint echoes
//     when it quotes the request line back at you.
//   - The LAST path segment on its own. For every webhook provider this is the
//     credential proper -- Slack's B00/XXXXXXXX tail, Discord's token -- and it
//     is what a "no such webhook" body tends to name. The earlier segments are
//     deliberately NOT returned: "services" is nine characters, clears
//     MinSecretLen, and masking that word would blind the log for nothing.
//   - Every query VALUE, for the providers that put the token there instead.
//
// Nothing here is a guess about what the text will look like: these are exact
// strings, and SecretSet removes them by value wherever they appear. Short ones
// are refused by NewSecretSet at the floor, which is the documented residual.
func EndpointSecrets(raw string) []string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil
	}
	var out []string
	if pq := u.EscapedPath(); pq != "" && pq != "/" {
		full := pq
		if u.RawQuery != "" {
			full += "?" + u.RawQuery
		}
		out = append(out, full, pq)

		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if last := segs[len(segs)-1]; last != "" {
			out = append(out, last)
		}
	}
	for _, vs := range u.Query() {
		out = append(out, vs...)
	}
	return out
}
