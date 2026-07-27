package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A Source is one ingested programme: an ingest configuration, plus everything
// that hangs off it.
//
// Before sources existed the ingest configuration lived in the settings
// singleton and there was exactly one of it. The case that broke that was OBS's
// vertical-canvas plugin: a 16:9 and a 9:16 feed are two different
// compositions, not one cropped from the other, and the only answer polyemesis
// had was "run a second container".
//
// A source owns its destinations and renditions. It does NOT own its
// recordings — those outlive it; see the schema for why.
type Source struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Ingest is the same shape settings.ingest carried when there was one
	// source. Deliberately identical so one validator serves both and the
	// migration is a copy rather than a translation.
	Ingest IngestSettings `json:"ingest"`
	// Token is this source's publish secret. Returned in plaintext because the
	// operator has to paste it into OBS and will come back to read it again --
	// see the schema comment on sources.token for why it is not hashed like an
	// API token.
	Token     string    `json:"token"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const (
	// MaxSourceNameLength keeps a name inside what the UI can show and what a
	// log line can carry without wrapping.
	MaxSourceNameLength = 64
	// DefaultSourceName is what the migration calls the source it builds from
	// an existing single-ingest install. "Main" rather than "Default" because
	// it is what the operator has been streaming to all along, and it reads as
	// a programme rather than as a placeholder.
	DefaultSourceName = "Main"
	// sourceTokenBytes is the entropy behind an ingest token. 24 bytes is 192
	// bits, which is plenty for a value that also has to be retyped by hand
	// into OBS occasionally.
	sourceTokenBytes = 24
)

// ErrSourceNotFound is returned by the single-row getters.
var ErrSourceNotFound = errors.New("source not found")

// NewSourceToken mints a publish secret.
//
// URL-safe and unpadded on purpose: this value is pasted into an SRT streamid
// and an RTMP path, and '+', '/' and '=' all have to be escaped in at least one
// of those. Producing a token that needs escaping is how you get an ingest that
// works in testing and fails for the one user whose token happened to contain a
// slash.
func NewSourceToken() (string, error) {
	b := make([]byte, sourceTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validateSource checks the parts that are this package's business. The ingest
// block validates itself through the same path settings uses.
func validateSource(s *Source) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("source name is required")
	}
	if len(s.Name) > MaxSourceNameLength {
		return fmt.Errorf("source name must be at most %d characters", MaxSourceNameLength)
	}
	if probs := s.Ingest.problems(); len(probs) > 0 {
		return fmt.Errorf("ingest settings: %s", strings.Join(probs, "; "))
	}
	return nil
}

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var (
		s          Source
		ingestJSON string
		enabled    int
		created    int64
		updated    int64
	)
	if err := row.Scan(&s.ID, &s.Name, &enabled, &ingestJSON, &s.Token, &s.Position, &created, &updated); err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	// A source whose ingest blob will not parse still has to be listable, or a
	// single bad row takes the whole page down and leaves no way to fix it
	// through the UI. Fall back to the defaults and let validation complain
	// when the operator next saves.
	if err := json.Unmarshal([]byte(ingestJSON), &s.Ingest); err != nil {
		s.Ingest = DefaultSettings().Ingest
	}
	s.CreatedAt = time.Unix(created, 0)
	s.UpdatedAt = time.Unix(updated, 0)
	return &s, nil
}

const sourceColumns = `id, name, enabled, ingest, token, position, created_at, updated_at`

// ListSources returns every source in display order.
func (d *DB) ListSources() ([]*Source, error) {
	rows, err := d.sql.Query(`SELECT ` + sourceColumns + ` FROM sources ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSource returns one source.
