package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPollNotFound is returned for an id that names no poll.
var ErrPollNotFound = fmt.Errorf("poll: %w", ErrNotFound)

// Poll is one stored chat poll (#14). Result is the poll's JSON as the API
// serves it, written by internal/polls; this package stores it and does not
// read it. It holds counts only: see the polls table in schema.sql.
type Poll struct {
	ID        int64
	Question  string
	Options   []string
	DurationS int
	State     string
	StartedAt time.Time
	EndsAt    time.Time
	// EndedAt is zero while the poll is open.
	EndedAt time.Time
	Result  json.RawMessage
}

// PollQuery bounds a history read. Zero values are unbounded; Limit 0 is the
// default of 100 and anything above 1000 is 1000.
type PollQuery struct {
	Since time.Time
	Until time.Time
	Limit int
}

func pollResult(r json.RawMessage) string {
	if len(r) == 0 || !json.Valid(r) {
		return "{}"
	}
	return string(r)
}

// CreatePoll stores a poll as it opens and returns its id.
func (d *DB) CreatePoll(p Poll) (int64, error) {
	opts, err := json.Marshal(p.Options)
	if err != nil {
		return 0, err
	}
	res, err := d.sql.Exec(
		`INSERT INTO polls (question, options, duration_s, state, started_at, ends_at, ended_at, result)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Question, string(opts), p.DurationS, p.State, p.StartedAt.Unix(), p.EndsAt.Unix(),
		unixOrZero(p.EndedAt), pollResult(p.Result))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePoll writes a poll's state, end time and result. The question,
// options and times it opened with are not rewritten.
func (d *DB) UpdatePoll(p Poll) error {
	res, err := d.sql.Exec(`UPDATE polls SET state = ?, ended_at = ?, result = ? WHERE id = ?`,
		p.State, unixOrZero(p.EndedAt), pollResult(p.Result), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPollNotFound
	}
	return nil
}

// InterruptOpenPolls marks every poll still open as interrupted. Run once at
// startup: an open row can only be one the previous process was counting when
// it stopped, and the votes it held went with it.
func (d *DB) InterruptOpenPolls(now time.Time) (int64, error) {
	res, err := d.sql.Exec(`UPDATE polls SET state = 'interrupted', ended_at = ? WHERE state = 'open'`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const pollColumns = `id, question, options, duration_s, state, started_at, ends_at, ended_at, result`

func scanPoll(row interface{ Scan(...any) error }) (*Poll, error) {
	var (
		p                      Poll
		opts, result           string
		started, ends, endedAt int64
	)
	if err := row.Scan(&p.ID, &p.Question, &opts, &p.DurationS, &p.State, &started, &ends, &endedAt, &result); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPollNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(opts), &p.Options); err != nil || p.Options == nil {
		p.Options = []string{}
	}
	p.StartedAt = time.Unix(started, 0)
	p.EndsAt = time.Unix(ends, 0)
	if endedAt > 0 {
		p.EndedAt = time.Unix(endedAt, 0)
	}
	p.Result = json.RawMessage(pollResult(json.RawMessage(result)))
	return &p, nil
}

// Poll returns one poll.
func (d *DB) Poll(id int64) (*Poll, error) {
	return scanPoll(d.sql.QueryRow(`SELECT `+pollColumns+` FROM polls WHERE id = ?`, id))
}

// Polls returns stored polls, newest first.
func (d *DB) Polls(q PollQuery) ([]Poll, error) {
	var (
		where []string
		args  []any
	)
	if !q.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, q.Since.Unix())
	}
	if !q.Until.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, q.Until.Unix())
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT ` + pollColumns + ` FROM polls`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Poll{}
	for rows.Next() {
		p, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
