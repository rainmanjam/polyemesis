package alerts

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ssrfRule builds a rule aimed at rawURL with nothing else interesting on it.
func ssrfRule(name, rawURL string) Rule {
	return Rule{Name: name, Enabled: true, Format: FormatJSON, URL: rawURL}.Normalized()
}

// TestAlertRuleValidateRejectsLiteralPrivateTargetUnlessAllowed is the
// save-time half of the guard. Before it, POST /alerts/rules answered 201 for
// http://169.254.169.254/ and for http://127.0.0.1:9, and the operator only
// found out what was on the other side by pressing "test" (#607).
//
// The last case is the control. A guard that refused every address would pass
// every other case here while breaking every real webhook, so a public target
// must still be ACCEPTED for the rest of this test to mean anything.
func TestAlertRuleValidateRejectsLiteralPrivateTargetUnlessAllowed(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"cloud metadata", "http://169.254.169.254/latest/meta-data/"},
		{"loopback", "http://127.0.0.1:9/hook"},
		{"loopback, non-canonical", "http://127.53.1.2/hook"},
		{"RFC1918", "http://10.1.2.3:9000/hook"},
		{"RFC1918 172.16", "http://172.16.4.4/hook"},
		{"RFC1918 192.168", "http://192.168.1.1/hook"},
		{"CGNAT / Tailscale", "http://100.64.0.1/hook"},
		{"unspecified", "http://0.0.0.0/hook"},
		{"IPv6 loopback", "http://[::1]/hook"},
		{"IPv6 link-local", "http://[fe80::1]/hook"},
		{"IPv6 ULA", "http://[fc00::1]/hook"},
		{"IPv4-mapped loopback", "http://[::ffff:127.0.0.1]/hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ssrfRule("ssrf", tc.url).Validate()
			if err == nil {
				t.Fatalf("Validate accepted an alert rule targeting %s (%s), so "+
					"the rule editor is still a way to reach it", tc.name, tc.url)
			}
			if !strings.Contains(err.Error(), "non-public") {
				t.Fatalf("error = %q, want it to explain the target is non-public", err)
			}
			if !strings.Contains(err.Error(), "allowPrivateTarget") {
				t.Fatalf("error = %q, want it to name the opt-in, so an operator "+
					"who meant it knows what to do next", err)
			}
		})
	}

	// The opt-in lifts the refusal for the operator who genuinely wants a
	// self-hosted target. Without an escape hatch a guard just gets disabled.
	allowed := ssrfRule("lan", "http://10.1.2.3:9000/hook")
	allowed.AllowPrivateTarget = true
	if err := allowed.Validate(); err != nil {
		t.Fatalf("Validate rejected a private target with AllowPrivateTarget "+
			"set: %v -- the opt-in must actually work", err)
	}

	// THE CONTROL. A fix that refuses everything passes every case above.
	for _, ok := range []string{
		"https://hooks.example.com/services/T0/B1/xxxx",
		"http://8.8.8.8/hook",
		"http://[2001:4860:4860::8888]/hook",
	} {
		if err := ssrfRule("public", ok).Validate(); err != nil {
			t.Errorf("Validate rejected the ordinary public target %s: %v -- the "+
				"guard now blocks legitimate webhooks", ok, err)
		}
	}
}

// TestAlertNotifierRefusesToDialAPrivateTargetAtSendTime is the controlling
// half: the dial. Validate never resolves a hostname, so a NAME that resolves
// to a private address -- or that only starts to after the rule was saved, i.e.
// DNS rebinding -- reaches this guard instead. httptest binds 127.0.0.1, which
// is exactly the shape of address the guard exists to keep an alert off.
//
// It goes through Notifier.Test on purpose: that is POST
// /alerts/rules/{id}/test, the port-scan oracle named in #607, and it must be
// covered by the dial-time guard and not only by the create path.
func TestAlertNotifierRefusesToDialAPrivateTargetAtSendTime(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// The default Doer, deliberately: the guard lives on the shipped
	// transport, so replacing the client here would test nothing.
	n := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		RuleFunc(func() ([]Rule, error) { return nil, nil }))
	n.SetRetry(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// srv.URL is http://127.0.0.1:<port>, which Validate would also catch, so
	// the rule is handed straight to Test -- the way a row stored before this
	// guard existed reaches the notifier. That is the case that matters: such a
	// rule LOADS, and is refused here.
	blocked := ssrfRule("stored before the guard", srv.URL)
	err := n.Test(ctx, blocked)
	if err == nil {
		t.Fatal("Notifier.Test delivered to a loopback endpoint with no " +
			"allowPrivateTarget opt-in; POST /alerts/rules/{id}/test is still " +
			"an internal port-scan oracle")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want it to say the address is non-public", err)
	}
	if hits != 0 {
		t.Fatalf("the endpoint was reached %d times; the guard must refuse "+
			"BEFORE the connection, not report on it afterwards", hits)
	}

	// THE CONTROL, again, at the dial: with the opt-in the same rule delivers.
	// Without this, "refuse everything" would pass.
	allowed := blocked
	allowed.AllowPrivateTarget = true
	if err := n.Test(ctx, allowed); err != nil {
		t.Fatalf("Notifier.Test refused a loopback endpoint that HAD the "+
			"opt-in: %v -- the opt-in must reach the dialer, not just Validate", err)
	}
	if hits != 1 {
		t.Fatalf("endpoint hits = %d, want 1 once the opt-in was set", hits)
	}
}
