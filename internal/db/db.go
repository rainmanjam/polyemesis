// Package db is the SQLite persistence layer: config, destinations, routing
// profiles, users, platform credentials and the recording index.
//
// Uses modernc.org/sqlite, a pure-Go translation of SQLite, so the whole
// server stays a single cgo-free static binary.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"sync"

	"github.com/rainmanjam/polyemesis/internal/fsperm"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the SQLite handle.
//
// Deliberately just that: no filesystem path and no logger. This package
// used to carry an optional data directory and logger for one migration's
// benefit (a legacy playlist FilePath, DESIGN 2026-08-01-playlist-items),
// which put filesystem I/O and logging on GetSettings's read path -- read by
// roughly twenty callers, several of them per-request API handlers. That
// migration now runs once at startup in cmd/polyemesis, where a real data
// directory and a real, configured logger already live; see
// LegacyPlaylistFilePath for the pure half this package still owns.
type DB struct {
	sql *sql.DB

	// passwordCost is the bcrypt cost every password hash in this package uses.
	// Open sets it to bcrypt.DefaultCost; only a test ever lowers it, through
	// WithPasswordCost.
	//
	// A per-instance field rather than a package var, deliberately. This repo
	// removed a set of package-var test seams in 0.3.0 for being order-dependent
	// under -count=N and impossible to run in parallel with anything else in
	// their package -- see internal/oauth/endpoints.go, which opens with that
	// reasoning. A field on the thing being configured has neither problem.
	passwordCost int

	// box seals and opens the destination stream keys. nil is a supported
	// configuration and means "store them in plaintext, exactly as before".
	//
	// ON THE DB RATHER THAN IN THE SIGNATURES, unlike every other sealed thing
	// in this package. PutPlatformCreds, UpsertPlatformAccount, ListHooks and
	// the rest all take a *secrets.Box parameter, and that is the right shape
	// for them: they are a handful of methods with a handful of callers each,
	// and the parameter makes the encryption visible at the call site.
	//
	// Destinations are not that. The stream key rides on Destination, which is
	// returned by eight CRUD methods called from roughly ninety-seven places
	// across twenty-four files -- ListDestinations alone has nine callers in
	// three packages. Threading a box through all of them would be a mechanical
	// edit of the whole repository to express one fact that never varies within
	// a process: which key file this install uses. So it is configured once, at
	// Open, beside the other thing that is fixed for the life of the handle.
	//
	// The cost is that the encryption is invisible at those call sites. What
	// pays for it is that there is exactly one place to get it wrong, and
	// scanDestination is that place.
	box *secrets.Box

	// settingsMu serialises READ-MODIFY-WRITE callers of the settings
	// singleton, and nothing else.
	//
	// The settings are ONE JSON document: PutSettings writes the whole blob,
	// so two callers that each read it, change a different field and write it
	// back do not merge -- whichever lands second silently discards every
	// field the other one changed, not just the field it meant to edit. Four
	// places in the running server do exactly that (the engine's scheduled
	// playlist flip, PUT /settings, the annotations mirror in PUT
	// /annotations, and PUT /jobs/policy), and before this mutex existed none
	// of them serialised against any of the others.
	//
	// The way it stays correct is that every such caller goes through
	// UpdateSettings. A lock is only a boundary while nobody walks around it,
	// and a new read-modify-write built out of GetSettings and PutSettings
	// would be exactly that walk.
	//
	// GetSettings and PutSettings deliberately do NOT take it. GetSettings
	// calls PutSettings to seed defaults on first run, so a lock in either
	// would deadlock the moment UpdateSettings -- which holds this across both
	// -- ran against a fresh database.
	settingsMu sync.Mutex
}

// Option configures a DB at Open time.
type Option func(*DB)

// WithPasswordCost lowers the bcrypt cost used for password hashing.
//
// FOR TESTS, and for one measured reason. bcrypt at DefaultCost costs 1.40
// SECONDS for a single hash-and-compare under -race, against 0.02s at MinCost
// -- seventy times. Every test that stands up a server creates a user and logs
// in, so internal/api was paying it on nearly every test: that package ran 709s
// of a 900s ceiling on the CI attempt that passed, and hit the full 900s and
// timed out on the attempt before it. The suite was a coin flip decided by
// runner speed, and the timeout landed on whichever test happened to be running
// -- which made an innocent test look flaky.
//
// The cost factor is a production hardening parameter, not behaviour: the same
// code path runs at any cost, so nothing is left untested by lowering it. What
// WAS going untested is every assertion after the fifteen-minute mark, because
// the binary was killed before reaching them.
//
// Production must never call this, and TestTheDefaultPasswordCostIsNotWeakened
// is what says so.
func WithPasswordCost(cost int) Option {
	return func(d *DB) { d.passwordCost = cost }
}

