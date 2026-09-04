package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreatedTokenIsUsableAndCarriesTheDisplayPrefix(t *testing.T) {
	d := testDB(t)

	tok, plaintext, err := d.CreateAPIToken("ci runner", ScopeAdmin)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		t.Errorf("plaintext %q does not start with %q", plaintext, TokenPrefix)
	}
	if tok.Prefix != plaintext[:tokenDisplayLength] {
		t.Errorf("Prefix = %q, want %q", tok.Prefix, plaintext[:tokenDisplayLength])
	}
	if len(plaintext) <= tokenDisplayLength {
		t.Fatalf("plaintext %q is no longer than the part shown in the UI", plaintext)
	}

	got, err := d.LookupAPIToken(plaintext)
	if err != nil {
		t.Fatalf("LookupAPIToken: %v", err)
	}
	if got.ID != tok.ID || got.Name != "ci runner" {
		t.Errorf("looked up %+v, want id %d named %q", got, tok.ID, "ci runner")
	}
}

func TestOnlyTheHashOfATokenIsPersisted(t *testing.T) {
	d := testDB(t)

	_, plaintext, err := d.CreateAPIToken("ci runner", ScopeAdmin)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// The secret half must not appear anywhere in the row, in any column.
	var name, hash, prefix string
	if err := d.sql.QueryRow(`SELECT name, token_hash, prefix FROM api_tokens`).Scan(&name, &hash, &prefix); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	secret := plaintext[tokenDisplayLength:]
	for col, v := range map[string]string{"name": name, "token_hash": hash, "prefix": prefix} {
		if strings.Contains(v, secret) {
			t.Errorf("column %s contains the token secret", col)
		}
	}
	if hash == plaintext {
		t.Error("token_hash stores the plaintext verbatim")
	}
	if len(hash) != 64 {
		t.Errorf("token_hash length = %d, want 64 hex characters of SHA-256", len(hash))
	}
}

func TestLookupAPITokenRejects(t *testing.T) {
	d := testDB(t)

	_, valid, err := d.CreateAPIToken("ci runner", ScopeAdmin)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	revoked, revokedPlaintext, err := d.CreateAPIToken("old laptop", ScopeAdmin)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := d.DeleteAPIToken(revoked.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{name: "empty credential", plaintext: ""},
		{name: "something that is not a polyemesis token", plaintext: "hunter2"},
		{name: "right shape, wrong secret", plaintext: TokenPrefix + strings.Repeat("A", 43)},
		{name: "the display prefix alone", plaintext: valid[:tokenDisplayLength]},
		{name: "a revoked token", plaintext: revokedPlaintext},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.LookupAPIToken(tc.plaintext); !errors.Is(err, ErrNotFound) {
				t.Errorf("LookupAPIToken(%q) error = %v, want ErrNotFound", tc.plaintext, err)
			}
		})
	}
}

func TestEachTokenIsDistinct(t *testing.T) {
	d := testDB(t)

	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		_, plaintext, err := d.CreateAPIToken("runner", ScopeAdmin)
		if err != nil {
			t.Fatalf("CreateAPIToken: %v", err)
		}
		if seen[plaintext] {
			t.Fatalf("token %q was minted twice", plaintext)
		}
		seen[plaintext] = true
	}
}

func TestCreateAPITokenRejectsBadNames(t *testing.T) {
	d := testDB(t)

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "whitespace only", input: "   "},
		{name: "longer than the column is meant to hold", input: strings.Repeat("x", maxTokenNameLength+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := d.CreateAPIToken(tc.input, ScopeAdmin); err == nil {
				t.Error("CreateAPIToken accepted the name, want an error")
			}
		})
	}
}

func TestListAPITokensReturnsNewestFirstAndNeverTheSecret(t *testing.T) {
	d := testDB(t)

	var plaintexts []string
	for _, name := range []string{"first", "second", "third"} {
		_, p, err := d.CreateAPIToken(name, ScopeAdmin)
		if err != nil {
			t.Fatalf("CreateAPIToken(%s): %v", name, err)
		}
		plaintexts = append(plaintexts, p)
	}

	list, err := d.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	want := []string{"third", "second", "first"}
	if len(list) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(list), len(want))
	}
	for i, n := range want {
		if list[i].Name != n {
			t.Errorf("token[%d].Name = %q, want %q", i, list[i].Name, n)
		}
		for _, p := range plaintexts {
			if list[i].Prefix == p {
				t.Errorf("token[%d].Prefix is the full plaintext", i)
			}
		}
	}
}

func TestLookupRecordsLastUse(t *testing.T) {
	d := testDB(t)

	tok, plaintext, err := d.CreateAPIToken("ci runner", ScopeAdmin)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !tok.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt on a fresh token = %v, want zero", tok.LastUsedAt)
	}

	if _, err := d.LookupAPIToken(plaintext); err != nil {
		t.Fatalf("LookupAPIToken: %v", err)
	}
	list, err := d.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if time.Since(list[0].LastUsedAt) > time.Minute {
		t.Errorf("LastUsedAt = %v, want roughly now", list[0].LastUsedAt)
	}
}

func TestDeleteAPITokenReportsAnUnknownID(t *testing.T) {
	d := testDB(t)

	if err := d.DeleteAPIToken(404); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteAPIToken(404) error = %v, want ErrNotFound", err)
	}
}

// APITokenExists is what a live /ws socket asks instead of consulting an
// in-process set. #706.
func TestAPITokenExistsAnswersForBothOutcomes(t *testing.T) {
	d := testDB(t)
	tok, _, err := d.CreateAPIToken("ci-runner", string(ScopeAdmin))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ok, err := d.APITokenExists(tok.ID)
	if err != nil || !ok {
		t.Fatalf("a live token reports exists=%v err=%v; a socket would be closed "+
			"on a credential that still works", ok, err)
	}

	if err := d.DeleteAPIToken(tok.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ok, err = d.APITokenExists(tok.ID)
	if err != nil {
		t.Fatalf("after delete: %v", err)
	}
	if ok {
		t.Fatal("a deleted token still reports as existing, so the socket opened " +
			"with it would keep streaming -- which is the whole of #706")
	}

	// Zero is not an id and must not become a database round trip, because
	// every socket without a token principal passes one on every ping.
	if ok, err := d.APITokenExists(0); ok || err != nil {
		t.Errorf("APITokenExists(0) = %v, %v; want false, nil", ok, err)
	}
	// An id that never existed is absent rather than an error: sql.ErrNoRows is
	// the ordinary answer here and must not close a socket for the wrong reason.
	if ok, err := d.APITokenExists(999999); ok || err != nil {
		t.Errorf("APITokenExists(999999) = %v, %v; want false, nil", ok, err)
	}
}
