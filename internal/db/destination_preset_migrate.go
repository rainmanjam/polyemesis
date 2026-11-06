package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// MigrateDestinationPresetID adds destinations.preset_id: the catalogue preset a
// destination was created from, NULL for one made by hand. Nullable and
// defaultless, so an upgrade changes nothing a running install can see.
//
// THE BACKFILL RUNS ONCE, in the same transaction that adds the column, and never
// again: afterwards a NULL is an operator's own choice (a destination typed in
// by hand) and not something to second-guess on every boot. A row is given a
// preset only when its ingest URL names exactly one: same scheme and host as a
// catalogue preset's fixed URL, and a platform that preset saves as. The two
// presets that share a host and differ only by scheme (Twitch's rtmp and rtmps)
// are told apart by the scheme. Anything a template host stands in for
// ("{owncast-host}"), and anything two presets claim, stays NULL.
func (d *DB) MigrateDestinationPresetID() error {
	has, err := columnExists(d.sql, "destinations", "preset_id")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if has {
		return nil
	}
	// ONE TRANSACTION, because the column's existence is what says "done". SQLite
	// DDL is transactional, so a failure in the backfill rolls the ALTER back and
	// the next boot starts again from the top, rather than finding the column
	// and skipping a backfill that never finished.
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin preset_id migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE destinations ADD COLUMN preset_id TEXT`); err != nil {
		return fmt.Errorf("add destinations.preset_id: %w", err)
	}
	if err := backfillDestinationPresetIDs(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit preset_id migration: %w", err)
	}
	return nil
}

func backfillDestinationPresetIDs(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, platform, url FROM destinations WHERE kind = ? AND url <> ''`, string(DestRTMP))
	if err != nil {
		return fmt.Errorf("read destinations for preset backfill: %w", err)
	}
	type match struct {
		id     int64
		preset string
	}
	var found []match
	for rows.Next() {
		var (
			id       int64
			platform string
			raw      string
		)
		if err := rows.Scan(&id, &platform, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan destination for preset backfill: %w", err)
		}
		if p, ok := presetForIngest(Platform(platform), raw); ok {
			found = append(found, match{id, p})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read destinations for preset backfill: %w", err)
	}
	rows.Close()
	for _, m := range found {
		if _, err := tx.Exec(`UPDATE destinations SET preset_id = ? WHERE id = ? AND preset_id IS NULL`, m.preset, m.id); err != nil {
			return fmt.Errorf("backfill destinations.preset_id: %w", err)
		}
	}
	return nil
}

// presetForIngest is the one catalogue preset whose fixed ingest URL has the
// same scheme and host as raw, among those the platform could have come from.
func presetForIngest(plat Platform, raw string) (string, bool) {
	scheme, host, ok := ingestKey(raw)
	if !ok {
		return "", false
	}
	var hit string
	for _, p := range destinationPresets {
		if p.URL == "" || strings.ContainsAny(p.URL, "{}") {
			continue
		}
		pscheme, phost, pok := ingestKey(p.URL)
		if !pok || pscheme != scheme || phost != host {
			continue
		}
		if !presetFitsPlatform(p, plat) {
			continue
		}
		if hit != "" {
			return "", false // two presets claim this ingest
		}
		hit = p.ID
	}
	return hit, hit != ""
}

func ingestKey(raw string) (scheme, host string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	return strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), true
}
