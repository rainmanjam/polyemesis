package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

const scheduleColumns = `id, name, enabled, action, kind, destination_ids, tz,
	at_minutes, days, run_at, grace_seconds, last_run_at, created_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	scheduleListQuery    = `SELECT ` + scheduleColumns + ` FROM schedules ORDER BY id`
	scheduleEnabledQuery = `SELECT ` + scheduleColumns + ` FROM schedules WHERE enabled = 1 ORDER BY id`
	scheduleByIDQuery    = `SELECT ` + scheduleColumns + ` FROM schedules WHERE id = ?`
)

// scanSchedule reads one row. Instants come back as UTC because that is how
// they went in — the zone a recurring schedule is READ in lives in tz, not in
// the stored timestamps.
func scanSchedule(s interface{ Scan(...any) error }) (*scheduler.Schedule, error) {
	var (
		sc                 scheduler.Schedule
		enabled            int
		action, kind       string
		destJSON, daysJSON string
		runAt, lastRun     int64
		created, updated   int64
	)
	if err := s.Scan(&sc.ID, &sc.Name, &enabled, &action, &kind, &destJSON, &sc.TZ,
		&sc.AtMinutes, &daysJSON, &runAt, &sc.GraceSeconds, &lastRun,
		&created, &updated); err != nil {
		return nil, err
	}
	sc.Enabled = enabled != 0
	sc.Action = scheduler.Action(action)
	sc.Kind = scheduler.Kind(kind)
	// A collection that will not parse is left empty rather than failing the
	// read. Empty destinations means "every destination" and empty days makes a
	// weekly schedule invalid, so both are visible to the operator instead of
	// taking the whole list down with them.
	if destJSON != "" && destJSON != "[]" {
		var ids []int64
		if err := json.Unmarshal([]byte(destJSON), &ids); err == nil {
			sc.DestinationIDs = ids
		}
	}
	if daysJSON != "" && daysJSON != "[]" {
		var days []int
		if err := json.Unmarshal([]byte(daysJSON), &days); err == nil {
			for _, d := range days {
				sc.Days = append(sc.Days, time.Weekday(d))
			}
		}
	}
	if runAt > 0 {
		sc.RunAt = time.Unix(runAt, 0).UTC()
	}
	if lastRun > 0 {
		sc.LastRunAt = time.Unix(lastRun, 0).UTC()
	}
	sc.CreatedAt = time.Unix(created, 0)
	sc.UpdatedAt = time.Unix(updated, 0)
	out := sc.Normalized()
	return &out, nil
}

// ListSchedules returns every schedule, oldest first.
func (d *DB) ListSchedules() ([]scheduler.Schedule, error) {
	return d.querySchedules(scheduleListQuery)
}

// Schedules returns the enabled schedules. It is what satisfies
// scheduler.Store, so a switched-off schedule is never evaluated.
func (d *DB) Schedules() ([]scheduler.Schedule, error) {
	return d.querySchedules(scheduleEnabledQuery)
}

func (d *DB) querySchedules(query string) ([]scheduler.Schedule, error) {
	rows, err := d.sql.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []scheduler.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

// GetSchedule loads one schedule.
func (d *DB) GetSchedule(id int64) (*scheduler.Schedule, error) {
	sc, err := scanSchedule(d.sql.QueryRow(scheduleByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sc, err
}

// CreateSchedule stores a new schedule.
func (d *DB) CreateSchedule(s *scheduler.Schedule) (*scheduler.Schedule, error) {
	norm := s.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	dest, days, err := marshalScheduleLists(norm)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO schedules
		(name, enabled, action, kind, destination_ids, tz, at_minutes, days,
		 run_at, grace_seconds, last_run_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		norm.Name, boolToInt(norm.Enabled), string(norm.Action), string(norm.Kind),
		dest, norm.TZ, norm.AtMinutes, days, unixOrZero(norm.RunAt),
		norm.GraceSeconds, unixOrZero(norm.LastRunAt), now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetSchedule(id)
}

// UpdateSchedule replaces a schedule in place.
//
// last_run_at is deliberately not written here: an edit must not resurrect a
// window that was already handled, and must not mark one as handled that was
// not. Only MarkScheduleRun moves it.
func (d *DB) UpdateSchedule(s *scheduler.Schedule) (*scheduler.Schedule, error) {
	norm := s.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	dest, days, err := marshalScheduleLists(norm)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE schedules SET
		name=?, enabled=?, action=?, kind=?, destination_ids=?, tz=?,
		at_minutes=?, days=?, run_at=?, grace_seconds=?, updated_at=? WHERE id=?`,
		norm.Name, boolToInt(norm.Enabled), string(norm.Action), string(norm.Kind),
		dest, norm.TZ, norm.AtMinutes, days, unixOrZero(norm.RunAt),
		norm.GraceSeconds, time.Now().Unix(), norm.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetSchedule(norm.ID)
}

// SetScheduleEnabled flips one schedule without touching the rest of it.
func (d *DB) SetScheduleEnabled(id int64, enabled bool) error {
	res, err := d.sql.Exec(`UPDATE schedules SET enabled=?, updated_at=? WHERE id=?`,
		boolToInt(enabled), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkScheduleRun records an occurrence as handled.
//
// The guard matters: sweeps can overlap and an out-of-order write would move
// the marker BACKWARDS, which is how an occurrence gets acted on twice.
func (d *DB) MarkScheduleRun(id int64, at time.Time) error {
	_, err := d.sql.Exec(`UPDATE schedules SET last_run_at=?, updated_at=?
		WHERE id=? AND last_run_at < ?`,
		at.UTC().Unix(), time.Now().Unix(), id, at.UTC().Unix())
	return err
}

// DeleteSchedule removes a schedule.
func (d *DB) DeleteSchedule(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func marshalScheduleLists(s scheduler.Schedule) (dest, days string, err error) {
	dest, days = "[]", "[]"
	if len(s.DestinationIDs) > 0 {
		b, e := json.Marshal(s.DestinationIDs)
		if e != nil {
			return "", "", e
		}
		dest = string(b)
	}
	if len(s.Days) > 0 {
		nums := make([]int, len(s.Days))
		for i, d := range s.Days {
			nums[i] = int(d)
		}
		b, e := json.Marshal(nums)
		if e != nil {
			return "", "", e
		}
		days = string(b)
	}
	return dest, days, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}
