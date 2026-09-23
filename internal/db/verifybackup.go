package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("backup has no secret.key, so every destination would "+
			"come back disabled and the restore would read as successful until go-live: %w", err)
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
