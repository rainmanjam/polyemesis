package db

import (
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Production must never get the test cost.
//
// This is the guard that makes WithPasswordCost safe to have at all. The option
// exists because bcrypt at DefaultCost costs 1.40 seconds per hash-and-compare
// under -race and 0.02s at MinCost, which was the entire runtime of
// internal/api -- but an option that can make hashing cheap is one edit away
// from making it cheap for everyone, and the failure would be silent and
// permanent: hashes are written at that cost and stay at it.
//
// Asserts on the COST INSIDE A REAL HASH rather than on the field. bcrypt.Cost
// reads it back out of the stored hash, which is what an attacker would be
// attacking; a field could be right while the value written was not.
//
// Mutation proving it can fail: in Open, change
// `passwordCost: bcrypt.DefaultCost` to `passwordCost: bcrypt.MinCost`.
// Measured: FAIL, "cost 4, want 10".
func TestTheDefaultPasswordCostIsNotWeakened(t *testing.T) {
	// Deliberately NOT testDB(t) -- that helper passes WithPasswordCost, which
	// is the thing this test has to not be handed.
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := d.CreateUser("admin", "correct-horse-battery"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var hash string
	if err := d.sql.QueryRow(`SELECT password_hash FROM users WHERE username = ?`,
		"admin").Scan(&hash); err != nil {
		t.Fatalf("read the stored hash: %v", err)
	}

	got, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("the stored value is not a bcrypt hash: %v", err)
	}
	if got != bcrypt.DefaultCost {
		t.Errorf("a password hashed by a default Open has cost %d, want %d. "+
			"An install built without an explicit option is every real install, "+
			"and a hash written at a low cost stays at that cost for ever.",
			got, bcrypt.DefaultCost)
	}
}

// And the option has to actually do something, or the test suite quietly went
// back to paying 1.40s a login and nothing said so.
//
// Mutation proving it can fail: in WithPasswordCost, change the body to
// `return func(d *DB) {}`. Measured: FAIL, "cost 10, want 4".
func TestWithPasswordCostIsHonoured(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"), WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := d.CreateUser("admin", "correct-horse-battery"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var hash string
	if err := d.sql.QueryRow(`SELECT password_hash FROM users WHERE username = ?`,
		"admin").Scan(&hash); err != nil {
		t.Fatalf("read the stored hash: %v", err)
	}
	got, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("the stored value is not a bcrypt hash: %v", err)
	}
	if got != bcrypt.MinCost {
		t.Errorf("cost %d, want %d: the option did not reach the hash, and the "+
			"suite is paying the production cost on every login again", got, bcrypt.MinCost)
	}
}

// SetPassword hashes too, and it was the second call site. A guard on CreateUser
// alone would not have noticed it being left behind.
//
// Mutation proving it can fail: in SetPassword, change `d.passwordCost` back to
// `bcrypt.DefaultCost`. Measured: FAIL, "cost 10, want 4".
func TestChangingAPasswordUsesTheSameCost(t *testing.T) {
	d := testDB(t)
	u, err := d.CreateUser("admin", "correct-horse-battery")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := d.SetPassword(u.ID, "a-different-long-password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	var hash string
	if err := d.sql.QueryRow(`SELECT password_hash FROM users WHERE id = ?`,
		u.ID).Scan(&hash); err != nil {
		t.Fatalf("read the stored hash: %v", err)
	}
	got, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("the stored value is not a bcrypt hash: %v", err)
	}
	if got != bcrypt.MinCost {
		t.Errorf("cost %d, want %d: SetPassword is still hashing at its own cost",
			got, bcrypt.MinCost)
	}
}
