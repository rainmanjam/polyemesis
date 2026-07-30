package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrNoUser is returned before first-run setup has completed.
var ErrNoUser = errors.New("no admin user configured")

// ErrUserExists is returned when first-run setup is attempted on an install
// that already has an administrator. It is a named error because CreateUser now
// returns it from two places — the fast-path check and the lost-race branch —
// and a test that only pins the string could pass while covering one of them.
var ErrUserExists = errors.New("an admin user already exists")

// User is the single administrator account.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	hash string
}

// MinPasswordLength is enforced at every entry point that sets a password.
const MinPasswordLength = 8

// MigrateUserTokenEpoch adds users.token_epoch to a database created before
// sessions were revocable.
//
// It defaults to 0, which is the value a freshly issued token carries, so an
// upgraded install does not log its operator out on restart.
func (d *DB) MigrateUserTokenEpoch() error {
	has, err := columnExists(d.sql, "users", "token_epoch")
	if err != nil {
		return fmt.Errorf("inspect users columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE users ADD COLUMN token_epoch INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add users.token_epoch: %w", err)
	}
	return nil
}

// TokenEpoch returns the current session epoch for a user.
//
// A token whose embedded epoch does not match this one is refused, so this is
// read on every authenticated request. It is a single indexed primary-key
// lookup against a table with exactly one row, which SQLite serves from cache.
func (d *DB) TokenEpoch(id int64) (int64, error) {
	var epoch int64
	err := d.sql.QueryRow(`SELECT token_epoch FROM users WHERE id = ?`, id).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoUser
	}
	return epoch, err
}

// BumpTokenEpoch invalidates every session token already issued for a user.
//
// There is no way to invalidate one stateless token and not another — that is
// the trade the JWT bought — so this is deliberately all-or-nothing: the
// operator gets logged out everywhere, including on the device they are
// currently using. The caller is expected to immediately issue a replacement
// for the session in hand.
func (d *DB) BumpTokenEpoch(id int64) error {
	res, err := d.sql.Exec(
		`UPDATE users SET token_epoch = token_epoch + 1, updated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNoUser
	}
	return nil
}

// HasUser reports whether first-run setup has been completed.
func (d *DB) HasUser() (bool, error) {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CreateUser creates the admin account. It refuses to run twice — first-run
// setup must not be a way to take over an existing install.
//
// "Refuses to run twice" has to survive two requests arriving at once, and a
// read followed by a write does not: both callers can see an empty table before
// either inserts. POST /api/v1/setup is unauthenticated, so the race is
// reachable by anyone who can reach an install that has not been set up yet.
// The window is short and the install is unconfigured, which is why this was
// never urgent — but the fix costs one SQL clause, and "unauthenticated
// endpoint whose guard has a hole in it" is not a sentence worth leaving true.
//
// The guard is the WHERE NOT EXISTS on the INSERT. SQLite executes a single
// statement atomically, so the check and the write cannot be separated by
// another connection's commit.
func (d *DB) CreateUser(username, password string) (*User, error) {
	if len(password) < MinPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if username == "" {
		return nil, errors.New("username is required")
	}
	// Kept as a fast path, not as the guard: it produces the specific error
	// without paying for a bcrypt hash every time someone probes an install
	// that is already configured.
	has, err := d.HasUser()
	if err != nil {
		return nil, err
	}
	if has {
		return nil, ErrUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO users (username, password_hash, created_at, updated_at)
		SELECT ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users)`,
		username, string(hash), now, now)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Lost the race: another setup request committed between the fast-path
		// check above and this insert. Nothing was written.
		return nil, ErrUserExists
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, CreatedAt: time.Unix(now, 0), UpdatedAt: time.Unix(now, 0)}, nil
}

// GetUser loads the admin account.
func (d *DB) GetUser() (*User, error) {
	var u User
	var created, updated int64
	err := d.sql.QueryRow(
		`SELECT id, username, password_hash, created_at, updated_at FROM users ORDER BY id LIMIT 1`).
		Scan(&u.ID, &u.Username, &u.hash, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoUser
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	u.UpdatedAt = time.Unix(updated, 0)
	return &u, nil
}

// CheckPassword verifies a plaintext password against the stored hash.
func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.hash), []byte(password)) == nil
}

// SetPassword changes the admin password. The caller is responsible for having
// verified the current one.
func (d *DB) SetPassword(id int64, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// The epoch bump is part of the password change, not a separate step a
	// caller could forget: changing a password is the thing an operator does
	// when they believe someone else has their session, and it has to actually
	// end that session.
	_, err = d.sql.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ?, token_epoch = token_epoch + 1 WHERE id = ?`,
		string(hash), time.Now().Unix(), id)
	return err
}
