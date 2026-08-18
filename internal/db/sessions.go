package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sessions: the library's unit, and editable metadata for the things in it.
//
// A broadcast is not a file. With hour-long segments a four-hour show is four
// recordings rows today, four unrelated entries in a list, and no way to say
// "that was the good stream". A session groups the segments that chain into
// one another, carries the title/description/tags a human writes, and is what
// the library lists; the recordings become its detail.
//
// The grouping is inferred rather than recorded because the recorder does not
// know when a broadcast ends — it only knows when a segment does. Inference
// also means existing installs get their history grouped on first run instead
// of starting from nothing.

// Session is one broadcast: a run of recordings plus its metadata.
type Session struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`

	// The span is derived from the members. RecalcSession is the only writer.
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt"`
	DurationMS int64     `json:"durationMs"`
	Bytes      int64     `json:"bytes"`
	Recordings int       `json:"recordings"`

	// Auto is false once a human has built or split this session by hand. The
	// backfill will extend an automatic session and will never rewrite a
	// manual one: a curated grouping is a decision, not a guess.
	Auto bool `json:"auto"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DisplayTitle is Title, or a readable stand-in when the user has not named
// the session yet. The fallback lives here rather than in the database so an
// untitled session picks up a better default when one is written, instead of
// being stuck with whatever was generated the day it was created.
func (s Session) DisplayTitle() string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if s.StartedAt.IsZero() {
		return "Untitled session"
	}
	return "Session " + s.StartedAt.Format("2006-01-02 15:04")
}

// Metadata is the editable half of a session or a recording. It is a separate
// type so an update cannot accidentally carry a stale computed span with it.
type Metadata struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// RecordingMeta is the editable metadata for a single recording.
//
// Stored in a sidecar table rather than as columns on recordings, because
// schema.sql runs against databases created before this existed where CREATE
// TABLE IF NOT EXISTS is a no-op and added columns would silently not appear.
// An absent row means "no metadata", which is not an error.
type RecordingMeta struct {
	RecordingID int64     `json:"recordingId"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const sessionColumns = `id, title, description, tags, started_at, ended_at,
	duration_ms, bytes, recordings, auto, created_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	sessionListQuery = `SELECT ` + sessionColumns + ` FROM sessions
		ORDER BY started_at DESC, id DESC`
	sessionByIDQuery = `SELECT ` + sessionColumns + ` FROM sessions WHERE id = ?`
)

func scanSession(s interface{ Scan(...any) error }) (*Session, error) {
	var (
		out                        Session
		tagsJSON                   string
		started, ended             int64
		auto                       int
		createdUnix, updatedUnix   int64
		durationMS, bytes, records int64
	)
	if err := s.Scan(&out.ID, &out.Title, &out.Description, &tagsJSON, &started, &ended,
		&durationMS, &bytes, &records, &auto, &createdUnix, &updatedUnix); err != nil {
		return nil, err
	}
	out.Tags = unmarshalTags(tagsJSON)
	if started > 0 {
		out.StartedAt = time.Unix(started, 0)
	}
	if ended > 0 {
		out.EndedAt = time.Unix(ended, 0)
	}
	out.DurationMS = durationMS
	out.Bytes = bytes
	out.Recordings = int(records)
	out.Auto = auto != 0
	out.CreatedAt = time.Unix(createdUnix, 0)
	out.UpdatedAt = time.Unix(updatedUnix, 0)
	return &out, nil
}

// ListSessions returns sessions newest first, which is how the library reads.
func (d *DB) ListSessions() ([]Session, error) {
	rows, err := d.sql.Query(sessionListQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListSessionsByTag narrows the library to one tag, case-insensitively.
func (d *DB) ListSessionsByTag(tag string) ([]Session, error) {
	want := normalizeTag(tag)
	if want == "" {
		return []Session{}, nil
	}
	all, err := d.ListSessions()
	if err != nil {
		return nil, err
	}
	// Filtered in Go rather than with a LIKE over the JSON column: LIKE
	// '%rock%' matches "rockabilly" too, and a tag filter that quietly returns
	// the wrong sessions is worse than one extra pass over a list that is
	// hundreds of rows long at most.
	out := []Session{}
	for _, s := range all {
		for _, t := range s.Tags {
			if normalizeTag(t) == want {
				out = append(out, s)
				break
			}
		}
	}
	return out, nil
}

// SessionTags is every tag in use, for an autocomplete.
func (d *DB) SessionTags() ([]string, error) {
	all, err := d.ListSessions()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, s := range all {
		for _, t := range s.Tags {
			k := normalizeTag(t)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return normalizeTag(out[i]) < normalizeTag(out[j]) })
	return out, nil
}

// GetSession loads one session.
func (d *DB) GetSession(id int64) (*Session, error) {
	s, err := scanSession(d.sql.QueryRow(sessionByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// CreateSession makes an empty session. Pass auto=false for one a human asked
// for; the backfill will then leave it alone.
func (d *DB) CreateSession(m Metadata, auto bool) (*Session, error) {
	now := time.Now().Unix()
	tags, err := marshalTags(m.Tags)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`INSERT INTO sessions
		(title, description, tags, started_at, ended_at, duration_ms, bytes, recordings, auto, created_at, updated_at)
		VALUES (?,?,?,0,0,0,0,0,?,?,?)`,
		strings.TrimSpace(m.Title), m.Description, tags, boolToInt(auto), now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetSession(id)
}

// UpdateSessionMeta edits the human half and nothing else. The computed span
// is not settable through this path on purpose: it is derived, and a caller
// that could set it would eventually set it wrong.
//
// Editing a session marks it manual, so the grouper stops adjusting a
// broadcast the user has already taken ownership of.
func (d *DB) UpdateSessionMeta(id int64, m Metadata) (*Session, error) {
	tags, err := marshalTags(m.Tags)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE sessions SET title=?, description=?, tags=?, auto=0, updated_at=? WHERE id=?`,
		strings.TrimSpace(m.Title), m.Description, tags, time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetSession(id)
}