// WithSecretBox encrypts destination stream keys at rest with box.
//
// Passing it turns on three things at once, and they only make sense together:
// CreateDestination and UpdateDestination seal the key instead of storing it,
// Open backfills any row still carrying a plaintext one, and a row whose
// ciphertext will not open is disabled rather than started with a wrong key.
//
// OMITTING IT IS SUPPORTED and is not a degraded mode with a warning: the keys
// stay in the plaintext column, which is what every install did before this
// existed. cmd/polyemesis always passes a box; the tests that do not are
// asserting the unencrypted path still works, because an embedded user of this
// package has no key file to give it.
func WithSecretBox(box *secrets.Box) Option {
	return func(d *DB) { d.box = box }
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string, opts ...Option) (*DB, error) {
	// WAL keeps the API responsive while the retention sweeper writes, and
	// busy_timeout turns the rare writer collision into a short wait instead
	// of an SQLITE_BUSY error surfacing to the user.
	//
	// secure_delete(ON) makes SQLite zero the bytes of a deleted or shortened
	// cell instead of only unlinking it. THIS FILE HOLDS STREAM KEYS, and
	// without it the seal-at-rest backfill leaves the plaintext it replaced
	// legible in freed pages: measured with 60 pre-0.7.0 destinations, 62
	// copies of the plaintext were still greppable out of the raw .db bytes
	// after the backfill had blanked every stream_key column. See
	// TestTheBackfillLeavesNoPlaintextInTheRawDatabaseBytes.
	//
	// IT MUST BE SET HERE, IN THE DSN, not executed later: it governs writes
	// made after it is on, so a pragma issued once the backfill has already
	// overwritten the rows would protect nothing. The default under
	// modernc.org/sqlite is 0 -- measured, and worth stating because CPython's
	// bundled SQLite compiles it to 1, so the same experiment run in Python
	// reports this bug as refuted.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)&_pragma=secure_delete(ON)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// modernc's driver is not safe to hammer with many concurrent writers;
	// one connection removes a whole class of lock contention and this
	// workload is nowhere near needing more.
	sqldb.SetMaxOpenConns(1)

	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	// THE FILE HOLDS EVERY DESTINATION STREAM KEY IN PLAINTEXT, and SQLite
	// creates it under the process umask -- 0644 on a default install, measured.
	// Issue #297.
	//
	// A stream key is a credential: whoever reads one can broadcast to the
	// owner's channel. Both shipped deployments put this file in a
	// world-traversable directory -- the unit file's install notes create
	// /var/lib/polyemesis with a plain `mkdir -p` under umask 022, and the
	// Dockerfile does the same for /data -- and neither sets UMask=, so nothing
	// narrowed the mode at runtime either.
	//
	// AFTER Ping, which is what forces SQLite to create the file. Chmod before
	// that would race a file that does not exist yet.
	//
	// THE SIDECARS FOLLOW FROM THIS ONE. SQLite creates -wal and -shm with the
	// permissions of the main database rather than from the umask, so securing
	// this file secures the pages that have not been checkpointed yet -- which
	// matter just as much, since a reader who cannot open the database can
	// still read recent writes out of the log.
	//
	// fsperm rather than os.Chmod(0600): a FileMode is a Unix concept that
	// Windows discards, so a literal mode here would compile, succeed, and
	// restrict nothing on that platform. See internal/fsperm.
	// THE SIDECARS ARE SECURED EXPLICITLY, and the first version of this fix
	// was wrong to assume they inherited.
	//
	// SQLite gives -wal and -shm the mode of the main database WHEN IT CREATES
	// THEM. On an existing install it does not create them -- they are already
	// there, at whatever mode the umask gave them before this code existed --
	// so chmodding only the database left them readable. Found by deploying to
	// a real server running an older build:
	//
	//     -rw-------  polyemesis.db        <- fixed
	//     -rw-r--r--  polyemesis.db-wal    <- still world-readable
	//     -rw-r--r--  polyemesis.db-shm
	//
	// and `sudo -u nobody head -c 32 polyemesis.db-wal` succeeded. The unit
	// test missed it because it creates a fresh database, which is the one case
	// where inheritance does happen.
	//
	// -wal is the one that matters: it holds committed pages that have not been
	// checkpointed, so a reader who cannot open the database still sees recent
	// writes -- including a stream key added moments ago.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			// Absent is fine: -shm exists only under some journal modes, and
			// -wal only once something has been written.
			continue
		}
		if err := fsperm.SecureFile(p); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("restrict %s: %w", p, err)
		}
	}
	if _, err := sqldb.Exec(schemaSQL); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// The default is applied BEFORE the options, so an install that passes none
	// gets the production cost and a test that passes one wins.
	d := &DB{sql: sqldb, passwordCost: bcrypt.DefaultCost}
	for _, o := range opts {
		if o != nil {
			o(d)
		}
	}
	// Adds destinations.rendition_id to a database created before renditions
	// existed; CREATE TABLE IF NOT EXISTS cannot do it.
	if err := d.MigrateRenditions(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Same story one release later: the expert-mode columns, plus draining the
	// sidecar table they were first stored in.
	if err := d.MigrateDestinationExpertArgs(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// And once more for the rendition aspect-conversion columns.
	if err := d.MigratePlatformAccountScopeVer(); err != nil {
		return nil, err
	}
	if err := d.MigrateRenditionAspect(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// users.token_epoch, so a password change can revoke sessions that are
	// already signed and in somebody's cookie jar.
	if err := d.MigrateUserTokenEpoch(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// api_tokens.scope, so a token minted for a monitoring script is not
	// automatically a token that can delete a destination. Tokens that predate
	// the column are backfilled to 'admin', which is what they already were.
	if err := d.MigrateAPITokenScope(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Last, because it reads settings and writes to destinations, renditions
	// and recordings: every column those tables are going to have must already
	// be there. It also creates the first source from the existing ingest
	// configuration, which is what keeps an upgraded install reachable by the
	// encoder that was already pointed at it.
	if err := d.MigrateSources(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// AFTER every migration, because it writes to two columns the migrations
	// above are what create, and because it is the one step that needs the
	// options applied -- it does nothing at all without a box.
	//
	// A DATA migration rather than a schema one, which is why it does not live
	// with the ALTERs: it is guarded by "is there still plaintext here", not by
	// "did this pass add the column", so it stays correct if it is interrupted
	// and correct if it runs a thousand times.
	if err := d.backfillDestinationStreamKeys(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// SQL exposes the underlying handle for the rare query that does not warrant
// a typed store method.
func (d *DB) SQL() *sql.DB { return d.sql }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }
