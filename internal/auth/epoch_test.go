package auth

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The revocation tests.
//
// Sessions are stateless JWTs, so "log out everywhere" cannot mean deleting a
// server-side record — there isn't one. It means refusing tokens that carry a
// stale epoch. Every test here is written so that deleting the epoch check in
// Verify makes it fail; a test that still passes against a build with the guard
// removed would be proving nothing.

func epochManager(t *testing.T, epoch EpochFunc) *Manager {
	t.Helper()
	return New(bytes.Repeat([]byte{0x2a}, 32), false, false, epoch)
}

func TestTokenIssuedBeforeTheEpochBumpIsRefused(t *testing.T) {
	// A mutable epoch, standing in for users.token_epoch across a password
	// change.
	current := int64(3)
	m := epochManager(t, func(int64) (int64, error) { return current, nil })

	token, err := m.Issue(7, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Sanity: it works before the bump. Without this the test could pass
	// because the token was never valid in the first place.
	if _, err := m.Verify(token); err != nil {
		t.Fatalf("Verify before the bump: %v", err)
	}

	current++ // the password change

	if _, err := m.Verify(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Verify after the bump = %v, want ErrUnauthorized: a token issued "+
			"before the epoch moved must stop working, or changing a password "+
			"does not end the sessions it was changed to end", err)
	}
}

func TestTokenIssuedAfterTheEpochBumpStillWorks(t *testing.T) {
	// The other half, and the one that catches a guard implemented as "refuse
	// everything": the operator who just changed their password has to stay
	// logged in on the device they changed it from.
	current := int64(3)
	m := epochManager(t, func(int64) (int64, error) { return current, nil })

	current++
	token, err := m.Issue(7, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := m.Verify(token); err != nil {
		t.Fatalf("Verify of a freshly issued token: %v", err)
	}
}

func TestVerifyFailsClosedWhenTheEpochStoreIsUnreachable(t *testing.T) {
	// A database that cannot answer is not permission to skip the check.
	token, err := epochManager(t, staticEpoch(0)).Issue(7, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	broken := epochManager(t, func(int64) (int64, error) {
		return 0, errors.New("database is gone")
	})
	if _, err := broken.Verify(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Verify with an unreachable store = %v, want ErrUnauthorized", err)
	}
}

func TestAnUnwiredManagerCannotIssueOrVerify(t *testing.T) {
	// The Kick lesson, enforced. An optional security control is an absent one,
	// so a Manager built without an EpochFunc must not quietly skip the check —
	// it must be unable to authenticate anyone at all, which is a failure that
	// shows up on the first request rather than never.
	good := epochManager(t, staticEpoch(0))
	token, err := good.Issue(7, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	unwired := New(bytes.Repeat([]byte{0x2a}, 32), false, false, nil)

	if _, err := unwired.Issue(7, "admin"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Issue on an unwired manager = %v, want ErrUnauthorized", err)
	}
	if _, err := unwired.Verify(token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Verify on an unwired manager = %v, want ErrUnauthorized: a "+
			"missing epoch resolver must fail closed, not skip the check", err)
	}
}

func TestEpochIsCheckedAgainstTheTokenSubject(t *testing.T) {
	// The resolver is called with the user id from the token, not with whatever
	// the caller happens to be holding. On a single-admin install this is
	// invisible; it stops being invisible the moment there is a second row.
	var asked []int64
	m := epochManager(t, func(id int64) (int64, error) {
		asked = append(asked, id)
		return 0, nil
	})

	token, err := m.Issue(99, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	asked = nil
	if _, err := m.Verify(token); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(asked) != 1 || asked[0] != 99 {
		t.Fatalf("epoch resolver asked for %v, want exactly [99]", asked)
	}
}

func TestATokenWithAnUnparseableSubjectIsRefused(t *testing.T) {
	// Subject is a string in the JWT spec, so "not a user id" is representable.
	// It has to be refused rather than defaulted to user 0, which on a fresh
	// install would be an id that does not exist and on an unlucky one would be
	// somebody else's.
	m := epochManager(t, staticEpoch(0))

	now := time.Now()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "not-a-number",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    "polyemesis",
		},
		Username: "admin",
	}).SignedString(m.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := m.Verify(signed); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Verify with a non-numeric subject = %v, want ErrUnauthorized", err)
	}
}
