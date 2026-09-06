package db

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// seedRefreshableAccount is one connected account carrying every column a
// refresh must not touch, with an already-expired token so it looks exactly
// like the row tokenFor picks up.
func seedRefreshableAccount(t *testing.T, d *DB, box *secrets.Box) *PlatformAccount {
	t.Helper()
	acct, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform:     PlatformTwitch,
		AccountName:  "ada",
		AccountRef:   "ada-ref",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour).Truncate(time.Second),
		Scopes:       "channel:manage:broadcast chat:edit",
		ScopeVer:     4,
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return acct
}

// TestUpdatePlatformAccountTokensWritesOnlyTheTokenColumns is the rung-1 half of
// the fix: the consent columns are not in the statement, so a refresh cannot
// restate them however wrong the struct it is holding has become.
//
// This is also the POSITIVE CONTROL for the compare-and-swap tests below. An
// implementation that refused every write -- always returning
// ErrAccountRewritten -- would satisfy "a stale revision is rejected" perfectly,
// and this is the assertion it could not pass.
//
// Mutation: add `account_name=?, scopes=?, scope_ver=?` to the UPDATE and feed
// them from the caller. Observed FAIL.
func TestUpdatePlatformAccountTokensWritesOnlyTheTokenColumns(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	fresh := time.Now().Add(time.Hour).Truncate(time.Second)
	got, err := d.UpdatePlatformAccountTokens(box, acct.ID, acct.Revision(),
		"new-access", "new-refresh", fresh)
	if err != nil {
		t.Fatalf("UpdatePlatformAccountTokens: %v", err)
	}

	if got.AccessToken != "new-access" {
		t.Errorf("access token = %q, want %q -- the narrow writer must still write the tokens it is for",
			got.AccessToken, "new-access")
	}
	if got.RefreshToken != "new-refresh" {
		t.Errorf("refresh token = %q, want %q", got.RefreshToken, "new-refresh")
	}
	if !got.ExpiresAt.Equal(fresh) {
		t.Errorf("expires at = %v, want %v", got.ExpiresAt, fresh)
	}
	// The consent columns. A refresh has no standing to restate any of these,
	// and rewriting them with pre-consent values is what left an account asking
	// to be reconnected for ever.
	if got.Scopes != acct.Scopes {
		t.Errorf("scopes = %q, want %q unchanged -- a token refresh must not write consent facts",
			got.Scopes, acct.Scopes)
	}
	if got.ScopeVer != acct.ScopeVer {
		t.Errorf("scope_ver = %d, want %d unchanged", got.ScopeVer, acct.ScopeVer)
	}
	if got.AccountName != acct.AccountName {
		t.Errorf("account name = %q, want %q unchanged", got.AccountName, acct.AccountName)
	}
}

// TestUpdatePlatformAccountTokensKeepsTheStoredRefreshTokenWhenNoneIsIssued
// pins the "" case, which is what most providers' refresh responses look like.
//
// Mutation: drop the COALESCE(NULLIF(...)) and bind the parameter directly.
// Observed FAIL -- the account loses its refresh token and can never renew
// again.
func TestUpdatePlatformAccountTokensKeepsTheStoredRefreshTokenWhenNoneIsIssued(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	got, err := d.UpdatePlatformAccountTokens(box, acct.ID, acct.Revision(),
		"new-access", "", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("UpdatePlatformAccountTokens: %v", err)
	}
	if got.RefreshToken != "old-refresh" {
		t.Errorf("refresh token = %q, want the stored %q kept", got.RefreshToken, "old-refresh")
	}
	if got.AccessToken != "new-access" {
		t.Errorf("access token = %q, want it written", got.AccessToken)
	}
}

