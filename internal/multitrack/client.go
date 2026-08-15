package multitrack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client fetches a negotiated configuration. The zero value talks to the real
// Twitch endpoint over the default HTTP client, which is what production wants.
type Client struct {
	// HTTP is the transport. Nil means defaultHTTP, whose timeout is the point:
	// this call happens at go-live, between the operator pressing the button and
	// anything reaching a viewer, so a hung platform must not hold the broadcast
	// open indefinitely. obs-studio allows five seconds for the same call.
	HTTP *http.Client
	// BaseURL overrides ConfigURL. It is the ONLY seam here and it exists for
	// tests, in the shape internal/oauth/endpoints.go settled on: one field that
	// moves every call this type makes, because a partially redirected client is
	// one that looks stubbed and is not. Nothing at runtime sets it.
	BaseURL string
}

// defaultTimeout is deliberately short. See Client.HTTP.
const defaultTimeout = 10 * time.Second

var defaultHTTP = &http.Client{
	Timeout:       defaultTimeout,
	CheckRedirect: refuseRedirect,
}

// refuseRedirect stops every redirect on the configuration POST.
//
// THE STREAM KEY IS IN THE BODY, which is what makes this different from the
// two redirect policies already in this repo. net/http strips Authorization and
// Cookie when a redirect crosses to another host, so a client whose credential
// rides in a header is protected by the standard library. Ours does not: Fetch
// puts the key in the JSON body precisely so it stays out of URLs and proxy
// logs, and net/http has no notion of a sensitive body.
//
// Measured against two httptest servers, redirecting a Fetch to a second host:
//
//	301, 302, 303  ->  the hop becomes a GET, no body, no leak
//	307, 308       ->  the hop stays a POST and net/http REPLAYS the body
//	                   through Request.GetBody -- 684 bytes of request JSON
//	                   arrived at the second server, stream key included
//
// So the leak needs no interception and no downgrade. One 307 from anything
// answering for ingest.twitch.tv hands the operator's stream key to a host of
// its choosing, and Go's default of following up to ten of them is what makes
// it a single response rather than an attack.
//
// REFUSED OUTRIGHT RATHER THAN CONSTRAINED TO THE SAME HOST, which is the
// weaker policy that would also have closed this. ConfigURL is a const for the
// reason it states -- "a configurable platform host is a partially-redirected
// provider waiting to happen" -- and no measured response has ever redirected.
// A same-host allowance would be an untested branch guarding a credential.
//
// Returning http.ErrUseLastResponse rather than an error is deliberate: it
// hands Fetch the 3xx response to deal with, and the status check there reports
// it as a refusal an operator can read, rather than burying it in a *url.Error
// that the caller must scrub. Nothing follows the Location header either way.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// http returns the transport to use, WITH the redirect policy applied whether
// or not the caller supplied a client.
//
// The policy is reapplied to an injected client rather than left to it, and
// that is the whole reason this is not a two-line accessor. HTTP is described
// above as the seam, and a seam that quietly drops a security policy is a test
// suite that proves the wrong thing: every test here passes httptest's
// srv.Client(), which has no CheckRedirect, so a policy that lived only on
// defaultHTTP would be a policy no test could exercise and no test could
// protect. The same argument BaseURL's comment makes about partial redirection
// applies to the transport.
//
// A SHALLOW COPY, so the caller's client does not grow our hook -- the pattern
// transcribe.download already uses for the same reason. Overwriting
// CheckRedirect on a shared *http.Client would change the behaviour of every
// other request made through it.
func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return defaultHTTP
	}
	cp := *c.HTTP
	cp.CheckRedirect = refuseRedirect
	return &cp
}

func (c *Client) url() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return ConfigURL
}

