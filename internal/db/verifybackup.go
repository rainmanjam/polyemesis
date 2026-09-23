package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// VerifyBackup answers the only question a backup has to answer: does it open.
//
// WHAT THIS REPLACES. The generated update.sh checked that polyemesis.db and
// secret.key EXIST and then printed "backup verified". Existence is not the
// property that matters. Migrations run forward only, so this copy is the
// single way back from an upgrade, and the ways it can exist without being
// usable are exactly the ways nobody notices until the day they restore: a
// database copied while the server was writing to it, a truncated file, an
// archive that unpacked into the wrong shape, a full disk that stopped the copy
// halfway. Every one of those leaves a file of plausible size.
//
// NOT db.Open. Open runs migrations, and migrating the backup is precisely the
// thing that must not happen -- it would move the copy forward to the schema
// the operator is trying to keep a way back FROM. This opens the file directly,
// asks SQLite to walk it, and reads the schema. It never writes DDL.
//
// The `-wal` sidecar is deliberately opened along with the main file rather
// than ignored: a copy taken from a live database keeps committed data there
// that the main file does not have, so a check that skipped it would pass on a
// database that is missing the operator's last few minutes of work.
//
// IT OPENS A PRIVATE COPY, NEVER THE BACKUP ITSELF. Opening the backup's own
// files read-write -- which is what this used to do -- makes SQLite fold the
// -wal into the main file on open and delete both sidecars on close, so the one
// copy that is the way back came out of the check with different bytes, while
// INSTALL.md says the check writes nothing. On a read-only mount, which is how
// the Docker update.sh hands the backup over (`-v "$verify_dir:/backup:ro"`),
// the same open cannot create the -shm and fails, refusing every Docker
// upgrade. The read-only DSN flags do not rescue it: `mode=ro` still needs to
// create the -shm when a -wal is present, and `immutable=1` ignores the -wal
// altogether -- the missing-last-minutes failure above. A copy is the one form
// that is both hands-off towards the backup and faithful to its -wal, and the
// only thing ever written is a scratch directory this function makes and
// removes.
func VerifyBackup(dir string) error {
	dbPath := filepath.Join(dir, "polyemesis.db")
	keyPath := filepath.Join(dir, "secret.key")

	if st, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("backup has no polyemesis.db: %w", err)
	} else if st.Size() == 0 {
		return errors.New("backup's polyemesis.db is zero bytes")
	}

	// Kept as a separate error from the database one, because the remedy
	// differs: a missing key is unrecoverable and means taking the backup
	// again, while a failing integrity check may mean stopping the server
	// first. See the note in the generated update.sh.
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup has no secret.key, so every destination would "+
			"come back disabled and the restore would read as successful until go-live: %w", err)
	}
	// PRESENT IS NOT ENOUGH. This was the whole key check, so an empty file, a
	// directory, or another install's key all passed. secrets.Load is the parser
	// boot uses, so what passes here is what the restored server will start
	// with; whether it is THIS database's key is asked below, of the rows it
	// sealed.
	box, err := secrets.Load(keyPath)
	if err != nil {
		return fmt.Errorf("backup's secret.key is not a key the server can start with, "+
			"so the restore would refuse to boot: %w", err)
	}

	scratch, err := os.MkdirTemp("", "polyemesis-verify-*")
	if err != nil {
		return fmt.Errorf("cannot make a scratch directory to verify a copy of the backup in "+
			"(the backup itself is never opened for writing; point TMPDIR somewhere writable): %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	copyPath, err := copyForVerify(dbPath, scratch)
	if err != nil {
		return err
	}

	sqldb, err := sql.Open("sqlite", copyPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("backup's polyemesis.db will not open: %w", err)
	}
	defer func() { _ = sqldb.Close() }()
	sqldb.SetMaxOpenConns(1)

	// integrity_check reports "ok" as a single row, or one row per problem.
	rows, err := sqldb.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("backup's polyemesis.db could not be read: %w", err)
	}
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return fmt.Errorf("reading integrity_check: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(s), "ok") {
			problems = append(problems, strings.TrimSpace(s))
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("reading integrity_check: %w", err)
	}
	_ = rows.Close()
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("(and %d more)", len(problems)-5))
		}
		return fmt.Errorf("backup's polyemesis.db failed its integrity check: %s",
			strings.Join(problems, "; "))
	}

	// A file can pass integrity_check and still be the wrong file -- an empty
	// database SQLite created for us, say, because something copied a path that
	// did not exist. Ask for the schema polyemesis actually writes.
	var tables int
	if err := sqldb.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sources','destinations','settings')`,
	).Scan(&tables); err != nil {
		return fmt.Errorf("backup's polyemesis.db has no readable schema: %w", err)
	}
	if tables == 0 {
		return errors.New("backup's polyemesis.db holds none of polyemesis's tables, " +
			"so it is not this server's database")
	}
	return checkKeyOpensSealedValues(sqldb, box)
}

// sealedColumn names one column this package writes with secrets.Box.Seal,
// and what an operator does to re-seal a value in it that this server's own
// key cannot open (see checkKeyOpensSealedValues for why that is needed).
type sealedColumn struct{ table, column, remedy string }

