package db

import (
	"database/sql"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Storage for recurring broadcast templates and their occurrences (#18).
//
// The template type and the occurrence type are defined in the scheduler package, because the
// schedule is what they hang off and the planned rollover lane (#19) reads them. This file only
// stores them. Schema and idempotent creation are in wave3_recurring_migrate.go.

// ErrOccurrenceClaimed is returned by ClaimOccurrence when a row already exists for the same schedule,
// destination and instant. It is the signal that another sweep owns this occurrence, and it is not a
// failure: the caller simply does not create a second platform event.
var ErrOccurrenceClaimed = errors.New("this occurrence is already claimed")

const templateColumns = `schedule_id, title_pattern, description, privacy, category, lookahead,
	(thumbnail IS NOT NULL AND length(thumbnail) > 0), thumbnail_mime, last_error, last_error_at,
	created_at, updated_at`

const occurrenceColumns = `id, schedule_id, destination_id, occurrence_at, state,
	platform_event_id, title, created_at, updated_at`

// The reads below are whole compile-time constants, for the reason chat.go records: a query assembled
// at the call site is indistinguishable from one that interpolates a variable.
const (
	templateListQuery    = `SELECT ` + templateColumns + ` FROM broadcast_templates ORDER BY schedule_id`
	templateByIDQuery    = `SELECT ` + templateColumns + ` FROM broadcast_templates WHERE schedule_id = ?`
	occurrenceByIDQuery  = `SELECT ` + occurrenceColumns + ` FROM recurring_occurrences WHERE id = ?`
	occurrenceByKeyQuery = `SELECT ` + occurrenceColumns + ` FROM recurring_occurrences
		WHERE schedule_id = ? AND destination_id = ? AND occurrence_at = ?`
	occurrenceWindowQuery = `SELECT ` + occurrenceColumns + ` FROM recurring_occurrences
		WHERE occurrence_at >= ? AND occurrence_at < ? AND state IN ('upcoming', 'skipped')
		ORDER BY occurrence_at, destination_id`
	occurrenceOpenQuery = `SELECT ` + occurrenceColumns + ` FROM recurring_occurrences
		WHERE state IN ('pending', 'upcoming', 'stranded') ORDER BY occurrence_at, id`
)

// SaveBroadcastTemplate creates or updates the template for one schedule. It writes the text fields
// and never the thumbnail, so editing the title does not drop the picture. Validation is the
// scheduler's; a template that fails it is refused before any SQL runs.
func (d *DB) SaveBroadcastTemplate(t scheduler.BroadcastTemplate) (*scheduler.BroadcastTemplate, error) {
	norm := t.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	_, err := d.sql.Exec(`INSERT INTO broadcast_templates
		(schedule_id, title_pattern, description, privacy, category, lookahead, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(schedule_id) DO UPDATE SET
		  title_pattern=excluded.title_pattern, description=excluded.description,
		  privacy=excluded.privacy, category=excluded.category, lookahead=excluded.lookahead,
		  updated_at=excluded.updated_at`,
		norm.ScheduleID, norm.TitlePattern, norm.Description, norm.Privacy, norm.Category,
		norm.Lookahead, now, now)
	if err != nil {
		return nil, err
	}
	return d.GetBroadcastTemplate(norm.ScheduleID)
}

// SetBroadcastTemplateThumbnail stores the picture for an existing template. A template that does
// not exist is ErrNotFound: a thumbnail has no home without its template, and creating one here
// would leave a half-made row with an empty title.
func (d *DB) SetBroadcastTemplateThumbnail(scheduleID int64, data []byte, mime string) error {
	if len(data) > scheduler.MaxThumbnailBytes {
		return errors.New("the thumbnail is larger than the 2 MB platform limit")
	}
	res, err := d.sql.Exec(`UPDATE broadcast_templates SET thumbnail=?, thumbnail_mime=?, updated_at=?
		WHERE schedule_id=?`, data, mime, time.Now().Unix(), scheduleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetBroadcastTemplate loads the template for one schedule, without its thumbnail bytes.
func (d *DB) GetBroadcastTemplate(scheduleID int64) (*scheduler.BroadcastTemplate, error) {
	t, err := scanTemplate(d.sql.QueryRow(templateByIDQuery, scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ListBroadcastTemplates returns every template, without thumbnail bytes.
func (d *DB) ListBroadcastTemplates() ([]scheduler.BroadcastTemplate, error) {
	rows, err := d.sql.Query(templateListQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduler.BroadcastTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// RecordBroadcastTemplateError keeps the redacted reason a look-ahead create was refused, on the template
// row, so the screen can show it. A template that does not exist is ErrNotFound: the failure has no home
// without its template, and the log still carries it.
func (d *DB) RecordBroadcastTemplateError(scheduleID int64, message string, at time.Time) error {
	res, err := d.sql.Exec(`UPDATE broadcast_templates SET last_error=?, last_error_at=? WHERE schedule_id=?`,
		message, at.Unix(), scheduleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearBroadcastTemplateError drops the recorded refusal after a create succeeds. Clearing a template
// that has no refusal is not an error.
func (d *DB) ClearBroadcastTemplateError(scheduleID int64) error {
	_, err := d.sql.Exec(`UPDATE broadcast_templates SET last_error='', last_error_at=0 WHERE schedule_id=?
		AND last_error <> ''`, scheduleID)
	return err
}

// BroadcastThumbnail returns the stored picture bytes for a template, for the one write that sends
// them to a platform. Empty when none is stored.
func (d *DB) BroadcastThumbnail(scheduleID int64) ([]byte, string, error) {
	var data []byte
	var mime string
	err := d.sql.QueryRow(`SELECT thumbnail, thumbnail_mime FROM broadcast_templates WHERE schedule_id = ?`,
		scheduleID).Scan(&data, &mime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return data, mime, err
}

// DeleteBroadcastTemplate removes a template. It does not touch occurrences: the sweep must delete
// the platform events first, so the record of them stays until they are gone.
func (d *DB) DeleteBroadcastTemplate(scheduleID int64) error {
	res, err := d.sql.Exec(`DELETE FROM broadcast_templates WHERE schedule_id = ?`, scheduleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimOccurrence inserts a row for one occurrence before its platform call, and returns its id. A
// second claim for the same schedule, destination and instant fails with ErrOccurrenceClaimed. The
// unique key does the compare-and-set, so the loser of a race creates nothing.
func (d *DB) ClaimOccurrence(o scheduler.Occurrence) (int64, error) {
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO recurring_occurrences
		(schedule_id, destination_id, occurrence_at, state, platform_event_id, title, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT (schedule_id, destination_id, occurrence_at) DO NOTHING`,
		o.ScheduleID, o.DestinationID, o.At.UTC().Unix(), string(scheduler.OccurrencePending),
		o.PlatformEventID, o.Title, now, now)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrOccurrenceClaimed
	}
	return res.LastInsertId()
}

// ResolveOccurrence records the platform event id and title for a claimed occurrence, and moves it
// to upcoming.
func (d *DB) ResolveOccurrence(id int64, platformEventID, title string) error {
	return d.updateOccurrence(id, `state=?, platform_event_id=?, title=?`,
		string(scheduler.OccurrenceUpcoming), platformEventID, title)
}

// StrandOccurrence records a platform event that exists without the operator's title, and whose cancel
// was refused. The id is stored so the sweep can retry the cancel; the title stays empty, which is the
// truth about the event. Used only by createTemplateEvent's failure path.
func (d *DB) StrandOccurrence(id int64, platformEventID string) error {
	return d.updateOccurrence(id, `state=?, platform_event_id=?, title=?`,
		string(scheduler.OccurrenceStranded), platformEventID, "")
}

// SetOccurrenceState moves an occurrence to a new state.
func (d *DB) SetOccurrenceState(id int64, state scheduler.OccurrenceState) error {
	return d.updateOccurrence(id, `state=?`, string(state))
}

// SetOccurrenceTitle records the title written to an occurrence's platform event.
func (d *DB) SetOccurrenceTitle(id int64, title string) error {
	return d.updateOccurrence(id, `title=?`, title)
}

func (d *DB) updateOccurrence(id int64, set string, args ...any) error {
	args = append(args, time.Now().Unix(), id)
	res, err := d.sql.Exec(`UPDATE recurring_occurrences SET `+set+`, updated_at=? WHERE id=?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOccurrence removes a row. A released claim uses it; so does the sweep once the platform
// event for a removed occurrence is confirmed gone.
func (d *DB) DeleteOccurrence(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM recurring_occurrences WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetOccurrence loads one occurrence by id.
func (d *DB) GetOccurrence(id int64) (*scheduler.Occurrence, error) {
	o, err := scanOccurrence(d.sql.QueryRow(occurrenceByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// OccurrenceFor loads the row for one schedule, destination and instant.
func (d *DB) OccurrenceFor(scheduleID, destinationID int64, at time.Time) (*scheduler.Occurrence, error) {
	o, err := scanOccurrence(d.sql.QueryRow(occurrenceByKeyQuery, scheduleID, destinationID, at.UTC().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// ListOccurrences returns the calendar window [from, to): upcoming and skipped occurrences, in time
// order. A pending claim is not shown, because it has no event yet.
func (d *DB) ListOccurrences(from, to time.Time) ([]scheduler.Occurrence, error) {
	return d.queryOccurrences(occurrenceWindowQuery, from.UTC().Unix(), to.UTC().Unix())
}

// ListOpenOccurrences returns every claim and every upcoming event, whatever its time. The sweep uses
// it to find events for schedules or templates that no longer exist.
func (d *DB) ListOpenOccurrences() ([]scheduler.Occurrence, error) {
	return d.queryOccurrences(occurrenceOpenQuery)
}

func (d *DB) queryOccurrences(query string, args ...any) ([]scheduler.Occurrence, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduler.Occurrence{}
	for rows.Next() {
		o, err := scanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func scanTemplate(s interface{ Scan(...any) error }) (*scheduler.BroadcastTemplate, error) {
	var (
		t            scheduler.BroadcastTemplate
		hasThumb     bool
		lastAt       int64
		created, upd int64
	)
	if err := s.Scan(&t.ScheduleID, &t.TitlePattern, &t.Description, &t.Privacy, &t.Category,
		&t.Lookahead, &hasThumb, &t.ThumbnailMIME, &t.LastError, &lastAt, &created, &upd); err != nil {
		return nil, err
	}
	t.HasThumbnail = hasThumb
	if lastAt != 0 {
		t.LastErrorAt = time.Unix(lastAt, 0).UTC()
	}
	return &t, nil
}

func scanOccurrence(s interface{ Scan(...any) error }) (*scheduler.Occurrence, error) {
	var (
		o            scheduler.Occurrence
		at           int64
		state        string
		created, upd int64
	)
	if err := s.Scan(&o.ID, &o.ScheduleID, &o.DestinationID, &at, &state,
		&o.PlatformEventID, &o.Title, &created, &upd); err != nil {
		return nil, err
	}
	o.At = time.Unix(at, 0).UTC()
	o.State = scheduler.OccurrenceState(state)
	o.CreatedAt = time.Unix(created, 0).UTC()
	o.UpdatedAt = time.Unix(upd, 0).UTC()
	return &o, nil
}

// ClaimGoLive records that the go-live title for one show was attempted on one destination. It returns
// false when that show was already claimed, which is the signal that the write has been made and must
// not be made again. The unique key does the compare-and-set, as ClaimOccurrence does.
func (d *DB) ClaimGoLive(destinationID, scheduleID int64, at time.Time) (bool, error) {
	res, err := d.sql.Exec(`INSERT INTO recurring_go_live (destination_id, schedule_id, occurrence_at, created_at)
		VALUES (?,?,?,?) ON CONFLICT (destination_id, schedule_id, occurrence_at) DO NOTHING`,
		destinationID, scheduleID, at.UTC().Unix(), time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PruneGoLiveBefore removes the go-live claims of shows that started before cutoff. A claim is only the
// record that a title was attempted, so once its show is past the retention window it is not needed.
func (d *DB) PruneGoLiveBefore(cutoff time.Time) error {
	_, err := d.sql.Exec(`DELETE FROM recurring_go_live WHERE occurrence_at < ?`, cutoff.UTC().Unix())
	return err
}
