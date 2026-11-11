package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ============================================================================
// WHAT A DESTINATION DELETE MUST TAKE WITH IT.
//
// Three things refer to a destination by id with no foreign key, on purpose,
// and so outlive it unless the delete removes them:
//
//   - schedules.destination_ids, a JSON list. A start or stop schedule naming
//     a deleted id errors on every occurrence for ever ("schedule cannot set
//     destination"), and nothing in the console says which id or why.
//   - destination_rollover, keyed by destination id. ListEnabledRollovers
//     keeps returning the orphan and the planner pays two queries per sweep
//     per orphan, for ever.
//
// Both are pruned HERE, in the same transaction as the delete, so every path
// that removes a destination -- the destination delete and the programme
// delete, whose ON DELETE CASCADE drops every destination on the programme --
// takes them along. A trigger would do the same and is not used: the schedule
// rule below has a case that must never write "[]", and that is easier to read
// and test as Go than as SQL.
//
// ONE CONNECTION (SetMaxOpenConns(1)), so every read below goes through tx:
// a d.sql call issued while the transaction is open waits for ever.
// ============================================================================

// SchedulePrune is what deleting destinations did to the schedules that named
// them, for the API to say in its response.
type SchedulePrune struct {
	// Narrowed names the schedules that lost a deleted destination and still
	// act on the others they name.
	Narrowed []string
	// Disabled names the schedules whose every named destination was deleted.
	// They are switched off with the dead ids left in place; see
	// pruneScheduleTargets for why the list is never emptied.
	Disabled []string
}

// DeleteDestinationPruned removes a destination and everything that names it
// by id: its rollover row, and its id in every schedule. The report says which
// schedules changed.
func (d *DB) DeleteDestinationPruned(id int64) (SchedulePrune, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return SchedulePrune{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM destinations WHERE id = ?`, id)
	if err != nil {
		return SchedulePrune{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SchedulePrune{}, ErrNotFound
	}
	prune, err := destinationsGone(tx, []int64{id}, time.Now())
	if err != nil {
		return SchedulePrune{}, err
	}
	return prune, tx.Commit()
}

// ErrDestinationsChanged is a programme delete confirmed against a set of
// destinations that is no longer the set: something was added or removed
// between the confirmation and the delete. Nothing is deleted.
var ErrDestinationsChanged = errors.New("the programme's destinations changed since the delete was confirmed")

// DeleteSourcePruned removes a programme. Its destinations and renditions go
// by ON DELETE CASCADE, and the destinations' ids are pruned from schedules and
// rollover exactly as DeleteDestinationPruned prunes one.
//
// The ids are read INSIDE the transaction, before the delete: once the cascade
// has run there is no query left that says which destinations were on it.
//
// confirmed is the destinations the operator's delete was checked against, by
// id. It is compared inside the same transaction, so a destination created
// after the handler read them -- while it was cancelling platform events,
// which can take seconds -- refuses the delete instead of being swept away
// unseen.
//
// THE IDS, NOT THEIR COUNT. A count cannot see one destination deleted and
// another created in that window: the number the operator confirmed is still
// the number, and the new destination would go with the programme. A nil or
// empty confirmed is a programme with no destinations, never "do not check".
func (d *DB) DeleteSourcePruned(id int64, confirmed []int64) (SchedulePrune, error) {
	return d.deleteSource(id, confirmed, true)
}

// deleteSource is DeleteSourcePruned, comparing the confirmed set only when
// check is set. DeleteSource, which no operator confirmed, does not.
func (d *DB) deleteSource(id int64, confirmed []int64, check bool) (SchedulePrune, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return SchedulePrune{}, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := destinationIDsOfSource(tx, id)
	if err != nil {
		return SchedulePrune{}, err
	}
	if check && !sameIDs(ids, confirmed) {
		return SchedulePrune{}, fmt.Errorf("%w: it has %d (ids %v), and the delete was confirmed for %d (ids %v)",
			ErrDestinationsChanged, len(ids), sortedIDs(ids), len(confirmed), sortedIDs(confirmed))
	}
	res, err := tx.Exec(`DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return SchedulePrune{}, err
	}
	if aff, _ := res.RowsAffected(); aff == 0 {
		return SchedulePrune{}, ErrSourceNotFound
	}
	prune, err := destinationsGone(tx, ids, time.Now())
	if err != nil {
		return SchedulePrune{}, err
	}
	return prune, tx.Commit()
}

