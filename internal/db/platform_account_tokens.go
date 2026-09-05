package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The narrow writer for a platform account's token columns, and the
// compare-and-swap that makes a refresh safe to run beside a reconnect.
//
// READ THIS IF AN ACCOUNT KEEPS ASKING TO BE RECONNECTED AND RECONNECTING NEVER
// HELPS. That was the defect this file exists for, and it was not a bug in the
// reconnect: it was two writers with different information both writing the
// whole row.
//
// A token refresh reads an account, spends anything from a hundred milliseconds
// to thirty seconds at the platform's token endpoint, and writes back what it
// got. The connect callback and the device flow write the same row from a
// different starting point -- they have just completed consent, so they are the
// only writers that know the CURRENT granted scopes, the CURRENT scope version
// and the CURRENT account name. Neither of them holds the refresh lock, because
// that lock is keyed by row id and a consent that is creating the row has no id
// to lock yet.
//
// So the interleaving is ordinary rather than exotic: a refresh starts, an
// operator reconnects to fix a scope warning, consent lands and writes the new
// scopes, and then the refresh -- which read the row BEFORE consent -- finishes
// and writes the whole struct back, scopes and scope_ver and account_name
// included, with the values it read before any of that happened. The account is
// now holding a fresh token and a stale scope stamp, so oauth.AccountNeedsReconnect
// goes on telling the operator to reconnect. Reconnecting loses the same race
// the same way, which is what turns a one-off into an unbreakable loop: the
// remedy IS the trigger.
//
// TWO DEVICES, AND THE FIRST IS THE IMPORTANT ONE.
//
//  1. The UPDATE below LISTS THREE COLUMNS. scopes, scope_ver and account_name
//     are not in the statement, so a refresh cannot write them by any route,
//     however wrong its in-memory struct becomes. That is not a rule this file
//     asks callers to follow; it is a sentence SQLite will not execute. The
//     model is UpdateLifecycle in destinations.go, which is narrow for the same
//     reason and says so at length.
//
//  2. The WHERE clause carries the row version the caller read, so a write that
//     would land on top of somebody else's is REFUSED rather than applied. The
//     loser then finds out, and internal/api decides what to do about it --
//     see Server.tokenFor, which yields to the row that won.
//
// The second device exists because the first is not sufficient on its own. Even
// with scopes out of reach, a refresh that started before consent would still
// overwrite the consent token with one minted against the pre-consent grant,
// and the operator would be back to a token that does not carry the permission
// they just granted.

// ErrAccountRewritten is what UpdatePlatformAccountTokens returns when the row
// changed between the caller reading it and the caller writing it back.
//
// A SENTINEL RATHER THAN A GENERIC FAILURE, for ErrLifecycleSkipped's reason:
// it is not an operational fault and it is not a database problem. It is the
// answer "somebody else has written a better row than the one you are holding",
// and the only caller that can decide what to do with that is the one that knows
// what it was refreshing for.
var ErrAccountRewritten = errors.New("the platform account was rewritten while its tokens were being refreshed")

// AccountRevision identifies the exact version of a platform account row that a
// caller read, and is the value UpdatePlatformAccountTokens swaps against.
//
// A TYPE WITH UNEXPORTED FIELDS RATHER THAN A time.Time OR AN int64 PARAMETER,
// and that shape is the poka-yoke. UpdatePlatformAccountTokens already takes an
// expiry, so a bare `updatedAt time.Time` would put two same-typed timestamps
// next to each other in one signature and make swapping them a thing the
// compiler accepts. Swapped, the comparison would be against the token's expiry
// instead of the row's version: every refresh would look like a lost race, or
// -- worse, on a row with no expiry -- none of them would. There is no
// constructor here and the fields cannot be set from outside this package, so
// the ONLY way to obtain one is PlatformAccount.Revision, i.e. by having
// actually read the row you are claiming to have read.
//
// IT CARRIES expires_at AS WELL AS updated_at because updated_at is stored in
// WHOLE SECONDS. A reconnect that lands in the same second as the refresh's
// read would leave the timestamps equal and the swap would wrongly succeed.
// expires_at closes that, and the argument is about DISTANCE, not about sign.
//
// A refresh runs against an account PlatformAccount.Expired() has called due,
// and that test carries a minute of LEAD -- `time.Now().Add(time.Minute).
// After(a.ExpiresAt)`, platforms.go -- precisely so a token is replaced before
// an in-flight API call can outlive it. So the row a refresh read does NOT
// necessarily have an expiry in the past: it has one that is at most sixty
// seconds in the future, and usually already gone. Any writer that beat it to
// the row -- a completed consent, another refresh -- wrote a freshly minted
// token, whose expiry is a whole token lifetime out from the moment it landed.
// For the two to collide, that new expiry would have to fall in the same second
// as the near-dead one it replaced, i.e. the platform would have to have issued
// a token already inside its own refresh window.
//
// The zero case is covered by the column's own representation rather than by
// this argument: an account with no expiry stores 0 (see platformAccountExpiry)
// and Expired() answers false for it, so it is never the row a refresh read.
type AccountRevision struct {
	// updatedAt and expiresAt are the two columns' stored values, not the
	// decoded time.Time fields: expires_at is stored as 0 for an account with
	// no expiry, and time.Time's zero value does not round-trip to 0.
	updatedAt int64
	expiresAt int64
}

