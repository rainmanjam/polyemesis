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

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	sourceListQuery    = `SELECT ` + sourceColumns + ` FROM sources ORDER BY position, id`
	sourceByIDQuery    = `SELECT ` + sourceColumns + ` FROM sources WHERE id = ?`
	sourceByTokenQuery = `SELECT ` + sourceColumns + ` FROM sources WHERE token = ?`
)

// ListSources returns every source in display order.
func (d *DB) ListSources() ([]*Source, error) {
	rows, err := d.sql.Query(sourceListQuery)
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

// CountSources is how many programmes this install has.
//
// A count rather than len(ListSources()), because the first caller is the
// UNAUTHENTICATED setup endpoint: a row carries the publish token an encoder
// authenticates with, and there is no reason to decode one to answer "how
// many". The number itself is not a secret -- it is what tells a browser
// whether this install has a programme yet, and therefore whether the
// dashboard should be showing an empty state instead of a red error.
func (d *DB) CountSources() (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n)
	return n, err
}

// GetSource returns one source.
func (d *DB) GetSource(id int64) (*Source, error) {
	row := d.sql.QueryRow(sourceByIDQuery, id)
	s, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	return s, err
}

// CreateSource inserts a source, minting publish tokens when none were given.
//
// There is nothing left to allocate or move out of the way, and no exclusivity
// left to check. Every source arrives on one listener per protocol and is told
// apart by its token -- SRT by the streamid, RTMP by the URL path -- so adding
// a source cannot collide with an existing one whichever protocol it uses.
//
// RTMP used to be the exception (checkRTMPExclusive, deleted): `ffmpeg -listen
// 1` is a single-connection receiver that cannot demultiplex by path, so an
// install could carry exactly one RTMP source. internal/rtmpserver replaced it
// with a real one-port server, which is what removed the rule rather than
// merely stopping it from being enforced.
func (d *DB) CreateSource(s *Source) error { return createSource(d.sql, s) }

// createSource is CreateSource with the handle passed in.
//
// It takes one rather than reaching for d.sql because MigrateSources creates
// the first source INSIDE a transaction, and this database runs on a single
// connection (db.go's SetMaxOpenConns(1)): an insert issued on d.sql while that
// transaction holds the connection does not fail, it waits for a connection the
// transaction will not release until it commits. The server would hang at
// startup rather than report anything.
func createSource(x execQuerier, s *Source) error {
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
		_ = x.QueryRow(`SELECT MAX(position) FROM sources`).Scan(&maxPos)
		s.Position = int(maxPos.Int64) + 1
	}

	now := time.Now().Unix()
	res, err := x.Exec(
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
// THE LAST SOURCE MAY BE DELETED, and this used to refuse it: an install with
// none had no ingest at all and no way through the UI to get one back, because
// the add-a-source form lived on a page every other screen refused to reach.
// None of that is true now. Zero sources is the state a fresh install boots
// into -- Open no longer seeds a row -- so the whole product already answers in
// it: the Sources page carries the create form, and every route that needs a
// programme refuses with a 503 naming that page rather than panicking.
//
// Refusing the deliberate way into a state the install ships in was a rule
// about nothing. What it cost was real: an operator replacing their one source
// had to create the new one first and work out which of two rows was which,
// and an operator winding an install down could not.
//
// Deleting a source that is not there is still ErrSourceNotFound, which is what
// the API turns into a 404 -- for a stale row on a screen nobody reloaded, or a
// client retrying a delete that already succeeded.
func (d *DB) DeleteSource(id int64) error {
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
	row := d.sql.QueryRow(sourceByTokenQuery, token)
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
//
// An EXISTING blob decodes onto mergeBaseSettings, not DefaultSettings, for the
// reason GetSettings gives about itself: DefaultSettings leaves the mode unset
// so a first run has to ask, and a stored blob that predates the mode field, or
// omits it, would inherit that and migrate onto a source that ingests nothing.
// The installs where this function runs at all are the oldest ones -- the ones
// whose blob is most likely to predate the field -- so the base that is right
// for a fresh install is exactly the wrong one here. A MISSING blob still takes
// the defaults, because there is nothing to preserve and nothing to ask on
// behalf of.
func (d *DB) ingestForMigration() (IngestSettings, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings().Ingest, nil
	}
	if err != nil {
		return IngestSettings{}, err
	}
	s := mergeBaseSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return IngestSettings{}, fmt.Errorf("decode settings: %w", err)
	}
	return s.Ingest, nil
}