// sameIDs reports whether two id lists hold the same ids, in any order.
func sameIDs(a, b []int64) bool {
	return slices.Equal(sortedIDs(a), sortedIDs(b))
}

// sortedIDs is a sorted copy: the caller's slice is never reordered.
func sortedIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func destinationIDsOfSource(tx *sql.Tx, sourceID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT id FROM destinations WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// destinationsGone removes what names these (already deleted) destinations.
func destinationsGone(tx *sql.Tx, ids []int64, now time.Time) (SchedulePrune, error) {
	if len(ids) == 0 {
		return SchedulePrune{}, nil
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM destination_rollover WHERE destination_id = ?`, id); err != nil {
			return SchedulePrune{}, fmt.Errorf("drop rollover settings of destination %d: %w", id, err)
		}
	}
	return pruneScheduleTargets(tx, ids, now)
}

// pruneScheduleTargets removes these destination ids from every schedule that
// names them.
//
// A schedule that still names something else keeps those and loses the dead
// id. A schedule whose EVERY named destination is gone is switched off with
// its list left as it was, and never written as "[]".
//
// NEVER "[]", AND THIS IS THE DATA-SAFETY RULE OF THE WHOLE FUNCTION.
// MigrateScheduleAllDestinations runs on every Open and folds any start or
// stop schedule whose destination_ids is "[]" into all_destinations = 1 --
// whether or not it is enabled. A stop schedule pruned to an empty list would
// come back from the next restart as "stop every destination on this install",
// and an operator who re-enabled it would take the whole broadcast down. A
// switched-off schedule naming a dead id is inert, visible, and says what it
// was for.
//
// Schedules with all_destinations set name nobody by id and are untouched, and
// so are the playlist actions, which do not act on destinations.
func pruneScheduleTargets(tx *sql.Tx, gone []int64, now time.Time) (SchedulePrune, error) {
	dead := make(map[int64]bool, len(gone))
	for _, id := range gone {
		dead[id] = true
	}
	hits, err := schedulesNamingDead(tx, dead)
	if err != nil {
		return SchedulePrune{}, err
	}
	var out SchedulePrune
	for _, r := range hits {
		if err := pruneSchedule(tx, r, dead, now, &out); err != nil {
			return SchedulePrune{}, err
		}
	}
	return out, nil
}

// pruneRow is a schedule that names at least one deleted destination.
type pruneRow struct {
	id      int64
	name    string
	enabled bool
	ids     []int64
}

// schedulesNamingDead returns the schedules, in id order, that name a deleted
// destination. A list that will not parse is left out.
func schedulesNamingDead(tx *sql.Tx, dead map[int64]bool) ([]pruneRow, error) {
	rows, err := tx.Query(`SELECT id, name, enabled, destination_ids FROM schedules
		WHERE all_destinations = 0 AND action IN ('start', 'stop')
		  AND destination_ids NOT IN ('[]', '', 'null')
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var hits []pruneRow
	for rows.Next() {
		var (
			r       pruneRow
			enabled int
			raw     string
		)
		if err := rows.Scan(&r.id, &r.name, &enabled, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		// A list that will not parse is left alone: scanSchedule already reads
		// it as empty and Validate shows it to the operator as invalid.
		if json.Unmarshal([]byte(raw), &r.ids) != nil {
			continue
		}
		r.enabled = enabled != 0
		for _, id := range r.ids {
			if dead[id] {
				hits = append(hits, r)
				break
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return hits, nil
}

// pruneSchedule drops the deleted destinations from one schedule. A schedule
// left naming nothing is switched off, and counted in out.Disabled only if this
// is what switched it off; otherwise it is counted in out.Narrowed.
func pruneSchedule(tx *sql.Tx, r pruneRow, dead map[int64]bool, now time.Time, out *SchedulePrune) error {
	kept := make([]int64, 0, len(r.ids))
	for _, id := range r.ids {
		if !dead[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		if _, err := tx.Exec(`UPDATE schedules SET enabled = 0, updated_at = ? WHERE id = ?`,
			now.Unix(), r.id); err != nil {
			return err
		}
		// Reported only when this delete is what switched it off: one that
		// was already off has nothing new to tell the operator.
		if r.enabled {
			out.Disabled = append(out.Disabled, r.name)
		}
		return nil
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE schedules SET destination_ids = ?, updated_at = ? WHERE id = ?`,
		string(b), now.Unix(), r.id); err != nil {
		return err
	}
	out.Narrowed = append(out.Narrowed, r.name)
	return nil
}
