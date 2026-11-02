package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
)

// Storage for cross-platform analytics (Wave 4 #4): one row per on-air
// stream, the viewer samples taken during it, and the chat tallied as it came
// in. The math over these rows lives in internal/analytics; this file only
// stores and reads.
//
// CHAT IS TALLIED, NOT JOINED. chat_messages is a replay buffer that
// PurgeChatMessages bounds, so a report computed from it at read time would
// lose a stream's chat as soon as the buffer rolled. Every tick counts the
// rows that arrived since the previous tick -- by chat_messages.id, which is
// assigned at insert, so a batch the hub flushed late is still counted once --
// into analytics_chatters, which lives as long as the stream does.
//
// A NULL viewers column is "not reported". Never 0: see analytics.Total.

// MigrateWave4Analytics creates the analytics tables. CREATE TABLE IF NOT
// EXISTS on every boot, and the schema version does not move: an older binary
// never reads these tables.
func (d *DB) MigrateWave4Analytics() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS analytics_streams (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at  INTEGER NOT NULL,               -- unix milliseconds
	ended_at    INTEGER NOT NULL DEFAULT 0,     -- 0 while the stream is open
	last_at     INTEGER NOT NULL DEFAULT 0,     -- the last tick
	title       TEXT    NOT NULL DEFAULT '',
	chat_cursor INTEGER NOT NULL DEFAULT 0,     -- chat_messages.id already tallied
	qoe_start   TEXT    NOT NULL DEFAULT '',    -- the player's counters at the start, JSON; '' none
	qoe_last    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_analytics_streams_started ON analytics_streams(started_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS analytics_samples (
	stream_id INTEGER NOT NULL,
	at        INTEGER NOT NULL,                 -- the tick, unix milliseconds
	platform  TEXT    NOT NULL,
	account   TEXT    NOT NULL DEFAULT '',
	name      TEXT    NOT NULL DEFAULT '',
	viewers   INTEGER,                          -- NULL: the platform did not report one
	reason    TEXT    NOT NULL DEFAULT '',
	FOREIGN KEY (stream_id) REFERENCES analytics_streams(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_analytics_samples_stream ON analytics_samples(stream_id, at);
CREATE TABLE IF NOT EXISTS analytics_chatters (
	stream_id   INTEGER NOT NULL,
	platform    TEXT    NOT NULL,
	author_id   TEXT    NOT NULL,
	author_name TEXT    NOT NULL DEFAULT '',
	messages    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (stream_id, platform, author_id),
	FOREIGN KEY (stream_id) REFERENCES analytics_streams(id) ON DELETE CASCADE
);`)
	return err
}

// The statements, as whole constants.
const (
	analyticsOpenQuery = `INSERT INTO analytics_streams (started_at, last_at, chat_cursor, qoe_start, qoe_last)
		VALUES (?, ?, (SELECT COALESCE(MAX(id), 0) FROM chat_messages), ?, ?)`
	analyticsOpenStateQuery = `SELECT id, started_at, last_at FROM analytics_streams WHERE ended_at = 0
		ORDER BY id DESC LIMIT 1`
	analyticsSampleInsert = `INSERT INTO analytics_samples (stream_id, at, platform, account, name, viewers, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	analyticsTickUpdate = `UPDATE analytics_streams SET last_at = ?,
		title = CASE WHEN title = '' THEN ? ELSE title END, qoe_last = ? WHERE id = ?`
	analyticsCursorQuery  = `SELECT chat_cursor FROM analytics_streams WHERE id = ?`
	analyticsChatMaxQuery = `SELECT COALESCE(MAX(id), 0) FROM chat_messages`
	// echo = 0: what polyemesis itself sent is not audience chat.
	analyticsChatTally = `INSERT INTO analytics_chatters (stream_id, platform, author_id, author_name, messages)
		SELECT ?, platform, author_id, MAX(author_name), COUNT(*) FROM chat_messages
		WHERE id > ? AND id <= ? AND echo = 0 GROUP BY platform, author_id
		ON CONFLICT (stream_id, platform, author_id) DO UPDATE SET
			messages = analytics_chatters.messages + excluded.messages,
			author_name = CASE WHEN excluded.author_name <> '' THEN excluded.author_name
				ELSE analytics_chatters.author_name END`
	analyticsCursorUpdate = `UPDATE analytics_streams SET chat_cursor = ? WHERE id = ?`
	analyticsCloseUpdate  = `UPDATE analytics_streams SET ended_at = ?,
		last_at = CASE WHEN last_at > ? THEN last_at ELSE ? END WHERE id = ?`
	analyticsSamplesQuery = `SELECT at, platform, account, name, viewers FROM analytics_samples
		WHERE stream_id = ? ORDER BY at, rowid`
	analyticsPrune       = `DELETE FROM analytics_streams WHERE ended_at > 0 AND ended_at < ?`
	analyticsForgetQoE   = `UPDATE analytics_streams SET qoe_start = '' WHERE id = ?`
	analyticsStreamQuery = `SELECT id, started_at, ended_at, last_at, title, qoe_start, qoe_last
		FROM analytics_streams WHERE id = ?`
	analyticsStreamsQuery = `SELECT id, started_at, ended_at, last_at, title, qoe_start, qoe_last
		FROM analytics_streams ORDER BY started_at DESC, id DESC LIMIT ?`
	analyticsChatByPlatform = `SELECT platform, SUM(messages) FROM analytics_chatters WHERE stream_id = ?
		GROUP BY platform ORDER BY platform`
	analyticsTopChatters = `SELECT platform, author_name, messages FROM analytics_chatters WHERE stream_id = ?
		ORDER BY messages DESC, platform, author_name LIMIT ?`
)

// AnalyticsStreamRow is one stored stream, with the player's QoE counters.
type AnalyticsStreamRow struct {
	Stream   analytics.Stream
	QoEStart *analytics.PlayerQoE
	QoELast  *analytics.PlayerQoE
}

func qoeJSON(q *analytics.PlayerQoE) (string, error) {
	if q == nil {
		return "", nil
	}
	b, err := json.Marshal(q)
	return string(b), err
}

func qoeParse(s string) *analytics.PlayerQoE {
	if s == "" {
		return nil
	}
	var q analytics.PlayerQoE
	if json.Unmarshal([]byte(s), &q) != nil {
		return nil
	}
	return &q
}

// OpenAnalyticsStream starts a stream record. Chat that arrived before this
// moment is not the stream's: the cursor starts at the newest message.
func (d *DB) OpenAnalyticsStream(at time.Time, qoe *analytics.PlayerQoE) (int64, error) {
	q, err := qoeJSON(qoe)
	if err != nil {
		return 0, err
	}
	ms := at.UnixMilli()
	res, err := d.sql.Exec(analyticsOpenQuery, ms, ms, q, q)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// OpenAnalyticsStreamState is the stream a previous process left open.
func (d *DB) OpenAnalyticsStreamState() (analytics.OpenState, bool, error) {
	var id, start, last int64
	err := d.sql.QueryRow(analyticsOpenStateQuery).Scan(&id, &start, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return analytics.OpenState{}, false, nil
	}
	if err != nil {
		return analytics.OpenState{}, false, err
	}
	st := analytics.OpenState{ID: id, Start: time.UnixMilli(start)}
	if last > 0 {
		st.LastAt = time.UnixMilli(last)
	}
	return st, true, nil
}

// RecordAnalyticsTick stores one tick and tallies the chat since the last.
func (d *DB) RecordAnalyticsTick(id int64, at time.Time, rs []analytics.Reading, title string, qoe *analytics.PlayerQoE) error {
	q, err := qoeJSON(qoe)
	if err != nil {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ms := at.UnixMilli()
	for _, r := range rs {
		var v any
		if r.Viewers != nil {
			v = *r.Viewers
		}
		if _, err := tx.Exec(analyticsSampleInsert, id, ms, r.Platform, r.Account, r.Name, v, r.Reason); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(analyticsTickUpdate, ms, title, q, id); err != nil {
		return err
	}
	if err := tallyAnalyticsChat(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func tallyAnalyticsChat(tx *sql.Tx, id int64) error {
	var cursor, hi int64
	if err := tx.QueryRow(analyticsCursorQuery, id).Scan(&cursor); err != nil {
		return err
	}
	if err := tx.QueryRow(analyticsChatMaxQuery).Scan(&hi); err != nil {
		return err
	}
	if hi <= cursor {
		return nil
	}
	if _, err := tx.Exec(analyticsChatTally, id, cursor, hi); err != nil {
		return err
	}
	_, err := tx.Exec(analyticsCursorUpdate, hi, id)
	return err
}

// CloseAnalyticsStream ends a stream at `at`, after a last chat tally.
func (d *DB) CloseAnalyticsStream(id int64, at time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tallyAnalyticsChat(tx, id); err != nil {
		return err
	}
	ms := at.UnixMilli()
	// ended_at is never 0 for a closed stream, even one closed at the epoch in
	// a test: 0 is the "open" marker.
	if _, err := tx.Exec(analyticsCloseUpdate, max(ms, 1), ms, ms, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ForgetAnalyticsQoE drops a stream's player baseline.
func (d *DB) ForgetAnalyticsQoE(id int64) error {
	_, err := d.sql.Exec(analyticsForgetQoE, id)
	return err
}

// AnalyticsSamples is a stream's samples in time order.
func (d *DB) AnalyticsSamples(id int64) ([]analytics.Sample, error) {
	rows, err := d.sql.Query(analyticsSamplesQuery, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []analytics.Sample{}
	for rows.Next() {
		var (
			at int64
			s  analytics.Sample
			v  sql.NullInt64
		)
		if err := rows.Scan(&at, &s.Platform, &s.Account, &s.Name, &v); err != nil {
			return nil, err
		}
		s.At = time.UnixMilli(at)
		if v.Valid {
			n := int(v.Int64)
			s.Viewers = &n
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PruneAnalytics deletes the streams that ended before cutoff, with their
// samples and chat tallies (foreign-key cascade). An open stream is never
// pruned, however old its start.
func (d *DB) PruneAnalytics(cutoff time.Time) (int, error) {
	res, err := d.sql.Exec(analyticsPrune, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func scanAnalyticsStream(sc interface{ Scan(...any) error }) (AnalyticsStreamRow, error) {
	var (
		id, start, end, last int64
		title, qs, ql        string
	)
	if err := sc.Scan(&id, &start, &end, &last, &title, &qs, &ql); err != nil {
		return AnalyticsStreamRow{}, err
	}
	st := analytics.Stream{ID: id, Title: title, StartedAt: time.UnixMilli(start)}
	stop := last
	if end > 0 {
		e := time.UnixMilli(end)
		st.EndedAt = &e
		stop = end
	}
	if stop > start {
		st.DurationMs = stop - start
	}
	return AnalyticsStreamRow{Stream: st, QoEStart: qoeParse(qs), QoELast: qoeParse(ql)}, nil
}

// GetAnalyticsStream returns one stream, ErrNotFound when there is none.
func (d *DB) GetAnalyticsStream(id int64) (AnalyticsStreamRow, error) {
	r, err := scanAnalyticsStream(d.sql.QueryRow(analyticsStreamQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AnalyticsStreamRow{}, ErrNotFound
	}
	return r, err
}

// ListAnalyticsStreams returns the newest streams first.
func (d *DB) ListAnalyticsStreams(limit int) ([]AnalyticsStreamRow, error) {
	rows, err := d.sql.Query(analyticsStreamsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AnalyticsStreamRow{}
	for rows.Next() {
		r, err := scanAnalyticsStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AnalyticsChat is a stream's chat: messages per platform and the top
// chatters.
func (d *DB) AnalyticsChat(id int64, top int) ([]analytics.ChatCount, []analytics.Chatter, error) {
	counts := []analytics.ChatCount{}
	rows, err := d.sql.Query(analyticsChatByPlatform, id)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var c analytics.ChatCount
		if err := rows.Scan(&c.Platform, &c.Messages); err != nil {
			rows.Close()
			return nil, nil, err
		}
		counts = append(counts, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	chatters := []analytics.Chatter{}
	rows, err = d.sql.Query(analyticsTopChatters, id, top)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c analytics.Chatter
		if err := rows.Scan(&c.Platform, &c.Name, &c.Messages); err != nil {
			return nil, nil, err
		}
		chatters = append(chatters, c)
	}
	return counts, chatters, rows.Err()
}
