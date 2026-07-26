package db

import (
	"database/sql"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// PlatformCreds is the operator's own OAuth developer app. polyemesis cannot
// ship client secrets, so the user registers an app and pastes the pair in.
type PlatformCreds struct {
	Platform     Platform  `json:"platform"`
	ClientID     string    `json:"clientId"`
	ClientSecret string    `json:"-"` // never serialised outward
	HasSecret    bool      `json:"hasSecret"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PutPlatformCreds stores a client ID/secret pair, encrypting the secret.
func (d *DB) PutPlatformCreds(box *secrets.Box, p Platform, clientID, clientSecret string) error {
	enc, err := box.Seal(clientSecret)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO platform_creds (platform, client_id, client_secret_enc, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(platform) DO UPDATE SET client_id=excluded.client_id,
			client_secret_enc=excluded.client_secret_enc, updated_at=excluded.updated_at`,
		p, clientID, enc, time.Now().Unix())
	return err
}

// GetPlatformCreds loads and decrypts a credential pair.
func (d *DB) GetPlatformCreds(box *secrets.Box, p Platform) (*PlatformCreds, error) {
	var (
		c       PlatformCreds
		enc     []byte
		updated int64
	)
	err := d.sql.QueryRow(`SELECT platform, client_id, client_secret_enc, updated_at
		FROM platform_creds WHERE platform = ?`, p).Scan(&c.Platform, &c.ClientID, &enc, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.ClientSecret, err = box.Open(enc); err != nil {
		return nil, err
	}
	c.HasSecret = c.ClientSecret != ""
	c.UpdatedAt = time.Unix(updated, 0)
	return &c, nil
}

// ListPlatformCreds returns credentials without secrets, for the settings UI.
func (d *DB) ListPlatformCreds() ([]PlatformCreds, error) {
	rows, err := d.sql.Query(`SELECT platform, client_id, LENGTH(client_secret_enc), updated_at FROM platform_creds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PlatformCreds{}
	for rows.Next() {
		var (
			c       PlatformCreds
			n       int
			updated int64
		)
		if err := rows.Scan(&c.Platform, &c.ClientID, &n, &updated); err != nil {
			return nil, err
		}
		c.HasSecret = n > 0
		c.UpdatedAt = time.Unix(updated, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletePlatformCreds removes a developer app registration.
func (d *DB) DeletePlatformCreds(p Platform) error {
	_, err := d.sql.Exec(`DELETE FROM platform_creds WHERE platform = ?`, p)
	return err
}

// PlatformAccount is a connected channel. Multiple per platform is supported
// and is the point: two YouTube channels are two accounts.
type PlatformAccount struct {
	ID           int64     `json:"id"`
	Platform     Platform  `json:"platform"`
	AccountName  string    `json:"accountName"`
	AccountRef   string    `json:"accountRef"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       string    `json:"scopes"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Expired reports whether the access token needs refreshing. The one-minute
// skew keeps us from handing a token to an API call that will outlive it.
func (a PlatformAccount) Expired() bool {
	if a.ExpiresAt.IsZero() || a.ExpiresAt.Unix() == 0 {
		return false
	}
	return time.Now().Add(time.Minute).After(a.ExpiresAt)
}

// UpsertPlatformAccount stores a connected account, replacing any previous
// tokens for the same (platform, accountRef).
func (d *DB) UpsertPlatformAccount(box *secrets.Box, a *PlatformAccount) (*PlatformAccount, error) {
	accessEnc, err := box.Seal(a.AccessToken)
	if err != nil {
		return nil, err
	}
	refreshEnc, err := box.Seal(a.RefreshToken)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var exp int64
	if !a.ExpiresAt.IsZero() {
		exp = a.ExpiresAt.Unix()
	}

	_, err = d.sql.Exec(`INSERT INTO platform_accounts
		(platform, account_name, account_ref, access_token_enc, refresh_token_enc, expires_at, scopes, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform, account_ref) DO UPDATE SET
			account_name=excluded.account_name,
			access_token_enc=excluded.access_token_enc,
			refresh_token_enc=COALESCE(NULLIF(excluded.refresh_token_enc, X''), platform_accounts.refresh_token_enc),
			expires_at=excluded.expires_at,
			scopes=excluded.scopes,
			updated_at=excluded.updated_at`,
		a.Platform, a.AccountName, a.AccountRef, accessEnc, refreshEnc, exp, a.Scopes, now, now)
	if err != nil {
		return nil, err
	}

	var id int64
	if err := d.sql.QueryRow(`SELECT id FROM platform_accounts WHERE platform=? AND account_ref=?`,
		a.Platform, a.AccountRef).Scan(&id); err != nil {
		return nil, err
	}
	return d.GetPlatformAccount(box, id)
}

// GetPlatformAccount loads and decrypts one account.
func (d *DB) GetPlatformAccount(box *secrets.Box, id int64) (*PlatformAccount, error) {
	var (
		a                 PlatformAccount
		accessEnc         []byte
		refreshEnc        []byte
		exp, created, upd int64
	)
	err := d.sql.QueryRow(`SELECT id, platform, account_name, account_ref, access_token_enc,
		refresh_token_enc, expires_at, scopes, created_at, updated_at
		FROM platform_accounts WHERE id = ?`, id).
		Scan(&a.ID, &a.Platform, &a.AccountName, &a.AccountRef, &accessEnc, &refreshEnc,
			&exp, &a.Scopes, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if a.AccessToken, err = box.Open(accessEnc); err != nil {
		return nil, err
	}
	if a.RefreshToken, err = box.Open(refreshEnc); err != nil {
		return nil, err
	}
	if exp > 0 {
		a.ExpiresAt = time.Unix(exp, 0)
	}
	a.CreatedAt = time.Unix(created, 0)
	a.UpdatedAt = time.Unix(upd, 0)
	return &a, nil
}

// ListPlatformAccounts returns all connected accounts, without token material.
func (d *DB) ListPlatformAccounts() ([]PlatformAccount, error) {
	rows, err := d.sql.Query(`SELECT id, platform, account_name, account_ref, expires_at,
		scopes, created_at, updated_at FROM platform_accounts ORDER BY platform, account_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PlatformAccount{}
	for rows.Next() {
		var (
			a                 PlatformAccount
			exp, created, upd int64
		)
		if err := rows.Scan(&a.ID, &a.Platform, &a.AccountName, &a.AccountRef, &exp,
			&a.Scopes, &created, &upd); err != nil {
			return nil, err
		}
		if exp > 0 {
			a.ExpiresAt = time.Unix(exp, 0)
		}
		a.CreatedAt = time.Unix(created, 0)
		a.UpdatedAt = time.Unix(upd, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeletePlatformAccount disconnects an account.
func (d *DB) DeletePlatformAccount(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM platform_accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- OAuth state (CSRF protection for the authorization-code flow) ---

// PutOAuthState records a pending authorization request.
func (d *DB) PutOAuthState(state string, p Platform, verifier string) error {
	// Opportunistically drop states older than 10 minutes; an authorization
	// round trip that takes longer than that has been abandoned.
	_, _ = d.sql.Exec(`DELETE FROM oauth_states WHERE created_at < ?`, time.Now().Add(-10*time.Minute).Unix())
	_, err := d.sql.Exec(`INSERT INTO oauth_states (state, platform, verifier, created_at) VALUES (?,?,?,?)`,
		state, p, verifier, time.Now().Unix())
	return err
}

// TakeOAuthState consumes a state parameter, returning its platform and PKCE
// verifier. Single-use: a replayed callback finds nothing.
func (d *DB) TakeOAuthState(state string) (Platform, string, error) {
	var (
		p        Platform
		verifier string
		created  int64
	)
	err := d.sql.QueryRow(`SELECT platform, verifier, created_at FROM oauth_states WHERE state = ?`, state).
		Scan(&p, &verifier, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("unknown or already-used OAuth state")
	}
	if err != nil {
		return "", "", err
	}
	if _, err := d.sql.Exec(`DELETE FROM oauth_states WHERE state = ?`, state); err != nil {
		return "", "", err
	}
	if time.Since(time.Unix(created, 0)) > 10*time.Minute {
		return "", "", errors.New("OAuth state expired; please retry the connection")
	}
	return p, verifier, nil
}