func (d *DB) GetSource(id int64) (*Source, error) {
	row := d.sql.QueryRow(`SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	return s, err
}

// CreateSource inserts a source, minting a publish token when none was given.
func (d *DB) CreateSource(s *Source) error {
	if err := validateSource(s); err != nil {
		return err
	}
	if s.Token == "" {
		tok, err := NewSourceToken()
		if err != nil {
			return fmt.Errorf("mint source token: %w", err)
		}
		s.Token = tok
	}
	blob, err := json.Marshal(s.Ingest)
	if err != nil {
		return err
	}

	// Append by default so a new source does not jump ahead of the programmes
	// already on screen.
	if s.Position == 0 {
		var maxPos sql.NullInt64
		_ = d.sql.QueryRow(`SELECT MAX(position) FROM sources`).Scan(&maxPos)
		s.Position = int(maxPos.Int64) + 1
	}

	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO sources (name, enabled, ingest, token, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.Name, boolToInt(s.Enabled), string(blob), s.Token, s.Position, now, now)
	if err != nil {
		return err
	}
	s.ID, _ = res.LastInsertId()
	s.CreatedAt = time.Unix(now, 0)
	s.UpdatedAt = time.Unix(now, 0)
	return nil
}

// UpdateSource writes every mutable field.
func (d *DB) UpdateSource(s *Source) error {
	if err := validateSource(s); err != nil {
		return err
	}
	if s.Token == "" {
		// An empty token would mean "anyone who reaches the port may publish
		// here", which is precisely what per-source tokens exist to prevent.
		// Mint one rather than storing the gap.
		tok, err := NewSourceToken()
		if err != nil {
			return fmt.Errorf("mint source token: %w", err)
		}
		s.Token = tok
	}
	blob, err := json.Marshal(s.Ingest)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`UPDATE sources SET name = ?, enabled = ?, ingest = ?, token = ?, position = ?, updated_at = ?
		 WHERE id = ?`,
		s.Name, boolToInt(s.Enabled), string(blob), s.Token, s.Position, now, s.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSourceNotFound
	}
	s.UpdatedAt = time.Unix(now, 0)
	return nil
}

// DeleteSource removes a source. Its destinations and renditions go with it
// (ON DELETE CASCADE); its recordings survive with a NULL source_id.
//
// The last source cannot be deleted. An install with none has no ingest at all
// and no way through the UI to get one back, since the "add a source" form
// still has to live somewhere.
func (d *DB) DeleteSource(id int64) error {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("cannot delete the only source: an install needs at least one ingest")
	}
	res, err := d.sql.Exec(`DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if aff, _ := res.RowsAffected(); aff == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// RotateSourceToken issues a new publish secret and returns it.
func (d *DB) RotateSourceToken(id int64) (string, error) {
	tok, err := NewSourceToken()
	if err != nil {
		return "", err
	}
	res, err := d.sql.Exec(`UPDATE sources SET token = ?, updated_at = ? WHERE id = ?`,
		tok, time.Now().Unix(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrSourceNotFound
	}
	return tok, nil
}

// SourceByToken resolves a publish secret to its source. This is the lookup an
// ingest listener makes to decide which programme an encoder is publishing to.
//
// An empty token never matches, even against a row that somehow stored one:
// otherwise a publisher who sends no token at all authenticates as whichever
// source has the emptiest configuration.
func (d *DB) SourceByToken(token string) (*Source, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrSourceNotFound
	}
	row := d.sql.QueryRow(`SELECT `+sourceColumns+` FROM sources WHERE token = ?`, token)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	return s, err
}

// DefaultSourceID returns the lowest-positioned source, which is what an
// unscoped API call operates on.
//
// Keeping "the default source" a query rather than a hardcoded id=1 matters
// because id 1 is deletable once a second source exists, and an install whose
// first source has been removed must still answer "which one did you mean?".
func (d *DB) DefaultSourceID() (int64, error) {
	var id int64
	err := d.sql.QueryRow(`SELECT id FROM sources ORDER BY position, id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSourceNotFound
	}
	return id, err
}

// ingestForMigration reads the stored ingest configuration.
//
// It exists instead of a GetSettings call because GetSettings SEEDS defaults on
// first run -- it writes a settings row when it finds none. A migration that
// reads must not write: doing so materialised a settings row on every fresh
// database and broke a test that seeds its own sparse blob to check that an
// older settings shape still upgrades. That test was right and the migration
// was wrong.
//
// A settings blob that will not parse is returned as an error rather than
// swallowed. Falling back to the defaults here would hand the operator an
// ingest on the default port instead of theirs, which presents as "my encoder
// cannot connect any more" -- a worse outcome than refusing to start, and much
// harder to diagnose.
func (d *DB) ingestForMigration() (IngestSettings, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings().Ingest, nil
	}
	if err != nil {
		return IngestSettings{}, err
	}
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return IngestSettings{}, fmt.Errorf("decode settings: %w", err)
	}
	return s.Ingest, nil
}

// MigrateSources brings a single-ingest database up to the sources model.
//
// Three steps, each idempotent, because this runs on every open:
//
//  1. Add the source_id columns. Nullable with a REFERENCES clause, which is
//     the only shape SQLite's ALTER TABLE accepts while foreign keys are on --
//     and the same shape schema.sql declares, so a migrated database and a
//     fresh one are indistinguishable afterwards.
//  2. Create the first source from settings.ingest, so an existing install
//     keeps ingesting on exactly the port and protocol it did before. This is
//     the step that must not be got wrong: an operator who upgrades and finds
//     their encoder can no longer connect has lost their broadcast.
//  3. Backfill. Every destination, rendition and recording that predates
//     sources belongs to that first source, because there was nowhere else for
//     it to have come from.
func (d *DB) MigrateSources() error {
	for _, c := range []struct{ table, ddl string }{
		{"destinations", `ALTER TABLE destinations ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE`},
		{"renditions", `ALTER TABLE renditions ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE`},
		{"recordings", `ALTER TABLE recordings ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE SET NULL`},
	} {
		has, err := columnExists(d.sql, c.table, "source_id")
		if err != nil {
			return fmt.Errorf("inspect %s columns: %w", c.table, err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(c.ddl); err != nil {
			return fmt.Errorf("add %s.source_id: %w", c.table, err)
		}
	}

	// Indexes here rather than in schema.sql for the reason MigrateRenditions
	// gives: schema.sql runs before the columns are guaranteed to exist, and a
	// failed CREATE INDEX aborts the script and stops the server booting.
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_destinations_source ON destinations(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_renditions_source ON renditions(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_recordings_source ON recordings(source_id)`,
	} {
		if _, err := d.sql.Exec(idx); err != nil {
			return fmt.Errorf("index source_id: %w", err)
		}
	}

	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		return fmt.Errorf("count sources: %w", err)
	}
	if n == 0 {
		// Carry the existing ingest across verbatim.
		ing, err := d.ingestForMigration()
		if err != nil {
			return fmt.Errorf("read settings for source migration: %w", err)
		}
		src := &Source{Name: DefaultSourceName, Enabled: true, Ingest: ing, Position: 1}
		if err := d.CreateSource(src); err != nil {
			return fmt.Errorf("create %s source: %w", DefaultSourceName, err)
		}
	}

	id, err := d.DefaultSourceID()
	if err != nil {
		return fmt.Errorf("resolve default source: %w", err)
	}
	for _, table := range []string{"destinations", "renditions", "recordings"} {
		if _, err := d.sql.Exec(
			`UPDATE `+table+` SET source_id = ? WHERE source_id IS NULL`, id); err != nil {
			return fmt.Errorf("backfill %s.source_id: %w", table, err)
		}
	}
	return nil
}