// TestUpdatePlatformAccountTokensRefusesARevisionSomebodyElseHasMovedOn is the
// compare-and-swap itself, in the shape the defect actually takes: a refresh
// reads the row, a reconnect completes and writes it, and the refresh comes
// back with what it read.
//
// The reconnect is a real UpsertPlatformAccount, because that is the writer
// that races -- it is what handleOAuthCallback and the device flow both call,
// and it holds no lock.
//
// Mutation: relax the WHERE clause to `WHERE id=?`. Observed FAIL -- the second
// write lands and the account is left carrying the pre-consent scopes.
func TestUpdatePlatformAccountTokensRefusesARevisionSomebodyElseHasMovedOn(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	// What the refresh read before it went to the platform.
	seen := acct.Revision()

	// Consent lands in the meantime and writes the whole row, including the
	// scopes the operator has just granted.
	if _, err := d.UpsertPlatformAccount(box, &PlatformAccount{
		Platform:     acct.Platform,
		AccountName:  "ada (renamed)",
		AccountRef:   acct.AccountRef,
		AccessToken:  "consent-access",
		RefreshToken: "consent-refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
		Scopes:       "channel:manage:broadcast chat:edit channel:read:stream_key",
		ScopeVer:     5,
	}); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	_, err := d.UpdatePlatformAccountTokens(box, acct.ID, seen,
		"refreshed-access", "refreshed-refresh", time.Now().Add(time.Hour))
	if !errors.Is(err, ErrAccountRewritten) {
		t.Fatalf("UpdatePlatformAccountTokens with a stale revision = %v, want ErrAccountRewritten", err)
	}

	after, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if after.AccessToken != "consent-access" {
		t.Errorf("access token = %q, want the reconnect's %q -- the losing refresh must not have landed",
			after.AccessToken, "consent-access")
	}
	if after.ScopeVer != 5 || after.AccountName != "ada (renamed)" {
		t.Errorf("scope_ver/account name = %d/%q, want 5/%q", after.ScopeVer, after.AccountName, "ada (renamed)")
	}
}

// TestUpdatePlatformAccountTokensAdvancesTheRevisionOnEveryWrite is the
// same-second case that a timestamp alone cannot carry.
//
// updated_at is stored in whole seconds, so a write that stamps the row with
// time.Now() inside the second the caller read it leaves the witness LOOKING
// unchanged. A third writer holding that spent witness would then match and
// land -- the swap silently stops swapping, on exactly the busy row it exists
// for.
//
// The row's timestamp is stamped directly here rather than waited for, so the
// scenario is the one being tested instead of one the clock happened to allow:
// updated_at is set to the current second, and both the expiry the caller reads
// and the expiry it writes are the same value, so expires_at cannot be what
// distinguishes the two writes and updated_at has to be.
//
// Mutation: write `updated_at=?` in place of `updated_at=MAX(?, updated_at+1)`.
// Observed FAIL, twice over -- "updated_at did not advance" and then the
// replayed revision landing.
func TestUpdatePlatformAccountTokensAdvancesTheRevisionOnEveryWrite(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	stamp := time.Now().Unix()
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := d.sql.Exec(`UPDATE platform_accounts SET updated_at=?, expires_at=? WHERE id=?`,
		stamp, expiry.Unix(), acct.ID); err != nil {
		t.Fatalf("stamp the row: %v", err)
	}

	read, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	seen := read.Revision()

	if _, err := d.UpdatePlatformAccountTokens(box, acct.ID, seen, "first", "", expiry); err != nil {
		t.Fatalf("first write: %v", err)
	}
	var got int64
	if err := d.sql.QueryRow(`SELECT updated_at FROM platform_accounts WHERE id=?`, acct.ID).Scan(&got); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if got <= stamp {
		t.Fatalf("updated_at did not advance: %d, want > %d -- a write inside the same second as the "+
			"read leaves the compare-and-swap witness spendable twice", got, stamp)
	}

	_, err = d.UpdatePlatformAccountTokens(box, acct.ID, seen, "second", "", expiry)
	if !errors.Is(err, ErrAccountRewritten) {
		t.Fatalf("replaying a spent revision = %v, want ErrAccountRewritten", err)
	}
	after, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if after.AccessToken != "first" {
		t.Errorf("access token = %q, want %q", after.AccessToken, "first")
	}
}