// sourceColumnArrivedByUpgrade reports whether table's source_id column was
// added by this migration rather than created with the table.
//
// The two are textually distinguishable in sqlite_master and permanently so.
// schema.sql declares source_id as a plain column and puts its reference in a
// separate FOREIGN KEY clause; ALTER TABLE ADD COLUMN cannot write one of those,
// so it appends the reference inline and SQLite rewrites the stored CREATE
// statement that way. Dumping a fresh database and an upgraded one side by side
// shows it: the fresh table keeps "FOREIGN KEY (source_id) REFERENCES ...", the
// upgraded one ends "..., source_id INTEGER REFERENCES sources(id) ON DELETE
// CASCADE)" and has no such clause.
//
// That makes it the one witness of a life before sources that needs no surviving
// row and does not decay -- see the third witness in MigrateSources for what
// that buys. The cost is a coupling to how schema.sql spells the constraint:
// rewrite it inline there and every fresh install starts answering true here.
// TestTheFreshSchemaKeepsTheShapeTheUpgradeProbeReads fails loudly if that
// happens, because the failure it would otherwise cause is silent.
//
// Line comments are stripped before matching, since schema.sql's are prose and
// prose about a foreign key is not a foreign key.
func sourceColumnArrivedByUpgrade(sqldb *sql.DB, table string) (bool, error) {
	var ddl string
	err := sqldb.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stripped strings.Builder
	for _, line := range strings.Split(ddl, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		stripped.WriteString(line)
		stripped.WriteString(" ")
	}
	flat := strings.Join(strings.Fields(strings.ToLower(stripped.String())), "")
	return !strings.Contains(flat, "foreignkey(source_id)"), nil
}

