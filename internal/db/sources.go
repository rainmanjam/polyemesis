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
	Token string `json:"token"`
	// PrevToken is the token this one replaced, still honoured until
	// PrevTokenUntil. Rotation that instantly kills a live stream is rotation
	// nobody performs, so the encoder already connected on the old token keeps
	// running while the new one takes effect.
	PrevToken      string    `json:"-"`
	PrevTokenUntil time.Time `json:"prevTokenUntil,omitempty"`
	Position       int       `json:"position"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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
		prevUntil  int64
		created    int64
		updated    int64
	)
	if err := row.Scan(&s.ID, &s.Name, &enabled, &ingestJSON, &s.Token,
		&s.PrevToken, &prevUntil, &s.Position, &created, &updated); err != nil {
		return nil, err
	}
	if prevUntil > 0 {
		s.PrevTokenUntil = time.Unix(prevUntil, 0)
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

const sourceColumns = `id, name, enabled, ingest, token, prev_token, prev_token_until, position, created_at, updated_at`

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

// portsInUse reports the SRT and RTMP ports every source except one is
// listening on. excludeID is the row being updated, so a source never conflicts
// with itself.
func (d *DB) portsInUse(excludeID int64) (map[int]string, error) {
	rows, err := d.ListSources()
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, s := range rows {
		if s.ID == excludeID {
			continue
		}
		out[s.Ingest.SRT.Port] = s.Name
		out[s.Ingest.RTMP.Port] = s.Name
	}
	return out, nil
}

// checkPortConflicts refuses a source that would listen where another already
// does.
//
// Without this a second source is created successfully, reports itself as
// configured, and then never receives anything -- because its ingest cannot
// bind a port the first source already holds, and the only evidence is a
// retrying child process in the log. Catching it here turns a silent
// non-functioning programme into a form error naming the source it clashes
// with.
func (d *DB) checkPortConflicts(s *Source) error {
	used, err := d.portsInUse(s.ID)
	if err != nil {
		return err
	}
	if owner, taken := used[s.Ingest.SRT.Port]; taken && s.Ingest.Mode == IngestSRT {
		return fmt.Errorf("srt port %d is already used by source %q", s.Ingest.SRT.Port, owner)
	}
	if owner, taken := used[s.Ingest.RTMP.Port]; taken && s.Ingest.Mode == IngestRTMP {
		return fmt.Errorf("rtmp port %d is already used by source %q", s.Ingest.RTMP.Port, owner)
	}
	return nil
}

// nextFreePort returns the first port at or above want that no source holds.
func nextFreePort(want int, used map[int]string) int {
	for p := want; p < 65535; p++ {
		if _, taken := used[p]; !taken {
			return p
		}
	}
	return want
}

// CreateSource inserts a source, minting a publish token when none was given.
//
// Ports that clash with an existing source are moved up rather than rejected.
// The alternative -- refusing the create -- would mean an operator adding a
// second programme has to know which ports are free before they can name it,
// and the defaults guarantee a clash because every source starts from the same
// ones. Moving them is what makes "add a source, then edit it" work.
func (d *DB) CreateSource(s *Source) error {
	if err := validateSource(s); err != nil {
		return err
	}
	used, err := d.portsInUse(0)
	if err != nil {
		return err
	}
	if _, taken := used[s.Ingest.SRT.Port]; taken {
		s.Ingest.SRT.Port = nextFreePort(s.Ingest.SRT.Port+1, used)
		used[s.Ingest.SRT.Port] = s.Name
	}
	if _, taken := used[s.Ingest.RTMP.Port]; taken {
		s.Ingest.RTMP.Port = nextFreePort(s.Ingest.RTMP.Port+1, used)
	}
	// Re-validate: moving a port must not have produced one out of range, and
	// SRT and RTMP must still differ.
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
		`INSERT INTO sources (name, enabled, ingest, token, prev_token, prev_token_until, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', 0, ?, ?, ?)`,
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
	// Rejected rather than moved, unlike create: an edit is an operator naming
	// a specific port, and silently using a different one would leave them
	// pointing an encoder somewhere nothing is listening.
	if err := d.checkPortConflicts(s); err != nil {
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
		`UPDATE sources SET name = ?, enabled = ?, ingest = ?, token = ?,
		 prev_token = ?, prev_token_until = ?, position = ?, updated_at = ?
		 WHERE id = ?`,
		s.Name, boolToInt(s.Enabled), string(blob), s.Token,
		s.PrevToken, unixOrZero(s.PrevTokenUntil), s.Position, now, s.ID)
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

// TokenGrace is how long a rotated-out token keeps working.
//
// Rotation that instantly kills a live stream is rotation nobody performs, and
// a credential nobody rotates is the problem per-source tokens were meant to
// solve. Five minutes is long enough to update an encoder without hurrying and
// short enough that a leaked token is not useful for long.
const TokenGrace = 5 * time.Minute

// RotateSourceToken issues a new publish secret and returns it.
//
// The replaced token stays valid for TokenGrace, so an encoder already
// publishing on it keeps running while the operator moves across.
func (d *DB) RotateSourceToken(id int64) (string, error) {
	cur, err := d.GetSource(id)
	if err != nil {
		return "", err
	}
	tok, err := NewSourceToken()
	if err != nil {
		return "", err
	}
	res, err := d.sql.Exec(
		`UPDATE sources SET token = ?, prev_token = ?, prev_token_until = ?, updated_at = ? WHERE id = ?`,
		tok, cur.Token, time.Now().Add(TokenGrace).Unix(), time.Now().Unix(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrSourceNotFound
	}
	return tok, nil
}

// ValidTokens returns every token this source currently accepts: the live one,
// plus the rotated-out one while its grace window is open.
//
// Returned as a slice rather than checked here because the caller compares in
// constant time across every candidate, and short-circuiting on the first match
// is exactly what leaks which token was right.
func (s *Source) ValidTokens(now time.Time) []string {
	out := []string{s.Token}
	if s.PrevToken != "" && !s.PrevTokenUntil.IsZero() && now.Before(s.PrevTokenUntil) {
		out = append(out, s.PrevToken)
	}
	return out
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

	// The rotation grace columns, for a database whose sources table predates
	// them. Plain columns with literal defaults, so unlike source_id they carry
	// a NOT NULL that ALTER TABLE accepts.
	for _, c := range []struct{ name, ddl string }{
		{"prev_token", `ALTER TABLE sources ADD COLUMN prev_token TEXT NOT NULL DEFAULT ''`},
		{"prev_token_until", `ALTER TABLE sources ADD COLUMN prev_token_until INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := columnExists(d.sql, "sources", c.name)
		if err != nil {
			return fmt.Errorf("inspect sources columns: %w", err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(c.ddl); err != nil {
			return fmt.Errorf("add sources.%s: %w", c.name, err)
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
