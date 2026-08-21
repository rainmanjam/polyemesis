package oauth

import (
	"fmt"
	"strings"
	"testing"
)

// TestTokenStringRedactsTokenMaterial is finding #16: nothing stopped a
// stray %v/%+v -- in a log line, a wrapped error, a test failure message --
// from printing a live access or refresh token. Checked for both the value
// and the pointer, because String() is defined on the value receiver and
// whether that also covers *Token is exactly the thing worth confirming
// rather than assuming.
//
// Mutation: delete Token's String() method (leave LogValue in place so the
// file still compiles) -- observed FAIL, output contained
// "super-secret-access".
func TestTokenStringRedactsTokenMaterial(t *testing.T) {
	tok := Token{AccessToken: "super-secret-access", RefreshToken: "super-secret-refresh", Scopes: "read write"}

	cases := []struct {
		name string
		got  string
	}{
		{"%v value", fmt.Sprintf("%v", tok)},
		{"%+v value", fmt.Sprintf("%+v", tok)},
		{"%v pointer", fmt.Sprintf("%v", &tok)},
		{"%+v pointer", fmt.Sprintf("%+v", &tok)},
	}
	for _, c := range cases {
		if strings.Contains(c.got, "super-secret-access") || strings.Contains(c.got, "super-secret-refresh") {
			t.Errorf("%s printed plaintext token material: %s", c.name, c.got)
		}
		if !strings.Contains(c.got, "[redacted]") {
			t.Errorf("%s did not redact at all: %s", c.name, c.got)
		}
	}
}

// TestTokenStringLeavesAnUnsetTokenVisiblyEmpty makes sure redaction does not
// overreach: a Token that never held a secret must print as empty, not as
// "[redacted]", which would claim a secret is present when there is none.
func TestTokenStringLeavesAnUnsetTokenVisiblyEmpty(t *testing.T) {
	got := fmt.Sprintf("%v", Token{})
	if strings.Contains(got, "[redacted]") {
		t.Errorf("an unset Token printed as if it held a secret: %s", got)
	}
}

// TestTokenLogValueRedactsTokenMaterial covers the slog path separately from
// fmt: slog.LogValuer is a distinct interface, and a build that satisfied
// fmt.Stringer but not slog.LogValuer would still leak through
// slog.Any("token", tok).
func TestTokenLogValueRedactsTokenMaterial(t *testing.T) {
	tok := Token{AccessToken: "super-secret-access", RefreshToken: "super-secret-refresh"}
	got := tok.LogValue().String()
	if strings.Contains(got, "super-secret-access") || strings.Contains(got, "super-secret-refresh") {
		t.Errorf("Token.LogValue() printed plaintext token material: %s", got)
	}
}