// Fetch negotiates a configuration.
//
// The stream key is a separate argument rather than a field on req that the
// caller fills in, and that is not ceremony. It is the one value in this
// exchange that must never be logged, and having exactly one function put it
// into the body means there is exactly one place to audit. It also gives Fetch
// the literal it needs in order to scrub the key out of every error it returns
// -- including the ones it did not construct, like a transport error carrying a
// URL, or a JSON decode error carrying a fragment of the response.
//
// A non-nil Config with a Refused verdict is a SUCCESSFUL call: Twitch answered
// and said no. The error return is for "no answer at all". Callers distinguish
// them because the two demand different things -- a refusal is reported to the
// operator and the ordinary ingest is used; a transport failure is the same
// outcome but is not the operator's to fix.
func (c *Client) Fetch(ctx context.Context, streamKey string, req Request) (*Config, error) {
	req.Authentication = streamKey
	req.Service = ServiceIVS
	req.SchemaVersion = SchemaVersion

	body, err := json.Marshal(req)
	if err != nil {
		// Cannot carry the key in practice -- json.Marshal fails on unsupported
		// types, not on values -- but scrubbed anyway, because "cannot in
		// practice" is what every one of these leaks was before it happened.
		return nil, fmt.Errorf("build multitrack configuration request: %s", scrub(err.Error(), streamKey))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build multitrack configuration request: %s", scrub(err.Error(), streamKey))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ask Twitch for a multitrack configuration: %s", scrub(err.Error(), streamKey))
	}
	defer resp.Body.Close()

	// Bounded. The measured responses are around 1-3 KB; a megabyte is four
	// hundred times that and still cannot exhaust a go-live handler.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read the multitrack configuration response: %s", scrub(err.Error(), streamKey))
	}

	// A non-200 is still checked, even though the whole point of this package is
	// that 200 is not the verdict. The two statements are not in tension: 200
	// does not mean yes, but a 5xx means Twitch never got as far as forming an
	// opinion, and decoding one as a Config would produce a zero-valued
	// negotiation that Verdict would then have to reason about.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// SCRUB OUTERMOST, TRUNCATE INSIDE IT. This read
		//
		//	scrub(snippet(raw), streamKey)
		//
		// which cuts the body at 300 bytes and only THEN looks for the key, so a
		// key straddling offset 300 is never matched: snippet hands scrub a
		// haystack that ends in the middle of the needle, scrub finds nothing,
		// and the surviving prefix goes into the error. Measured with a 55-byte
		// key at offset 270: 30 bytes of it leaked, and no placeholder appeared
		// anywhere in the line -- so the output did not even LOOK redacted,
		// which is the property that makes it survive review.
		//
		// This is #306's failure mode exactly -- a needle longer than the
		// haystack it is searched in -- reproduced in the newest code in the
		// repository, three commits after that issue was closed. There it was a
		// 65-byte configured value against a 56-byte printed one; here it is a
		// whole key against the truncated tail of one. Same shape, different
		// route in.
		//
		// THE RULE, stated so the next transform added here does not have to
		// rediscover it: SCRUB IS ONLY SOUND AS THE OUTERMOST TRANSFORM. It is a
		// literal substring replace, so anything that runs after it can reveal
		// what it removed, and anything that runs BEFORE it can destroy the
		// match it depends on -- truncating, escaping, re-encoding, wrapping,
		// case-folding. Every other scrub call in this function obeys that
		// already: they scrub err.Error(), which is the last thing that happens
		// to those strings. This was the only one with a transform underneath
		// it, and the fix is to put the transform inside.
		return nil, fmt.Errorf("Twitch returned %d to the multitrack configuration request: %s",
			resp.StatusCode, snippet([]byte(scrub(string(raw), streamKey))))
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode the multitrack configuration response: %s", scrub(err.Error(), streamKey))
	}
	return &cfg, nil
}

// snippet bounds an error message. Borrowed in spirit from oauth.snippet.
//
// IT MUST BE GIVEN ALREADY-SCRUBBED TEXT, not text to be scrubbed afterwards.
// The doc comment here used to say the opposite -- "the caller scrubs whatever
// comes back" -- and that instruction is what the leak was: truncating first
// gives the scrub a haystack shorter than its needle. See the rule written out
// at the call site in Fetch.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// ---------------------------------------------------------------- the verdict

// Verdict is what to DO with a configuration, which is not the same question as
// what Twitch's status field says. Three answers, because there are three
// different things a caller has to do.
type Verdict string

