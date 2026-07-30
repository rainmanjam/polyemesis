package db

import (
	"errors"
	"sync"
	"testing"
)

// TestConcurrentFirstRunSetupCreatesExactlyOneAdmin is the regression guard for
// the setup race.
//
// POST /api/v1/setup is unauthenticated by necessity — there is nobody to
// authenticate as yet — and its only guard is CreateUser refusing to run twice.
// That guard used to be a HasUser() read followed by an INSERT, which two
// requests can interleave: both read an empty table, both insert. The UNIQUE
// constraint on username catches the case where they pick the same name and
// misses the case where they do not.
//
// The fix is the WHERE NOT EXISTS on the INSERT, which SQLite evaluates
// atomically with the write. Remove it and this test fails.
func TestConcurrentFirstRunSetupCreatesExactlyOneAdmin(t *testing.T) {
	d := testDB(t)

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created []string
		refused int
	)

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Distinct usernames on purpose. Identical ones would be caught by
			// the UNIQUE index and the test would pass without the real guard
			// ever being exercised.
			u, err := d.CreateUser(usernameFor(i), "correct-horse-battery")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created = append(created, u.Username)
			case errors.Is(err, ErrUserExists):
				refused++
			default:
				t.Errorf("CreateUser returned an unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(created) != 1 {
		t.Fatalf("%d admins were created (%v), want exactly 1: first-run setup "+
			"is unauthenticated and must not be a way to add an account to an "+
			"install someone else is setting up", len(created), created)
	}
	if refused != racers-1 {
		t.Fatalf("%d racers were refused, want %d", refused, racers-1)
	}

	// And the table agrees with what the calls reported.
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("users table holds %d rows, want 1", n)
	}
}

func usernameFor(i int) string {
	return string(rune('a'+i)) + "dmin"
}

// TestSetPasswordBumpsTheTokenEpoch pins the half of session revocation that
// lives in the store. The auth package proves a stale epoch is refused; this
// proves a password change is what makes it stale.
func TestSetPasswordBumpsTheTokenEpoch(t *testing.T) {
	d := testDB(t)

	u, err := d.CreateUser("admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}

	before, err := d.TokenEpoch(u.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.SetPassword(u.ID, "a-different-password"); err != nil {
		t.Fatal(err)
	}

	after, err := d.TokenEpoch(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("token epoch went %d -> %d; a password change must invalidate "+
			"sessions issued before it", before, after)
	}
}

func TestBumpTokenEpochOnAMissingUserIsAnError(t *testing.T) {
	d := testDB(t)
	if err := d.BumpTokenEpoch(404); !errors.Is(err, ErrNoUser) {
		t.Fatalf("BumpTokenEpoch on a missing user = %v, want ErrNoUser", err)
	}
}

func TestTokenEpochOnAMissingUserIsAnError(t *testing.T) {
	d := testDB(t)
	if _, err := d.TokenEpoch(404); !errors.Is(err, ErrNoUser) {
		t.Fatalf("TokenEpoch on a missing user = %v, want ErrNoUser", err)
	}
}