// sealedColumns is every column holding secretbox ciphertext.
// TestVerifyBackupKnowsEverySealedColumn holds it to the schema, so a new
// sealed column cannot be added without the key check covering it, and
// TestEverySealedColumnSaysHowToReseal holds each to a remedy.
var sealedColumns = []sealedColumn{
	{"destinations", "stream_key_enc",
		"re-enter the stream key on each affected destination (Dashboard, edit the destination), or remove it"},
	{"destinations", "backup_stream_key_enc",
		"re-enter the backup stream key on each affected destination (Dashboard, edit the destination), or remove it"},
	{"mqtt_creds", "password_enc",
		"re-enter or clear the MQTT broker password in Settings"},
	{"automod_creds", "key_enc",
		"re-enter or clear the automod model API key in Settings"},
	{"platform_creds", "client_secret_enc",
		"re-enter or remove the platform's client secret in Settings"},
	{"platform_accounts", "access_token_enc",
		"reconnect the platform account in Settings, or disconnect it"},
	{"platform_accounts", "refresh_token_enc",
		"reconnect the platform account in Settings, or disconnect it"},
	{"hooks", "secret",
		"set a new secret on the hook in Automation, or delete the hook"},
}

// checkKeyOpensSealedValues trial-decrypts the backup's sealed values with its
// secret.key, and refuses when a sealed column has values and the key opens
// none of them.
//
// "OPENS NONE", NOT "FAILS ONE". A live database can already hold a row its own
// key cannot open -- a destination keyUnreadable since an earlier bad restore
// is the case that shipped -- and refusing the backup over that row would
// refuse the only good copy. A key from another install opens nothing at all,
// so one success per column is proof enough and one failure is proof of
// nothing. Per column rather than once overall, because a restore needs every
// kind of credential back, not just the first one the check happens to try.
//
// That rationale only holds where a column has several values to try. For a
// singleton -- mqtt_creds and automod_creds are CHECK (id = 1) -- or any column
// that happens to hold one value, "opens none" and "fails one" are the same
// test, and a value this server's own key cannot open is indistinguishable
// from a foreign key. The backup is still refused (the restore would bring
// that credential back unreadable either way), but the refusal cannot just say
// "back up again with this server's key": that is the backup it is looking at,
// and the next one would fail the same way, refusing every update.sh upgrade
// with advice that leads round in a loop. So it names both causes, and for
// the second gives the column's remedy -- re-seal or clear the value from the
// console -- which is the only thing that changes what the next backup holds.
//
// A backup with no sealed values anywhere -- a fresh install, or one with no
// credentials yet -- has nothing to try the key on, and passes on the parse
// alone: there is nothing it could fail to open.
//
// A table or column the backup's schema does not have yet is skipped, not an
// error: an older backup is still a backup.
func checkKeyOpensSealedValues(sqldb *sql.DB, box *secrets.Box) error {
	for _, c := range sealedColumns {
		var present int
		if err := sqldb.QueryRow(
			`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column,
		).Scan(&present); err != nil {
			return fmt.Errorf("backup's polyemesis.db schema could not be read: %w", err)
		}
		if present == 0 {
			continue
		}
		// Identifiers come from the fixed list above, never from input.
		rows, err := sqldb.Query(`SELECT "` + c.column + `" FROM "` + c.table +
			`" WHERE "` + c.column + `" IS NOT NULL AND length("` + c.column + `") > 0`)
		if err != nil {
			return fmt.Errorf("backup's %s.%s could not be read: %w", c.table, c.column, err)
		}
		tried, opened := 0, false
		for rows.Next() && !opened {
			var sealed []byte
			if err := rows.Scan(&sealed); err != nil {
				_ = rows.Close()
				return fmt.Errorf("backup's %s.%s could not be read: %w", c.table, c.column, err)
			}
			tried++
			if _, err := box.Open(sealed); err == nil {
				opened = true
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("backup's %s.%s could not be read: %w", c.table, c.column, err)
		}
		if tried > 0 && !opened {
			return fmt.Errorf("backup's secret.key opens none of the %d sealed value(s) in %s.%s, "+
				"so a restore would bring every one of those credentials back unreadable. "+
				"Either the key is from another install -- take the backup again with this "+
				"server's own secret.key -- or this server cannot open these values either "+
				"(sealed under a key it no longer has, say after an earlier restore), and a "+
				"new backup will fail the same way: %s, then take the backup again",
				tried, c.table, c.column, c.remedy)
		}
	}
	return nil
}

// copyForVerify copies polyemesis.db and, when present, its -wal into scratch,
// and returns the copied database's path.
//
// The -shm is deliberately NOT copied. It is SQLite's index into the -wal, not
// data: the first connection to open the copy rebuilds it from the -wal, which
// is what a restore does too. A stale one carried over from a live server
// could only disagree with the -wal it is meant to describe.
func copyForVerify(dbPath, scratch string) (string, error) {
	dst := filepath.Join(scratch, filepath.Base(dbPath))
	if err := copyFile(dbPath, dst); err != nil {
		return "", fmt.Errorf("backup's polyemesis.db could not be read: %w", err)
	}
	wal := dbPath + "-wal"
	if _, err := os.Stat(wal); err == nil {
		if err := copyFile(wal, dst+"-wal"); err != nil {
			return "", fmt.Errorf("backup's polyemesis.db-wal could not be read: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("backup's polyemesis.db-wal could not be read: %w", err)
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
