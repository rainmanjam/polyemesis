package alerts

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The Slack shape: the ENTIRE credential is the path. Anything that can post to
// this URL can post into the workspace, and nothing about the host says so.
const (
	slackHost   = "https://hooks.slack.com"
	slackPath   = "/services/T00000000/B00000000/xoxbSECRETPATHmaterial99"
	slackURL    = slackHost + slackPath
	slackSecret = "xoxbSECRETPATHmaterial99"
)

// transportFailures are the four shapes net/http hands back, each wrapped the
// way net/http wraps them: in a *url.Error carrying the FULL request URL.
//
// FOUR, not one. The earlier round measured only a DNS failure and concluded
// the field was fine, which it was not -- every one of these is a *url.Error
// and every one of them therefore carried the path. They are also the four an
// operator has to be able to tell apart, which is the reason the fix rebuilds
// the wrapper instead of replacing the text with a class code.
func transportFailures() []struct {
	name  string
	err   error
	inner string
} {
	return []struct {
		name  string
		err   error
		inner string
	}{
		{
			name:  "DNS: the name does not resolve",
			err:   &url.Error{Op: "Post", URL: slackURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "hooks.slack.com"}}},
			inner: "no such host",
		},
		{
			name:  "refused: nothing is listening",
			err:   &url.Error{Op: "Post", URL: slackURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}},
			inner: "connection refused",
		},
		{
			name:  "timeout: the endpoint accepted and went quiet",
			err:   &url.Error{Op: "Post", URL: slackURL, Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")},
			inner: "Client.Timeout",
		},
		{
			name:  "TLS: the certificate does not verify",
			err:   &url.Error{Op: "Post", URL: slackURL, Err: x509.UnknownAuthorityError{}},
			inner: "x509",
		},
	}
}

// TestClientErrorTextKeepsTheHostAndLosesThePath is the unit of the fix.
func TestClientErrorTextKeepsTheHostAndLosesThePath(t *testing.T) {
	for _, tc := range transportFailures() {
		t.Run(tc.name, func(t *testing.T) {
			// The positive control: what the field held BEFORE, so a green
			// assertion below cannot be green because there was nothing to
			// leak.
			if !strings.Contains(tc.err.Error(), slackSecret) {
				t.Fatalf("the fixture does not reproduce the leak: %v", tc.err)
			}

			got := ClientErrorText(tc.err)
			if strings.Contains(got, slackSecret) || strings.Contains(got, slackPath) {
				t.Errorf("ClientErrorText leaked the webhook path: %q", got)
			}
			if !strings.Contains(got, "hooks.slack.com") {
				t.Errorf("ClientErrorText dropped the HOST: %q. The host is not the "+
					"secret and it is the first thing an operator needs -- "+
					"Rule.RedactedURL publishes it deliberately.", got)
			}
			if !strings.Contains(got, tc.inner) {
				t.Errorf("ClientErrorText lost the inner wording %q: %q. The four "+
					"transport failures differ ONLY in this text; collapsing them "+
					"turns every support conversation into \"it says delivery failed\".",
					tc.inner, got)
			}
			if !strings.HasPrefix(got, "Post ") {
				t.Errorf("ClientErrorText dropped the Op prefix: %q", got)
			}
		})
	}

	t.Run("a non-url.Error is passed through unchanged", func(t *testing.T) {
		err := errors.New("endpoint returned 404")
		if got := ClientErrorText(err); got != err.Error() {
			t.Errorf("ClientErrorText(%v) = %q, want it unchanged: there is no URL "+
				"wrapper here and inventing one would lose the message", err, got)
		}
	})

	t.Run("nil is empty", func(t *testing.T) {
		if got := ClientErrorText(nil); got != "" {
			t.Errorf("ClientErrorText(nil) = %q, want \"\"", got)
		}
	})
}

// TestRedactIsANoOpOnAWebhookPathSecret states, executably, why the obvious fix
// is not a fix.
//
// "Also run Redact on LastError" was proposed and is a NO-OP on exactly the
// shape that matters. This test exists so nobody records it as coverage.
func TestRedactIsANoOpOnAWebhookPathSecret(t *testing.T) {
	raw := (&url.Error{Op: "Post", URL: slackURL, Err: errors.New("connection refused")}).Error()
	if got := Redact(raw); !strings.Contains(got, slackSecret) {
		t.Fatalf("Redact now masks an https path segment (%q). That is a behaviour change "+
			"with much wider consequences than this test -- it would blank half of every "+
			"diagnostic in the process -- so verify it was intended before updating "+
			"this row, and re-read limit 2 on the doc for Redact.", got)
	}
}

