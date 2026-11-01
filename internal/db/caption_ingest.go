package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
)

// The YouTube caption ingestion URL of a destination (#8), sealed at rest.
//
// WHY A TABLE OF ITS OWN AND NOT A COLUMN ON destinations. Every destination
// read and every save goes through the destinations row: UpdateDestination writes
// each column from the struct the UI sent back, and a URL the UI is never shown
// would arrive empty on every edit and wipe itself. Kept apart, the URL is in no
// destination read, no JSON view, no backup export of the row and no save. It is
// written by the one path that receives it from YouTube (a liveStreams.insert
// that asked for contentDetails, see internal/oauth) and read by the two that
// need it: the next stream pick, and the engine's caption poster.
//
// WHY THE STREAM KEY'S HASH IS STORED BESIDE IT. The URL belongs to one
// liveStream, and the stream is known here only by its key. A destination whose
// key has since been typed over, or rotated by a refresh, publishes to a
// different stream, and posting captions to the old stream's URL would put them
// on a broadcast nobody watches. So a read compares the hash with the current key
// and answers "none" on a mismatch, and the operator is told to refresh.
//
// NO BOX, NO URL. Without a secret box this refuses to store rather than fall
// back to plaintext: the stream key's plaintext fallback exists for one upgrade,
// and a new credential has no upgrade to survive. Production always opens the
// database with a box (cmd/polyemesis/main.go).

// ErrCaptionIngestMissing is the refusal to start a destination that sends
// captions by YouTube's HTTP POST path but holds no caption ingestion URL for its
// current stream key. errors.Is it: the API maps it to 409.
var ErrCaptionIngestMissing = errors.New("caption ingest URL missing")

// captionIngestMissingReason is the sentence the refusal carries. It names the
// button, and says the key moves once, because that is the cost of the fix.
const captionIngestMissingReason = "this destination sends captions to YouTube by HTTP POST, and it has no caption " +
	"ingestion URL for its current stream key. Press Refresh stream key to create this destination's own " +
	"YouTube stream (its stream key changes once, so update your encoder if it publishes directly), or set " +
	"captions to off or embedded"

// streamKeyHash is the fingerprint stored with a caption URL. The empty key has
// no fingerprint, so a destination without a key never matches a stored URL.
func streamKeyHash(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// PutCaptionIngestURL seals and stores a destination's caption ingestion URL,
// bound to the stream key it was issued with. An empty URL deletes the row.
func (d *DB) PutCaptionIngestURL(destinationID int64, streamKey, ingestURL string) error {
	if strings.TrimSpace(ingestURL) == "" {
		return d.DeleteCaptionIngestURL(destinationID)
	}
	if d.box == nil {
		return fmt.Errorf("no secret key is configured, so the caption ingestion URL cannot be stored sealed")
	}
	hash := streamKeyHash(streamKey)
	if hash == "" {
		return fmt.Errorf("a caption ingestion URL is stored with the stream key it belongs to, and this destination has none")
	}
	enc, err := d.box.Seal(ingestURL)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO caption_ingest (destination_id, url_secret_enc, stream_key_hash, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(destination_id) DO UPDATE SET url_secret_enc=excluded.url_secret_enc,
			stream_key_hash=excluded.stream_key_hash, updated_at=excluded.updated_at`,
		destinationID, enc, hash, time.Now().Unix())
	return err
}

// CaptionIngestURL returns the stored URL when it belongs to streamKey, and ""
// when there is none, when it was issued for another key, or when it does not
// open on this machine. Only a database failure is an error.
func (d *DB) CaptionIngestURL(destinationID int64, streamKey string) (string, error) {
	var enc []byte
	var hash string
	err := d.sql.QueryRow(`SELECT url_secret_enc, stream_key_hash FROM caption_ingest WHERE destination_id = ?`,
		destinationID).Scan(&enc, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	want := streamKeyHash(streamKey)
	if want == "" || hash != want || d.box == nil {
		return "", nil
	}
	u, err := d.box.Open(enc)
	if err != nil {
		// Sealed under another key: as absent as no row, and the remedy is the
		// same refresh.
		return "", nil
	}
	return u, nil
}

// HasCaptionIngestURL reports whether a URL for streamKey is stored, without
// returning it.
func (d *DB) HasCaptionIngestURL(destinationID int64, streamKey string) (bool, error) {
	u, err := d.CaptionIngestURL(destinationID, streamKey)
	return u != "", err
}

// DeleteCaptionIngestURL forgets a destination's caption URL.
func (d *DB) DeleteCaptionIngestURL(destinationID int64) error {
	_, err := d.sql.Exec(`DELETE FROM caption_ingest WHERE destination_id = ?`, destinationID)
	return err
}

// sendsYouTubeCaptionPOST reports whether a row sends captions by YouTube's HTTP
// POST path, the only mode that needs a caption URL.
func sendsYouTubeCaptionPOST(platform Platform, mode string) bool {
	return platform == PlatformYouTube && captions.Mode(mode) == captions.ModeYouTubePOST
}

// captionIngestRefusal is the go-live refusal for a row that sends POST captions
// with no URL for its key, or nil when it may start.
func (d *DB) captionIngestRefusal(id int64, platform Platform, mode, streamKey string) error {
	if !sendsYouTubeCaptionPOST(platform, mode) {
		return nil
	}
	ok, err := d.HasCaptionIngestURL(id, streamKey)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrCaptionIngestMissing, captionIngestMissingReason)
	}
	return nil
}