// TestUpdatePlatformAccountTokensRefusesAWitnessNobodyRead covers the caller
// that never read the row: a zero AccountRevision is a programming mistake, and
// it has to be REPORTED as one.
//
// The distinction is the whole test, because a zero witness matches no real row
// and would therefore fall out of the WHERE clause as ErrAccountRewritten all by
// itself. That error is not a lie so much as a wrong diagnosis with a
// consequence: tokenFor treats ErrAccountRewritten as "somebody wrote a better
// row" and YIELDS, so a caller that simply forgot to carry the revision would
// have its refresh silently discarded on every attempt, and the log line would
// blame a reconnect that never happened.
//
// Mutation: replace the `seen.updatedAt == 0` guard with `if false`. Observed
// FAIL ("a zero revision was reported as a lost race").
func TestUpdatePlatformAccountTokensRefusesAWitnessNobodyRead(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	_, err := d.UpdatePlatformAccountTokens(box, acct.ID, AccountRevision{},
		"new-access", "", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("a zero revision was accepted; it must be refused")
	}
	if errors.Is(err, ErrAccountRewritten) || errors.Is(err, ErrNotFound) {
		t.Fatalf("a zero revision was reported as a lost race or a missing row (%v); it is a caller that "+
			"never read the account, and tokenFor yields on ErrAccountRewritten rather than surfacing it", err)
	}

	after, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if after.AccessToken != "old-access" {
		t.Errorf("access token = %q, want the seeded %q -- the refusal may not have written",
			after.AccessToken, "old-access")
	}
}

// TestUpdatePlatformAccountTokensRefusesAnEmptyAccessToken pins the refusal AND
// the sentence it arrives in, deliberately.
//
// The refusal itself is the schema's: access_token_enc is BLOB NOT NULL and
// Seal("") produces no bytes, so this write cannot land whatever this package
// does. Asserting only "it errors" would therefore pass with the Go check
// deleted -- and what is lost with it deleted is the diagnosis. The operator or
// the next reader gets "NOT NULL constraint failed:
// platform_accounts.access_token_enc", which names a column instead of the
// mistake and points at the encryption layer, where the bug is not.
//
// Mutation: delete the `accessToken == ""` guard. Observed FAIL -- the error
// comes back as the raw constraint violation.
func TestUpdatePlatformAccountTokensRefusesAnEmptyAccessToken(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)

	_, err := d.UpdatePlatformAccountTokens(box, acct.ID, acct.Revision(),
		"", "", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("an empty access token was accepted; it must be refused")
	}
	if !strings.Contains(err.Error(), "empty access token") {
		t.Errorf("error = %v, want one that names the empty access token rather than the column it "+
			"failed a constraint on", err)
	}
	after, err := d.GetPlatformAccount(box, acct.ID)
	if err != nil {
		t.Fatalf("GetPlatformAccount: %v", err)
	}
	if after.AccessToken != "old-access" {
		t.Errorf("access token = %q, want the seeded %q kept", after.AccessToken, "old-access")
	}
}

// TestUpdatePlatformAccountTokensSaysNotFoundForADeletedAccount keeps "the row
// is gone" distinguishable from "the row moved on". One means stop; the other
// means look again, and tokenFor branches on the difference.
func TestUpdatePlatformAccountTokensSaysNotFoundForADeletedAccount(t *testing.T) {
	d := testDB(t)
	box := testBox(t)
	acct := seedRefreshableAccount(t, d, box)
	seen := acct.Revision()
	if err := d.DeletePlatformAccount(acct.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := d.UpdatePlatformAccountTokens(box, acct.ID, seen, "new-access", "", time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("writing to a deleted account = %v, want ErrNotFound", err)
	}
}