const (
	// Negotiated: publish to this configuration.
	Negotiated Verdict = "negotiated"
	// Advisory: publish to this configuration, and show the operator what Twitch
	// said. obs-studio puts a modal here and offers to abort; polyemesis has no
	// operator standing at the machine when a scheduled broadcast starts, so it
	// proceeds and reports.
	Advisory Verdict = "advisory"
	// Refused: do NOT publish to this configuration. Fall back to the ordinary
	// ingest and say why. This is the common answer on a host with no supported
	// GPU, which is most polyemesis hosts.
	Refused Verdict = "refused"
)

// Verdict reads the configuration and says what to do with it, with a sentence
// for the operator. The sentence is always populated for Advisory and Refused
// and is always empty for Negotiated -- there is nothing to say about a
// negotiation that worked.
//
// The mapping follows obs-studio's HandleGoLiveApiErrors, which is the only
// published interpretation of these values, with one addition and one
// substitution:
//
//   - ADDITION: a configuration with no video renditions or no live audio track
//     is Refused whatever the status field says. obs-studio reaches the same
//     outcome further downstream, by throwing out of create_encoders when a
//     list is empty. Deciding it here rather than there is what stops "status
//     was absent, therefore success" from ever being the last word: EVERY
//     measured refusal came back with empty lists, and on a response that
//     somehow carried no status at all the empty lists are the only signal
//     left. A guard that could pass while the thing it names is broken is the
//     failure mode; this one cannot, because the emptiness IS the breakage.
//
//   - SUBSTITUTION: obs-studio treats StatusResult::Error as fatal to the
//     broadcast. Here it is Refused, which is fatal to the MULTITRACK PATH
//     only. That is issue #326's scope item 5 and it is the right call for a
//     server: refusing to go live at all because an optional second audio track
//     could not be negotiated would trade a missing VOD mix for a missing
//     broadcast.
func (c *Config) Verdict() (Verdict, string) {
	if c == nil {
		return Refused, "Twitch returned no multitrack configuration."
	}

	verdict := Negotiated
	advice := ""

	if c.Status != nil {
		switch c.Status.Result {
		case StatusError:
			return Refused, c.explain("Twitch declined to configure Enhanced Broadcasting")
		case "", StatusSuccess:
			// The absent case and the explicit-success case are the same case.
			// A successful negotiation omits the status object entirely -- that
			// is measured, not assumed -- so the zero StatusResult has to mean
			// success or every good response would be read as a refusal.
		case StatusWarning:
			// A warning with nothing in the ladder is a refusal wearing a softer
			// word, and obs-studio treats it as fatal for that reason. The
			// emptiness check below reaches the same verdict, so this case only
			// has to set the advice.
			verdict, advice = Advisory, c.explain("Twitch configured Enhanced Broadcasting with a warning")
		default:
			// A result string this build does not know. Proceeding with a note
			// rather than refusing: obs-studio does the same, and a client that
			// refused on an unrecognised value would break the moment Twitch
			// added one.
			verdict = Advisory
			advice = fmt.Sprintf("Twitch returned an unrecognised status %q for Enhanced Broadcasting; "+
				"continuing with the configuration it sent.", c.Status.Result)
		}
	}

	// The addition. Deliberately after the switch, so it overrides Advisory too.
	if len(c.EncoderConfigurations) == 0 {
		return Refused, joinAdvice(advice,
			"Twitch returned no video renditions, so there is nothing to publish to the multitrack ingest.")
	}
	if len(c.AudioConfigurations.Live) == 0 {
		return Refused, joinAdvice(advice,
			"Twitch returned no live audio track, so there is nothing to publish to the multitrack ingest.")
	}
	return verdict, advice
}

// explain renders Twitch's own sentence, prefixed with ours so an operator
// reading a log knows which half is whose. The HTML is left as Twitch sent it
// rather than stripped: what arrives is a sentence with the occasional anchor,
// and a tag-stripper that got it wrong would silently eat the URL of the help
// page the sentence exists to point at.
func (c *Config) explain(prefix string) string {
	if c.Status == nil || c.Status.HTMLEnUS == "" {
		// Twitch is not obliged to send a reason and has a field for it that is
		// optional. Saying so beats an empty string, which reads as a bug.
		return prefix + ", and gave no reason."
	}
	return prefix + ": " + c.Status.HTMLEnUS
}

func joinAdvice(existing, added string) string {
	if existing == "" {
		return added
	}
	return existing + " " + added
}
