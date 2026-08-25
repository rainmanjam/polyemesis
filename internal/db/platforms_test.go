package db

import (
	"fmt"
	"strings"
	"testing"
)

// TestPlatformAccountStringRedactsTokenMaterial is finding #16:
// AccessToken/RefreshToken already carry `json:"-"` for the API boundary
// (confirmed by reading the struct -- that protection already exists and
// this test does not duplicate it), but nothing stopped a stray %v/%+v --
// in a log line, a wrapped error, a test failure message -- from printing
// the plaintext tokens. Checked for both the value and the pointer, since
// String() is defined on the value receiver and whether that also covers
// *PlatformAccount is worth confirming rather than assuming.
//
// Mutation: delete PlatformAccount's String() method (leave LogValue in
// place so the file still compiles) -- observed FAIL, output contained
// "super-secret-access".
func TestPlatformAccountStringRedactsTokenMaterial(t *testing.T) {
	acct := PlatformAccount{
		ID: 7, Platform: PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "super-secret-access", RefreshToken: "super-secret-refresh",
	}

	cases := []struct {
		name string
		got  string
	}{
		{"%v value", fmt.Sprintf("%v", acct)},
		{"%+v value", fmt.Sprintf("%+v", acct)},
		{"%v pointer", fmt.Sprintf("%v", &acct)},
		{"%+v pointer", fmt.Sprintf("%+v", &acct)},
	}
	for _, c := range cases {
		if strings.Contains(c.got, "super-secret-access") || strings.Contains(c.got, "super-secret-refresh") {
			t.Errorf("%s printed plaintext token material: %s", c.name, c.got)
		}
		if !strings.Contains(c.got, "[redacted]") {
			t.Errorf("%s did not redact at all: %s", c.name, c.got)
		}
		// The account name/ref are not secrets and identify which account this
		// was -- redaction must not blank the whole struct.
		if !strings.Contains(c.got, "ada-ref") {
			t.Errorf("%s dropped non-secret identifying fields it should have kept: %s", c.name, c.got)
		}
	}
}

// TestPlatformAccountStringLeavesAnUnsetTokenVisiblyEmpty makes sure
// redaction does not overreach: an account with no stored token (e.g. before
// its first refresh) must print as empty, not "[redacted]", which would
// claim a secret is present when there is none.
func TestPlatformAccountStringLeavesAnUnsetTokenVisiblyEmpty(t *testing.T) {
	got := fmt.Sprintf("%v", PlatformAccount{Platform: PlatformTwitch, AccountName: "ada"})
	if strings.Contains(got, "[redacted]") {
		t.Errorf("an account with no stored token printed as if it held one: %s", got)
	}
}

// TestPlatformAccountLogValueRedactsTokenMaterial covers the slog path
// separately from fmt: slog.LogValuer is a distinct interface, and a build
// that satisfied fmt.Stringer but not slog.LogValuer would still leak
// through slog.Any("account", acct).
func TestPlatformAccountLogValueRedactsTokenMaterial(t *testing.T) {
	acct := PlatformAccount{AccessToken: "super-secret-access", RefreshToken: "super-secret-refresh"}
	got := acct.LogValue().String()
	if strings.Contains(got, "super-secret-access") || strings.Contains(got, "super-secret-refresh") {
		t.Errorf("PlatformAccount.LogValue() printed plaintext token material: %s", got)
	}
}
