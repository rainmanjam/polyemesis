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
func (d *DB) CreateUser(username, password string) (*User, error) {
	if len(password) < MinPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if username == "" {
		return nil, errors.New("username is required")
	}
	has, err := d.HasUser()
	if err != nil {
		return nil, err
	}
	if has {
		return nil, errors.New("an admin user already exists")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		username, string(hash), now, now)
	if err != nil {
		return nil, err
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
	_, err = d.sql.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		string(hash), time.Now().Unix(), id)
	return err
}