// DeleteSession removes the grouping. The recordings themselves are untouched
// — deleting a label must never delete hours of footage — and they become
// ungrouped, so a later backfill can pick them up again.
func (d *DB) DeleteSession(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionRecordings returns the members in the order they were broadcast.
func (d *DB) SessionRecordings(sessionID int64) ([]Recording, error) {
	rows, err := d.sql.Query(`SELECT r.id, r.filename, r.started_at, r.finished_at, r.bytes, r.duration_ms, r.tracks
		FROM session_recordings sr JOIN recordings r ON r.id = sr.recording_id
		WHERE sr.session_id = ? ORDER BY sr.position, r.started_at, r.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecordingRows(rows)
}

// UngroupedRecordings are the recordings no session claims. Newest first.
func (d *DB) UngroupedRecordings() ([]Recording, error) {
	rows, err := d.sql.Query(`SELECT r.id, r.filename, r.started_at, r.finished_at, r.bytes, r.duration_ms, r.tracks
		FROM recordings r LEFT JOIN session_recordings sr ON sr.recording_id = r.id
		WHERE sr.session_id IS NULL ORDER BY r.started_at DESC, r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecordingRows(rows)
}

func scanRecordingRows(rows *sql.Rows) ([]Recording, error) {
	out := []Recording{}
	for rows.Next() {
		var (
			r                 Recording
			started, finished int64
		)
		if err := rows.Scan(&r.ID, &r.Filename, &started, &finished, &r.Bytes, &r.DurationMS, &r.Tracks); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(started, 0)
		if finished > 0 {
			r.FinishedAt = time.Unix(finished, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionForRecording is the session a recording belongs to, or ErrNotFound.
func (d *DB) SessionForRecording(recordingID int64) (*Session, error) {
	var id int64
	err := d.sql.QueryRow(`SELECT session_id FROM session_recordings WHERE recording_id = ?`, recordingID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return d.GetSession(id)
}

// SessionIDsForRecordings maps recording ids to their session, for a listing
// that needs the grouping without a query per row.
func (d *DB) SessionIDsForRecordings(ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.sql.Query(`SELECT recording_id, session_id FROM session_recordings
		WHERE recording_id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid, sid int64
		if err := rows.Scan(&rid, &sid); err != nil {
			return nil, err
		}
		out[rid] = sid
	}
	return out, rows.Err()
}

// SetSessionRecordings replaces the membership wholesale and recomputes the
// span. Members claimed by another session are moved, since a recording
// belongs to exactly one broadcast.
func (d *DB) SetSessionRecordings(sessionID int64, recordingIDs []int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireSession(tx, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM session_recordings WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	// Ordered by broadcast time, not by the order the caller listed them: the
	// position column exists to make playback order obvious, and the caller
	// does not necessarily know it.
	ordered, err := orderByStart(tx, recordingIDs)
	if err != nil {
		return err
	}
	// Noted BEFORE the upsert, because the upsert is what overwrites session_id.
	// AddRecordingToSession already does this for the one-recording case -- "the
	// session it came from is now shorter and its span is stale" -- and the bulk
	// path simply never did. Same steal, same stale span, one path fixed.
	donors := map[int64]bool{}
	for i, id := range ordered {
		var prev int64
		err := tx.QueryRow(`SELECT session_id FROM session_recordings WHERE recording_id = ?`, id).Scan(&prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if prev != 0 && prev != sessionID {
			donors[prev] = true
		}
		if _, err := tx.Exec(`INSERT INTO session_recordings (recording_id, session_id, position)
			VALUES (?,?,?) ON CONFLICT(recording_id) DO UPDATE SET session_id=excluded.session_id, position=excluded.position`,
			id, sessionID, i); err != nil {
			return err
		}
	}
	if err := recalcSessionTx(tx, sessionID); err != nil {
		return err
	}
	for prev := range donors {
		if err := recalcSessionTx(tx, prev); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddRecordingToSession moves one recording into a session.
func (d *DB) AddRecordingToSession(sessionID, recordingID int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireSession(tx, sessionID); err != nil {
		return err
	}
	var prev int64
	err = tx.QueryRow(`SELECT session_id FROM session_recordings WHERE recording_id = ?`, recordingID).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO session_recordings (recording_id, session_id, position)
		VALUES (?,?,0) ON CONFLICT(recording_id) DO UPDATE SET session_id=excluded.session_id`,
		recordingID, sessionID); err != nil {
		return err
	}
	if err := renumberSession(tx, sessionID); err != nil {
		return err
	}
	if err := recalcSessionTx(tx, sessionID); err != nil {
		return err
	}
	// The session it came from is now shorter and its span is stale.
	if prev != 0 && prev != sessionID {
		if err := recalcSessionTx(tx, prev); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RemoveRecordingFromSession ungroups one recording.
func (d *DB) RemoveRecordingFromSession(recordingID int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sessionID int64
	err = tx.QueryRow(`SELECT session_id FROM session_recordings WHERE recording_id = ?`, recordingID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM session_recordings WHERE recording_id = ?`, recordingID); err != nil {
		return err
	}
	if err := renumberSession(tx, sessionID); err != nil {
		return err
	}
	if err := recalcSessionTx(tx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecalcSession recomputes one session's span from its members. Call it after
// a member's duration or size is measured.
func (d *DB) RecalcSession(sessionID int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recalcSessionTx(tx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecalcSessions recomputes every session, which is what the recording scanner
// wants after a sweep changed durations underneath it.
func (d *DB) RecalcSessions() error {
	rows, err := d.sql.Query(`SELECT id FROM sessions`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := d.RecalcSession(id); err != nil {
			return err
		}
	}
	return nil
}

type execQuerier interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func requireSession(tx execQuerier, id int64) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func orderByStart(tx execQuerier, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT id FROM recordings WHERE id IN (`+placeholders(len(ids))+`)
		ORDER BY started_at, id`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func renumberSession(tx execQuerier, sessionID int64) error {
	ordered, err := sessionMemberIDs(tx, sessionID)
	if err != nil {
		return err
	}
	for i, id := range ordered {
		if _, err := tx.Exec(`UPDATE session_recordings SET position = ? WHERE recording_id = ?`, i, id); err != nil {
			return err
		}
	}
	return nil
}

func sessionMemberIDs(tx execQuerier, sessionID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT sr.recording_id FROM session_recordings sr
		JOIN recordings r ON r.id = sr.recording_id
		WHERE sr.session_id = ? ORDER BY r.started_at, r.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// recalcSessionTx derives the span. ended_at is the largest of every member's
// own end, not the last member's, because segments can be indexed out of order
// and a still-open final segment reports no finish time at all.
func recalcSessionTx(tx execQuerier, sessionID int64) error {
	rows, err := tx.Query(`SELECT r.started_at, r.finished_at, r.bytes, r.duration_ms
		FROM session_recordings sr JOIN recordings r ON r.id = sr.recording_id
		WHERE sr.session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	var (
		count            int
		started, ended   int64
		totalBytes, dura int64
	)
	for rows.Next() {
		var s, f, b, dm int64
		if err := rows.Scan(&s, &f, &b, &dm); err != nil {
			rows.Close()
			return err
		}
		count++
		if started == 0 || s < started {
			started = s
		}
		end := f
		if dm > 0 {
			// duration_ms is measured from the file and is the better answer
			// when both exist; finished_at is when the writer closed it, which
			// can be minutes later if the process was stopped.
			if e := s + dm/1000; e > end {
				end = e
			}
		}
		if end > ended {
			ended = end
		}
		totalBytes += b
		dura += dm
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE sessions SET started_at=?, ended_at=?, duration_ms=?, bytes=?, recordings=?, updated_at=?
		WHERE id=?`, started, ended, dura, totalBytes, count, time.Now().Unix(), sessionID)
	return err
}

// SessionRules is the grouping heuristic's tuning.
//
// The signal that two recordings are one broadcast is that the second begins
// where the first ended. Segmentation is continuous — FFmpeg closes segment N
// and opens N+1 in the same instant — so the gap is normally milliseconds, and
// anything up to MaxGap is an encoder that stumbled and came back rather than
// a new show.
//
// It deliberately measures the END-to-START gap and not the start-to-start
// delta. A rule phrased as "starts within one segment length" breaks the
// moment the operator changes the segment length mid-broadcast, which is
// exactly the case a heuristic must survive.
type SessionRules struct {
	// MaxGap is the longest silence between one recording ending and the next
	// beginning that still counts as the same broadcast.
	MaxGap time.Duration
	// SegmentHint is the assumed length of a recording whose own duration was
	// never measured. Without it an unmeasured segment has no end, and the
	// pair after it could not be judged at all.
	SegmentHint time.Duration
	// MaxSpan caps how long one inferred session may run. A recorder left
	// running for a week is not one broadcast, and a session with two hundred
	// members is not a useful library entry. Zero means no cap.
	MaxSpan time.Duration
}

// DefaultSessionRules is deliberately generous. Grouping two shows into one is
// something the user can undo in the UI in a second; leaving a four-hour
// broadcast as four unrelated rows is the problem sessions exist to fix, so
// the heuristic errs towards joining.
func DefaultSessionRules() SessionRules {
	return SessionRules{
		MaxGap:      5 * time.Minute,
		SegmentHint: time.Hour, // the default recording segment length
		MaxSpan:     24 * time.Hour,
	}
}

func (r SessionRules) normalized() SessionRules {
	if r.MaxGap <= 0 {
		r.MaxGap = DefaultSessionRules().MaxGap
	}
	if r.SegmentHint < 0 {
		r.SegmentHint = 0
	}
	if r.MaxSpan < 0 {
		r.MaxSpan = 0
	}
	return r
}

// recordingEnd is when a recording stopped, best effort.
func recordingEnd(r Recording, hint time.Duration) time.Time {
	if r.DurationMS > 0 {
		return r.StartedAt.Add(time.Duration(r.DurationMS) * time.Millisecond)
	}
	if !r.FinishedAt.IsZero() && r.FinishedAt.After(r.StartedAt) {
		return r.FinishedAt
	}
	// Nothing measured this one. Assuming a typical segment is a guess, but
	// the alternative — treating it as zero-length — puts a full segment's
	// worth of apparent silence after it and splits a session that never
	// broke. Guessing long fails towards joining, which is the safe direction.
	return r.StartedAt.Add(hint)
}

// Chains reports whether next belongs to the same broadcast as prev.
func (r SessionRules) Chains(prev, next Recording) bool {
	r = r.normalized()
	gap := next.StartedAt.Sub(recordingEnd(prev, r.SegmentHint))
	// A negative gap is an overlap: two writers running at once, or a duration
	// that over-measured. Either way they are the same broadcast.
	if gap < 0 {
		return true
	}
	return gap <= r.MaxGap
}

// GroupRecordings splits recordings into broadcasts. It is pure so the
// heuristic can be tested without a database, and it sorts its input, so the
// caller need not.
func GroupRecordings(recs []Recording, rules SessionRules) [][]Recording {
	rules = rules.normalized()
	if len(recs) == 0 {
		return nil
	}
	sorted := make([]Recording, len(recs))
	copy(sorted, recs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].StartedAt.Equal(sorted[j].StartedAt) {
			return sorted[i].StartedAt.Before(sorted[j].StartedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})

	var (
		out [][]Recording
		cur []Recording
	)
	for i, rec := range sorted {
		if i == 0 {
			cur = []Recording{rec}
			continue
		}
		prev := sorted[i-1]
		join := rules.Chains(prev, rec)
		if join && rules.MaxSpan > 0 && rec.StartedAt.Sub(cur[0].StartedAt) > rules.MaxSpan {
			join = false
		}
		if join {
			cur = append(cur, rec)
			continue
		}
		out = append(out, cur)
		cur = []Recording{rec}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// BackfillResult is what a grouping pass changed.
type BackfillResult struct {
	// Created is new sessions.
	Created int `json:"created"`
	// Assigned is recordings that gained a session.
	Assigned int `json:"assigned"`
	// Extended is existing sessions that absorbed a newly chained recording.
	Extended int `json:"extended"`
	// Groups is how many broadcasts the heuristic saw in total, grouped or not.
	Groups int `json:"groups"`
}

// BackfillSessions infers sessions from the recordings already indexed, so an
// install that predates this feature does not lose its history.
//
// It is idempotent and additive: a recording that already belongs to a session
// is never moved, and two existing sessions are never merged even when the
// heuristic thinks their recordings chain. Both rules exist because the user
// may have split or curated a grouping by hand, and a backfill that undid that
// on every restart would be worse than no backfill at all.
func (d *DB) BackfillSessions(rules SessionRules) (BackfillResult, error) {
	var res BackfillResult

	recs, err := d.ListRecordings()
	if err != nil {
		return res, err
	}
	if len(recs) == 0 {
		return res, nil
	}
	ids := make([]int64, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	owned, err := d.SessionIDsForRecordings(ids)
	if err != nil {
		return res, err
	}

	groups := GroupRecordings(recs, rules)
	res.Groups = len(groups)

	for _, g := range groups {
		if err := d.backfillGroup(g, owned, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// backfillGroup places one inferred broadcast.
func (d *DB) backfillGroup(g []Recording, owned map[int64]int64, res *BackfillResult) error {
	// What does this run already belong to? More than one answer means a human
	// has split it, and the split stands.
	existing := map[int64]bool{}
	missing := make([]int64, 0, len(g))
	for _, r := range g {
		if id := owned[r.ID]; id != 0 {
			existing[id] = true
			continue
		}
		missing = append(missing, r.ID)
	}
	if len(existing) > 1 {
		return d.backfillAmbiguous(g, owned, res)
	}
	if len(missing) == 0 {
		return nil
	}

	var target int64
	for id := range existing {
		target = id
	}
	if target == 0 {
		s, err := d.CreateSession(Metadata{}, true)
		if err != nil {
			return err
		}
		target = s.ID
		res.Created++
	} else {
		res.Extended++
	}
	for _, id := range missing {
		if err := d.AddRecordingToSession(target, id); err != nil {
			return err
		}
		owned[id] = target
		res.Assigned++
	}
	return nil
}

// backfillAmbiguous handles a run that spans two existing sessions: the user
// split this broadcast, so the split is honoured and each ungrouped recording
// simply joins whichever side it is adjacent to.
func (d *DB) backfillAmbiguous(g []Recording, owned map[int64]int64, res *BackfillResult) error {
	for i, r := range g {
		if owned[r.ID] != 0 {
			continue
		}
		var target int64
		for j := i - 1; j >= 0 && target == 0; j-- {
			target = owned[g[j].ID]
		}
		for j := i + 1; j < len(g) && target == 0; j++ {
			target = owned[g[j].ID]
		}
		if target == 0 {
			continue
		}
		if err := d.AddRecordingToSession(target, r.ID); err != nil {
			return err
		}
		owned[r.ID] = target
		res.Assigned++
	}
	return nil
}

// AssignRecording places a newly indexed recording: into the session of the
// recording it chains from, or into a new one. This is the live path the
// recorder calls; BackfillSessions is the same decision applied to history.
//
// A recording that already belongs somewhere is left alone.
func (d *DB) AssignRecording(recordingID int64, rules SessionRules) (*Session, error) {
	rules = rules.normalized()
	rec, err := d.GetRecording(recordingID)
	if err != nil {
		return nil, err
	}
	if s, err := d.SessionForRecording(recordingID); err == nil {
		return s, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	prev, err := d.recordingBefore(rec.StartedAt, rec.ID)
	if err != nil {
		return nil, err
	}
	if prev != nil && rules.Chains(*prev, *rec) {
		if s, err := d.SessionForRecording(prev.ID); err == nil {
			if rules.MaxSpan == 0 || rec.StartedAt.Sub(s.StartedAt) <= rules.MaxSpan {
				if err := d.AddRecordingToSession(s.ID, rec.ID); err != nil {
					return nil, err
				}
				return d.GetSession(s.ID)
			}
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	s, err := d.CreateSession(Metadata{}, true)
	if err != nil {
		return nil, err
	}
	if err := d.AddRecordingToSession(s.ID, rec.ID); err != nil {
		return nil, err
	}
	return d.GetSession(s.ID)
}

// recordingBefore is the recording immediately preceding this one in time.
func (d *DB) recordingBefore(at time.Time, id int64) (*Recording, error) {
	var (
		r                 Recording
		started, finished int64
	)
	err := d.sql.QueryRow(`SELECT id, filename, started_at, finished_at, bytes, duration_ms, tracks
		FROM recordings WHERE (started_at < ? OR (started_at = ? AND id < ?))
		ORDER BY started_at DESC, id DESC LIMIT 1`, at.Unix(), at.Unix(), id).
		Scan(&r.ID, &r.Filename, &started, &finished, &r.Bytes, &r.DurationMS, &r.Tracks)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt = time.Unix(started, 0)
	if finished > 0 {
		r.FinishedAt = time.Unix(finished, 0)
	}
	return &r, nil
}

// PruneEmptySessions drops automatic sessions that lost their last recording.
// Manual ones are kept: an empty session a human made is a placeholder they
// are about to fill, not litter.
func (d *DB) PruneEmptySessions() (int, error) {
	res, err := d.sql.Exec(`DELETE FROM sessions WHERE auto = 1
		AND id NOT IN (SELECT session_id FROM session_recordings)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetRecordingMeta returns a recording's editable metadata. An absent row is
// the zero value and not an error: most recordings never get a title.
func (d *DB) GetRecordingMeta(recordingID int64) (*RecordingMeta, error) {
	var (
		m        RecordingMeta
		tagsJSON string
		updated  int64
	)
	err := d.sql.QueryRow(`SELECT recording_id, title, description, tags, updated_at
		FROM recording_meta WHERE recording_id = ?`, recordingID).
		Scan(&m.RecordingID, &m.Title, &m.Description, &tagsJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return &RecordingMeta{RecordingID: recordingID, Tags: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	m.Tags = unmarshalTags(tagsJSON)
	m.UpdatedAt = time.Unix(updated, 0)
	return &m, nil
}

// SetRecordingMeta writes a recording's editable metadata.
func (d *DB) SetRecordingMeta(recordingID int64, m Metadata) (*RecordingMeta, error) {
	tags, err := marshalTags(m.Tags)
	if err != nil {
		return nil, err
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM recordings WHERE id = ?`, recordingID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	if _, err := d.sql.Exec(`INSERT INTO recording_meta (recording_id, title, description, tags, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(recording_id) DO UPDATE SET
			title=excluded.title, description=excluded.description,
			tags=excluded.tags, updated_at=excluded.updated_at`,
		recordingID, strings.TrimSpace(m.Title), m.Description, tags, time.Now().Unix()); err != nil {
		return nil, err
	}
	return d.GetRecordingMeta(recordingID)
}

// ListRecordingMeta fetches metadata for many recordings at once, so a listing
// does not issue a query per row. Recordings with no metadata are absent from
// the map.
func (d *DB) ListRecordingMeta(ids []int64) (map[int64]RecordingMeta, error) {
	out := map[int64]RecordingMeta{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.sql.Query(`SELECT recording_id, title, description, tags, updated_at
		FROM recording_meta WHERE recording_id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m        RecordingMeta
			tagsJSON string
			updated  int64
		)
		if err := rows.Scan(&m.RecordingID, &m.Title, &m.Description, &tagsJSON, &updated); err != nil {
			return nil, err
		}
		m.Tags = unmarshalTags(tagsJSON)
		m.UpdatedAt = time.Unix(updated, 0)
		out[m.RecordingID] = m
	}
	return out, rows.Err()
}

// NormalizeTags trims, drops empties and removes case-insensitive duplicates
// while keeping the casing the user typed first. Order is preserved: tags read
// as a list the user wrote, not as a set the database sorted.
func NormalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		k := normalizeTag(t)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

func normalizeTag(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func marshalTags(tags []string) (string, error) {
	norm := NormalizeTags(tags)
	if len(norm) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(norm)
	if err != nil {
		return "", fmt.Errorf("encode tags: %w", err)
	}
	return string(b), nil
}

// unmarshalTags never fails. A tags column that will not parse is a display
// problem; refusing to load the session it belongs to would hide the recording
// as well, which is a much larger one.
func unmarshalTags(s string) []string {
	if s == "" || s == "[]" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return NormalizeTags(out)
}
