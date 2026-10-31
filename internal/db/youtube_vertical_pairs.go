package db

import (
	"database/sql"
	"errors"
	"time"
)

// YouTubeVerticalPair links a horizontal YouTube destination to the vertical
// destination that publishes its 9:16 copy (#7).
//
// The two halves are separate destination rows, so each runs in its own FFmpeg
// process. A failure in one does not stop the other. The pair row only records
// which rows belong together and which YouTube liveStream the vertical half uses.
// It never holds a stream key, and nothing binds the vertical stream to the
// horizontal broadcast. See MigrateWave3YouTubeVertical.
type YouTubeVerticalPair struct {
	HorizontalID int64 `json:"horizontalId"`
	VerticalID   int64 `json:"verticalId"`
	// VerticalStreamID is the YouTube liveStream id of the vertical ingest. It is
	// an id, not a key, and is safe to log.
	VerticalStreamID string `json:"verticalStreamId"`
	// PairingConfirmedAt is when the operator confirmed the one-time Live Control
	// Room step. Zero means not yet confirmed.
	PairingConfirmedAt int64 `json:"pairingConfirmedAt,omitempty"`
	CreatedAt          int64 `json:"createdAt"`
}

// ErrNoYouTubeVerticalPair is returned when a destination has no vertical half.
var ErrNoYouTubeVerticalPair = errors.New("this destination has no YouTube vertical pair")

// CreateYouTubeVerticalPair records that verticalID is the vertical half of
// horizontalID. It does not validate that the two destinations exist beyond the
// foreign keys, and it does not check their platform. The API layer does that.
func (d *DB) CreateYouTubeVerticalPair(horizontalID, verticalID int64, verticalStreamID string) error {
	_, err := d.sql.Exec(`INSERT INTO youtube_vertical_pairs
		(horizontal_id, vertical_id, vertical_stream_id, pairing_confirmed_at, created_at)
		VALUES (?, ?, ?, 0, ?)`,
		horizontalID, verticalID, verticalStreamID, time.Now().Unix())
	return err
}

// GetYouTubeVerticalPairByHorizontal returns the pair for a horizontal destination,
// or ErrNoYouTubeVerticalPair.
func (d *DB) GetYouTubeVerticalPairByHorizontal(horizontalID int64) (*YouTubeVerticalPair, error) {
	return scanYouTubeVerticalPair(d.sql.QueryRow(`SELECT horizontal_id, vertical_id, vertical_stream_id,
		pairing_confirmed_at, created_at FROM youtube_vertical_pairs WHERE horizontal_id = ?`, horizontalID))
}

// ListYouTubeVerticalPairs returns every pair. Callers use it to find the vertical
// stream ids to keep out of the first-rtmp fallback.
func (d *DB) ListYouTubeVerticalPairs() ([]YouTubeVerticalPair, error) {
	rows, err := d.sql.Query(`SELECT horizontal_id, vertical_id, vertical_stream_id,
		pairing_confirmed_at, created_at FROM youtube_vertical_pairs ORDER BY horizontal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YouTubeVerticalPair
	for rows.Next() {
		p, err := scanYouTubeVerticalPair(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ConfirmYouTubeVerticalPairing stamps the one-time Live Control Room step as done.
func (d *DB) ConfirmYouTubeVerticalPairing(horizontalID int64, at time.Time) error {
	res, err := d.sql.Exec(`UPDATE youtube_vertical_pairs SET pairing_confirmed_at = ? WHERE horizontal_id = ?`,
		at.Unix(), horizontalID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoYouTubeVerticalPair
	}
	return nil
}

type rowScanner interface{ Scan(...any) error }

func scanYouTubeVerticalPair(s rowScanner) (*YouTubeVerticalPair, error) {
	var p YouTubeVerticalPair
	if err := s.Scan(&p.HorizontalID, &p.VerticalID, &p.VerticalStreamID, &p.PairingConfirmedAt, &p.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoYouTubeVerticalPair
		}
		return nil, err
	}
	return &p, nil
}