// Revision returns the compare-and-swap witness for the row this account was
// read from. It means nothing on a PlatformAccount that was constructed rather
// than read, which is why the zero value is refused at the write.
func (a PlatformAccount) Revision() AccountRevision {
	return AccountRevision{
		updatedAt: a.UpdatedAt.Unix(),
		expiresAt: platformAccountExpiry(a.ExpiresAt),
	}
}

// platformAccountExpiry converts an expiry to the column's own representation:
// 0 for "this token does not expire", which is how UpsertPlatformAccount has
// always written it. Going through time.Time.Unix() on a zero time would store
// -62135596800 instead, and every comparison against a real row would then miss.
func platformAccountExpiry(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// UpdatePlatformAccountTokens replaces an account's access token, refresh token
// and expiry -- and nothing else -- provided the row is still the one the
// caller read.
//
// WHAT IT WILL NOT DO, which is the point of it existing beside
// UpsertPlatformAccount rather than instead of it: it cannot create a row, it
// cannot change which platform or account a row is for, and it cannot touch
// scopes, scope_ver or account_name. Those three are CONSENT facts. They are
// known correctly at exactly one moment -- when the authorization flow returns
// -- and a refresh, which never sees a consent screen, has nothing truer to say
// about them than the row already does. UpsertPlatformAccount stays the writer
// for connect and device-flow completion, where all of it IS known and where
// winning unconditionally is right.
//
// An empty refreshToken means "the platform did not issue a new one, keep the
// stored one", which is what nearly every refresh response looks like outside
// of providers that rotate. It is expressed in the statement rather than by the
// caller reading the old value and passing it back, so a caller cannot get it
// wrong by forgetting -- and, more to the point, cannot resurrect a refresh
// token that a racing consent has already replaced.
//
// Returns ErrNotFound if the account is gone and ErrAccountRewritten if it is
// still there but is no longer the revision the caller read. Those are
// deliberately different: one means stop, the other means look again.
func (d *DB) UpdatePlatformAccountTokens(box *secrets.Box, id int64, seen AccountRevision,
	accessToken, refreshToken string, expiresAt time.Time) (*PlatformAccount, error) {
	// A zero revision is a caller that never read the row -- a struct built in
	// code, or a field somebody forgot to carry through. Writing on that basis
	// is exactly the unconditional overwrite this function replaced, so it is
	// refused rather than treated as "matches nothing" (which it would not: a
	// row whose token never expires stores expires_at = 0).
	if seen.updatedAt == 0 {
		return nil, fmt.Errorf("refusing to write tokens for platform account %d without the revision "+
			"they were read at; use PlatformAccount.Revision on the row you read", id)
	}
	// An empty access token is not a refresh result, it is a refresh result
	// that was dropped somewhere. Storing it would leave a connected account
	// whose next call authenticates with nothing, reported by the platform as
	// an authorization failure with no hint that polyemesis erased the token
	// itself.
	//
	// THE SCHEMA ALREADY REFUSES THIS -- access_token_enc is BLOB NOT NULL and
	// secrets.Box.Seal("") returns no bytes at all -- so this check adds no
	// guarantee. What it adds is the DIAGNOSIS. Without it the refusal arrives
	// as "NOT NULL constraint failed: platform_accounts.access_token_enc",
	// which names a column rather than the mistake and sends the next reader
	// into the encryption layer looking for a bug that is not there.
	if accessToken == "" {
		return nil, fmt.Errorf("refusing to store an empty access token for platform account %d", id)
	}

	accessEnc, err := box.Seal(accessToken)
	if err != nil {
		return nil, err
	}
	refreshEnc, err := box.Seal(refreshToken)
	if err != nil {
		return nil, err
	}

	// updated_at is written as MAX(now, updated_at + 1) so that this statement
	// ALWAYS advances the revision, even when it runs twice inside one second.
	// Without it two writes in the same second would leave the witness
	// unchanged and a third writer holding the pre-first revision would still
	// match. The cost is a timestamp that can sit one second in the future on a
	// very busy row; the column is a "when did this last change" for a human,
	// and a second is below anything read off it.
	res, err := d.sql.Exec(`UPDATE platform_accounts SET
			access_token_enc=?,
			refresh_token_enc=COALESCE(NULLIF(?, X''), refresh_token_enc),
			expires_at=?,
			updated_at=MAX(?, updated_at + 1)
		WHERE id=? AND updated_at=? AND expires_at=?`,
		accessEnc, refreshEnc, platformAccountExpiry(expiresAt), time.Now().Unix(),
		id, seen.updatedAt, seen.expiresAt)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing matched, which is two different worlds. Asking which one is a
		// second query rather than a transaction because the answer is only
		// ever used to pick an error message: whichever it is, this write has
		// already declined to land, and that is the property that matters.
		var present int
		err := d.sql.QueryRow(`SELECT 1 FROM platform_accounts WHERE id=?`, id).Scan(&present)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		return nil, ErrAccountRewritten
	}
	return d.GetPlatformAccount(box, id)
}
