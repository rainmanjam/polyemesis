package db

import (
	"database/sql"
	"errors"
	"fmt"
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

	sqldb, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
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
