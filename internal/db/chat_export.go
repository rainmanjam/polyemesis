package db

// The chat reads behind the export: a time range, paged.
//
// ONE CONNECTION IS ALL THERE IS (db.go sets SetMaxOpenConns(1)), so a cursor
// that stays open while its rows are written to a slow client would stall the
// chat flush, the recording scan and every settings read behind that download.
// These reads therefore never hold rows across a write: each call fetches one
// bounded page, closes it, and the caller resumes from the last (at_ms, id).

import (
	"database/sql"
	"math"
	"time"
)

// chatExportPage is how many rows one page carries.
const chatExportPage = 1000

// ChatRange selects stored chat for an export. Messages polyemesis sent itself
// (echo) are never part of it: they are the operator's, not the audience's, and
// the highlight scanner leaves them out of the same window for the same reason.
type ChatRange struct {
	// FromMS is inclusive and ToMS exclusive, Unix ms of the stored message time (at_ms) -- the
	// clock the highlight scanner lines chat up with a recording by.
	FromMS, ToMS int64
	// Platform narrows to one platform; empty means all.
	Platform Platform
	// MaxID, when non-zero, hides rows stored after the summary was taken, so
	// the count a header states is the count the stream delivers.
	MaxID int64
}

// ChatRangeSummary is what a range holds, taken before it is streamed.
type ChatRangeSummary struct {
	Count     int
	Platforms []Platform
	// MaxID is the highest row id in the range; pass it back as ChatRange.MaxID.
	MaxID int64
}

const (
	chatRangeSummaryQuery = `SELECT platform, COUNT(*), MAX(id) FROM chat_messages
		WHERE at_ms >= ? AND at_ms < ? AND echo = 0 AND (? = '' OR platform = ?)
		GROUP BY platform ORDER BY platform`

	chatRangePageQuery = `SELECT ` + chatColumns + ` FROM chat_messages
		WHERE at_ms >= ? AND at_ms < ? AND echo = 0 AND id <= ? AND (? = '' OR platform = ?)
		AND (at_ms > ? OR (at_ms = ? AND id > ?))
		ORDER BY at_ms, id LIMIT ?`

	chatOldestQuery = `SELECT MIN(at_ms) FROM chat_messages`
)

// ChatRangeSummary counts a range and names the platforms in it.
func (d *DB) ChatRangeSummary(q ChatRange) (ChatRangeSummary, error) {
	rows, err := d.sql.Query(chatRangeSummaryQuery, q.FromMS, q.ToMS, string(q.Platform), string(q.Platform))
	if err != nil {
		return ChatRangeSummary{}, err
	}
	defer rows.Close()
	out := ChatRangeSummary{Platforms: []Platform{}}
	for rows.Next() {
		var (
			p     Platform
			n     int
			maxID int64
		)
		if err := rows.Scan(&p, &n, &maxID); err != nil {
			return ChatRangeSummary{}, err
		}
		out.Count += n
		out.Platforms = append(out.Platforms, p)
		out.MaxID = max(out.MaxID, maxID)
	}
	return out, rows.Err()
}

// ForEachChatMessage calls fn for every message in the range, oldest first,
// reading a page at a time. It stops at the first error fn returns.
func (d *DB) ForEachChatMessage(q ChatRange, fn func(ChatMessage) error) error {
	maxID := q.MaxID
	if maxID <= 0 {
		maxID = math.MaxInt64
	}
	afterAt, afterID := q.FromMS-1, int64(math.MaxInt64)
	// afterAt starts one ms early with an id no row can exceed, so the first
	// page's keyset condition is "at_ms > FromMS-1", i.e. every row from FromMS.
	for {
		page, err := d.chatRangePage(q, maxID, afterAt, afterID)
		if err != nil {
			return err
		}
		for _, m := range page {
			if err := fn(m); err != nil {
				return err
			}
		}
		if len(page) < chatExportPage {
			return nil
		}
		last := page[len(page)-1]
		afterAt, afterID = last.At.UnixMilli(), last.ID
	}
}

func (d *DB) chatRangePage(q ChatRange, maxID, afterAt, afterID int64) ([]ChatMessage, error) {
	rows, err := d.sql.Query(chatRangePageQuery, q.FromMS, q.ToMS, maxID,
		string(q.Platform), string(q.Platform), afterAt, afterAt, afterID, chatExportPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChatMessage, 0, chatExportPage)
	for rows.Next() {
		m, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ChatOldest is the stored time of the oldest stored message, and false when
// the table is empty.
//
// It is how an export tells a window that was fully retained from one the purge
// reached into: the purge keeps every row newer than its cutoff plus the newest
// few whatever their age, so what survives is always a suffix of time, and a
// window that starts at or after the oldest row has lost nothing to it.
func (d *DB) ChatOldest() (time.Time, bool, error) {
	var ms sql.NullInt64
	if err := d.sql.QueryRow(chatOldestQuery).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if !ms.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(ms.Int64), true, nil
}