// sourceEverExisted reports whether a row was ever inserted into sources, at any
// point in this database's life.
//
// sources.id is INTEGER PRIMARY KEY AUTOINCREMENT, so SQLite records the high
// water mark in sqlite_sequence on the first insert and DELETE never takes it
// away -- only dropping the table does. An install that had a source and lost it
// is therefore distinguishable from one that never had one, which no other table
// can tell you: after a delete the rows are gone and the migrated column shapes
// are not.
//
// That distinction is the veto in the discriminator below. Both are states with
// zero sources, and exactly one of them wants a source seeded.
func sourceEverExisted(sqldb *sql.DB) (bool, error) {
	// sqlite_sequence is created with the first AUTOINCREMENT table, so it is
	// always there by the time schema.sql has run -- but a database that has
	// never had one at all answers "no source was ever inserted", which is the
	// truth, rather than "no such table".
	var n int
	if err := sqldb.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'`,
	).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := sqldb.QueryRow(
		`SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'sources'`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
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
//
// Step 2 is UPGRADE-ONLY. A brand-new install finishes this function with no
// source at all, because there is no ingest to carry and nothing to attach: it
// asks the operator to create one. Telling the two apart is the whole of the
// discriminator below, and getting it wrong in either direction is silent --
// seed a fresh install and the operator inherits a programme they did not
// configure; skip an upgrading one and their encoder stops connecting with no
// visible cause.
func (d *DB) MigrateSources() error {
	// EVERY read happens before the transaction opens, and that is not
	// stylistic. columnExists and the probes below query d.sql, and db.go sets
	// SetMaxOpenConns(1) -- a read issued while a transaction holds the one
	// connection waits for a connection the transaction will not release until
	// it commits. It would not fail; it would hang on startup, for ever.
	// MigrateDestinationExpertArgs carries the same warning over the same trap.
	type column struct{ table, ddl string }
	sourceColumns := []column{
		{"destinations", `ALTER TABLE destinations ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE`},
		{"renditions", `ALTER TABLE renditions ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE`},
		{"recordings", `ALTER TABLE recordings ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE SET NULL`},
	}

	// migrating is decided HERE, in the probe loop, and never afterwards.
	//
	// schema.sql declares all three source_id columns and runs before this, so
	// on a fresh database every check answers true and this is false. On a
	// database that predates sources at least one answers false. Recompute the
	// same checks after the ALTERs and the answer is true everywhere, for every
	// install -- including the upgrading one this exists to recognise, which
	// then never gets its ingest carried onto a source.
	//
	// OR-ed rather than assigned, because a database really can have one
	// source_id column and not the others: releases before this commit ran the
	// ALTERs outside a transaction, so a process killed part-way through the
	// loop left exactly that. Keeping only the last table's answer would call
	// such an install fresh.
	var missing []column
	migrating := false
	present := make(map[string]bool, len(sourceColumns))
	for _, c := range sourceColumns {
		has, err := columnExists(d.sql, c.table, "source_id")
		if err != nil {
			return fmt.Errorf("inspect %s columns: %w", c.table, err)
		}
		present[c.table] = has
		if has {
			continue
		}
		migrating = true
		missing = append(missing, c)
	}

	// The rotation grace columns, for a database whose sources table predates
	// them. Plain columns with literal defaults, so unlike source_id they carry
	// a NOT NULL that ALTER TABLE accepts.
	//
	// Deliberately NOT folded into `migrating`: they belong to the token
	// rotation release, which is LATER than sources. A database that already
	// migrated to sources but predates rotation would otherwise read as
	// "pre-sources" for ever, and once an operator is allowed to delete their
	// last source that flag resurrects Main on the next boot.
	tokenColumns := []struct{ name, ddl string }{
		{"prev_token", `ALTER TABLE sources ADD COLUMN prev_token TEXT NOT NULL DEFAULT ''`},
		{"prev_token_until", `ALTER TABLE sources ADD COLUMN prev_token_until INTEGER NOT NULL DEFAULT 0`},
	}
	var missingToken []struct{ name, ddl string }
	for _, c := range tokenColumns {
		has, err := columnExists(d.sql, "sources", c.name)
		if err != nil {
			return fmt.Errorf("inspect sources columns: %w", err)
		}
		if has {
			continue
		}
		missingToken = append(missingToken, c)
	}

	// The second witness: a row that has no source to belong to. It catches the
	// install that already has its source_id columns -- an earlier release added
	// them -- but never got a source, which `migrating` alone cannot see.
	//
	// DESTINATIONS AND RENDITIONS ONLY. recordings.source_id is ON DELETE SET
	// NULL by design (schema.sql:226), so an orphan recording is the NORMAL
	// state after a legitimate delete. Counting one would resurrect Main on the
	// next boot of the install that deliberately removed its last source -- and
	// would do it to the operator with the largest library.
	//
	// Probed only over tables whose column already exists, because a table
	// still missing it cannot be asked about it: on a genuinely pre-sources
	// database this SELECT is "no such column". Nothing is lost by skipping it
	// there -- a missing column has already set `migrating`, so the seed
	// condition is satisfied either way.
	orphans := false
	for _, table := range []string{"destinations", "renditions"} {
		if orphans || !present[table] {
			continue
		}
		var orphaned int
		if err := d.sql.QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE source_id IS NULL`).Scan(&orphaned); err != nil {
			return fmt.Errorf("count orphan %s: %w", table, err)
		}
		orphans = orphaned > 0
	}

	// The third witness: the SHAPE of the source_id columns that are already
	// there. A column this migration added reads differently in sqlite_master
	// from one schema.sql created -- see sourceColumnArrivedByUpgrade.
	//
	// It is the only witness that survives an interruption on a release OLDER
	// than this one, and that is the state that matters on this build's first
	// boot. Before the transaction below, the ALTERs committed one at a time and
	// the source was created afterwards, so anything that stopped the process in
	// between -- a crash, `docker stop`, or the release's own error path when the
	// stored ingest no longer validated -- left the columns committed and the
	// sources table empty. On such a database `migrating` is false because all
	// three columns are present, and an install whose only history is a
	// configured ingest has no destination or rendition to orphan. Without this
	// the discriminator calls it a fresh install and the operator's ingest is
	// dropped, which is the exact loss the whole discriminator exists to prevent.
	//
	// Asked only of tables that HAVE the column: one that does not is already
	// `migrating`, and there is no shape to read.
	addedByUpgrade := false
	for _, table := range []string{"destinations", "renditions", "recordings"} {
		if addedByUpgrade || !present[table] {
			continue
		}
		altered, err := sourceColumnArrivedByUpgrade(d.sql, table)
		if err != nil {
			return fmt.Errorf("inspect %s shape: %w", table, err)
		}
		addedByUpgrade = altered
	}

	// And the veto. A database that ever held a source keeps that fact for ever
	// in sqlite_sequence, and an operator who deleted their last source has said
	// something the next boot must not undo (MUST NOT #4).
	//
	// The witnesses above cannot say it on their own: a migrated install keeps
	// its upgrade-shaped columns after its source is deleted, so `addedByUpgrade`
	// stays true and would put Main back on every start -- for ever, on the
	// install with the longest history. This is what separates "never had one"
	// from "had one and removed it", the two zero-source states that look
	// identical in every table.
	everHadASource, err := sourceEverExisted(d.sql)
	if err != nil {
		return fmt.Errorf("check for a source ever existing: %w", err)
	}

	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		return fmt.Errorf("count sources: %w", err)
	}

	// n == 0 is the question, not the answer: it is equally true of the fresh
	// install that must come up with none, and of the install that deliberately
	// deleted its last. It takes a witness of a life before sources to turn it
	// into a yes, and no history of a source to keep it one.
	seed := n == 0 && !everHadASource && (migrating || orphans || addedByUpgrade)

	var ing IngestSettings
	if seed {
		// Read before Begin, for the connection reason above -- and read only
		// when it is going to be used, because a settings blob that will not
		// parse is a hard error and an install with sources already has no
		// business being stopped from booting by one.
		if ing, err = d.ingestForMigration(); err != nil {
			return fmt.Errorf("read settings for source migration: %w", err)
		}
	}

	// The backfill's target. Resolved here when a source already exists, and
	// from the insert below when one is about to.
	//
	// Gated on a source EXISTING rather than on `orphans`: the UPDATE is
	// idempotent and its own WHERE makes it a no-op when there is nothing to
	// attach, so the only thing this ever needed to prevent was demanding a
	// default source from an install that has none. Gating it on `orphans`
	// instead strands the recordings of a pre-sources install that had no
	// destinations or renditions -- it seeds Main and then never attaches them.
	var backfillID int64
	if n > 0 {
		id, err := d.DefaultSourceID()
		if err != nil {
			return fmt.Errorf("resolve default source: %w", err)
		}
		backfillID = id
	}

	// WHICH tables the backfill covers, which is not the same question as
	// whether it runs.
	//
	// destinations and renditions are ON DELETE CASCADE, so they never outlive
	// their source: a NULL there is always a row from before sources existed and
	// always wants attaching, on any boot.
	//
	// recordings are ON DELETE SET NULL by design (schema.sql:225-226), so a
	// NULL there usually means the opposite -- the archive of a source the
	// operator deleted, deliberately kept. Attaching those to whatever
	// DefaultSourceID happens to answer hands one programme's recordings to
	// another, on the next boot, permanently, with nothing in the library to say
	// it happened. So recordings are attached only when the source below is
	// SEEDED, which the discriminator only does on an install that never had one
	// -- an install with no delete behind it, so its NULL recordings can only be
	// the pre-sources ones A4 is about.
	backfillTables := []string{"destinations", "renditions"}
	if seed {
		backfillTables = append(backfillTables, "recordings")
	}

	// ONE TRANSACTION over the ALTERs and the source they exist for.
	//
	// This is not housekeeping. Without it, an upgrade interrupted between the
	// ALTER loop and the insert -- crash, SIGKILL, power loss, `docker stop` --
	// leaves a database whose source_id columns exist while no source does, and
	// nothing about that state says which of the two zero-source installs it is.
	// SQLite's DDL is transactional, so the columns and the source now arrive
	// together or neither does, and the next open tries again from a state it
	// recognises. Do not unwind this for tidiness.
	//
	// It stops the state being CREATED; it cannot repair a database already in
	// it, and this build's first boot is exactly when those arrive. That is what
	// `addedByUpgrade` above is for, and why removing either one on the grounds
	// that the other covers it reopens a silent data-loss window.
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin sources migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, c := range missing {
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("add %s.source_id: %w", c.table, err)
		}
	}
	for _, c := range missingToken {
		if _, err := tx.Exec(c.ddl); err != nil {
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
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("index source_id: %w", err)
		}
	}

	if seed {
		// Carry the existing ingest across verbatim.
		src := &Source{Name: DefaultSourceName, Enabled: true, Ingest: ing, Position: 1}
		if err := createSource(tx, src); err != nil {
			return fmt.Errorf("create %s source: %w", DefaultSourceName, err)
		}
		backfillID = src.ID
	}

	if backfillID != 0 {
		for _, table := range backfillTables {
			if _, err := tx.Exec(
				`UPDATE `+table+` SET source_id = ? WHERE source_id IS NULL`, backfillID); err != nil {
				return fmt.Errorf("backfill %s.source_id: %w", table, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sources migration: %w", err)
	}
	return nil
}
