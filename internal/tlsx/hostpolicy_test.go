package tlsx

import (
	"context"
	"testing"
)

/* AN INTERNATIONALISED HOSTNAME COULD NEVER GET A CERTIFICATE.
 *
 * hostPolicy compares the configured name against the SNI the client sent, and
 * normalizeHost only lowercased and trimmed a trailing dot. But a client
 * connecting to a name with non-ASCII in it sends SNI ALREADY CONVERTED to
 * punycode, while the operator configured the readable form. Two spellings of
 * one name, compared as strings, never equal -- so the policy refused to
 * request a certificate for the only hostname it was configured with, and told
 * the operator so using a punycode string they never typed.
 */
func TestTheHostPolicyAcceptsAnInternationalisedNameInEitherSpelling(t *testing.T) {
	const unicode = "münchen.example"
	const punycode = "xn--mnchen-3ya.example"

	for _, configured := range []string{unicode, punycode} {
		p := hostPolicy(configured)
		for _, arriving := range []string{unicode, punycode} {
			if err := p(context.Background(), arriving); err != nil {
				t.Errorf("configured as %q, SNI %q: refused (%v) — these are two "+
					"spellings of one hostname, and a browser only ever sends the "+
					"punycode one", configured, arriving, err)
			}
		}
	}
}

// The policy exists to be strict. Canonicalising must not widen it.
func TestTheHostPolicyStillRefusesEverythingElse(t *testing.T) {
	p := hostPolicy("stream.example")
	for _, host := range []string{
		"other.example",
		"stream.example.attacker.test",
		"xn--mnchen-3ya.example",
		"",
	} {
		if err := p(context.Background(), host); err == nil {
			t.Errorf("accepted %q — anyone who can reach the port could burn this "+
				"box's Let's Encrypt rate limit", host)
		}
	}
}

// Case and a trailing dot are the same name, and were already handled.
func TestTheHostPolicyIgnoresCaseAndTheRootDot(t *testing.T) {
	p := hostPolicy("Stream.Example")
	for _, host := range []string{"stream.example", "STREAM.EXAMPLE", "stream.example."} {
		if err := p(context.Background(), host); err != nil {
			t.Errorf("refused %q: %v", host, err)
		}
	}
}