// TestNotifierStatsNeverDisclosesTheWebhookPath drives the REAL Notifier
// delivery loop and reads the field the API serves.
//
// Notifier.Stats().LastError is what GET /api/v1/alerts/meta returns under
// "stats", and that route is in the ordinary authenticated group -- a
// READ-SCOPED token may call it. So a webhook secret landing in this field is a
// read token that can post into somebody's Slack, which is the whole of the
// escalation.
func TestNotifierStatsNeverDisclosesTheWebhookPath(t *testing.T) {
	for _, tc := range transportFailures() {
		t.Run(tc.name, func(t *testing.T) {
			rule := testRule(func(r *Rule) {
				r.URL = slackURL
				r.Format = FormatSlack
				r.DebounceSeconds = MinDebounceSeconds
				r.MinIntervalSeconds = MinIntervalSeconds
			})
			doer := &fakeDoer{replies: []reply{{err: tc.err}}}
			n := New(quietLog(),
				RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
				WithDoer(doer), WithRetry(1, time.Millisecond, time.Millisecond),
				WithFlushInterval(2*time.Millisecond))

			// The whole real path, minus the clock: Add, Flush, the sender
			// channel, and deliver -- which is the only writer of
			// Stats.LastError and the thing under test. What is asserted is
			// what deliver RECORDS, not when it runs, so the ticker buys
			// nothing here and the debounce is stepped over by handing Flush a
			// later time.
			//
			// Run is deliberately NOT started. The coalescer is
			// single-goroutine by design (see its doc), so feeding it directly
			// beside a live Run is a data race on its maps by construction --
			// #249, which fired on a PR that does not touch this package.
			// Calling deliver here instead of waiting for Run's sender to get
			// round to it also makes the assertion below unconditional rather
			// than a three-second poll.
			now := time.Now()
			n.co.Add([]Rule{rule}, downEvent("1", now), now)
			n.Flush(now.Add(time.Duration(rule.DebounceSeconds+1) * time.Second))

			var d Delivery
			select {
			case d = <-n.send:
			default:
				t.Fatal("the flush handed the sender nothing, so deliver never ran " +
					"and this test asserted nothing about Stats.LastError")
			}
			n.deliver(context.Background(), d)

			last := n.Stats().LastError
			if last == "" {
				t.Fatal("no delivery failure was recorded, so this test asserted nothing")
			}
			if strings.Contains(last, slackSecret) || strings.Contains(last, slackPath) {
				t.Errorf("Stats.LastError = %q, which carries the webhook path. This field "+
					"is served at GET /api/v1/alerts/meta to a READ-scoped token.", last)
			}
			if !strings.Contains(last, "hooks.slack.com") {
				t.Errorf("Stats.LastError = %q lost the host; an operator cannot tell which "+
					"rule failed", last)
			}
			if !strings.Contains(last, tc.inner) {
				t.Errorf("Stats.LastError = %q lost the inner wording %q", last, tc.inner)
			}
		})
	}
}

// TestNotifierTestReturnsAMaskedError covers the admin-only route whose handler
// comment claims "errors out of the notifier are already redacted".
func TestNotifierTestReturnsAMaskedError(t *testing.T) {
	for _, tc := range transportFailures() {
		t.Run(tc.name, func(t *testing.T) {
			doer := &fakeDoer{replies: []reply{{err: tc.err}}}
			n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
				WithDoer(doer), WithRetry(1, time.Millisecond, time.Millisecond))

			err := n.Test(context.Background(), Rule{ID: 1, Name: "ops", URL: slackURL, Format: FormatSlack})
			if err == nil {
				t.Fatal("Test returned no error against a failing endpoint")
			}
			if strings.Contains(err.Error(), slackSecret) {
				t.Errorf("Notifier.Test returned %q, which handleTestAlertRule writes "+
					"straight into a 502 body under a comment claiming it is already "+
					"redacted", err)
			}
			if !strings.Contains(err.Error(), "hooks.slack.com") {
				t.Errorf("Notifier.Test returned %q, which no longer names the endpoint", err)
			}
		})
	}
}

// TestEndpointSecretsExcludesTheHost pins the seeding decision.
//
// Seeding a SecretSet with the WHOLE URL is the easy version and it destroys
// the diagnostic: "cannot reach [redacted]" does not distinguish a Slack outage
// from a DNS problem. The host is published deliberately by RedactedURL, so it
// must not be in the set.
func TestEndpointSecretsExcludesTheHost(t *testing.T) {
	got := EndpointSecrets(slackURL)
	if len(got) == 0 {
		t.Fatal("EndpointSecrets returned nothing for a Slack webhook URL")
	}
	for _, lit := range got {
		if strings.Contains(lit, "hooks.slack.com") {
			t.Errorf("EndpointSecrets returned %q, which contains the HOST. Masking the "+
				"host by value would blank it everywhere it appears, including in the "+
				"RedactedURL this package publishes on purpose.", lit)
		}
	}

	set := NewSecretSet(nil, got...)
	if set.Len() == 0 {
		t.Fatal("every literal was refused, so the set masks nothing")
	}
	// The path and its credential tail both go.
	for _, bad := range []string{slackPath, slackSecret} {
		if out := set.Scrub("the endpoint said: unknown webhook " + bad); strings.Contains(out, bad) {
			t.Errorf("the set did not remove %q: %q", bad, out)
		}
	}
	// "services" is a path segment and is NOT a credential; masking it would
	// blind the log for nothing. Only the LAST segment is seeded.
	if out := set.Scrub("checking services status"); out != "checking services status" {
		t.Errorf("the set masked an ordinary path word: %q", out)
	}

	// A query-carried token is the other provider shape.
	q := NewSecretSet(nil, EndpointSecrets("https://example.test/hook?token=QUERYSECRET123456")...)
	if out := q.Scrub(`{"error":"bad token QUERYSECRET123456"}`); strings.Contains(out, "QUERYSECRET123456") {
		t.Errorf("a query-carried credential survived: %q", out)
	}
}
