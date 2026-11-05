package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TWO WRITERS FOR THE HANDLERS THAT GO AWAY BETWEEN THEIR READ AND THEIR WRITE.
//
// Refresh stream key reads a destination, asks the platform for a new key (up
// to thirty seconds), and used to write the WHOLE row back afterwards with
// UpdateDestination. The expert save read the row, repaired it and wrote all of
// it back too. UpdateDestination has no version behind it, so anything that
// landed in between was silently undone: a stop came back as enabled and the
// reconcile started it again, a rename reverted, a routing change from the
// editor or from POST /routing/presets/{preset}/apply disappeared.
//
// So each of these writes only the columns its handler decides, the way
// UpdateAnnouncement, UpdateLifecycle and UpdateDestinationMixes do:
//
//   - The row is READ AGAIN inside one transaction, and apply is handed that
//     row, not the caller's snapshot. apply must not touch the store: the pool
//     holds one connection (SetMaxOpenConns(1)) and this transaction has it.
//   - Only the columns listed in the statement are written. Anything else apply
//     sets is discarded.
//   - The row is checked as UpdateDestination checks it before anything is
//     written, and the stored row is read back inside the same transaction, so
//     a commit that succeeded is never reported as a failure.
//
// THE REPAIR COLUMNS ride along on both: account_id, compliance, facebook,
// go_live_title and x_go_live_tweet. They are what the API's
// dropUnsendableSettings may clear, and both handlers run that repair, inside
// apply, on the row as it stands now. Writing them from that row is what keeps
// the repair true (a warning that says "unlinked" over a row that still links
// would be a lie) without writing anything from a stale copy.

// ErrIngestSkipped is what an UpdateDestinationIngest callback returns for a row
// it leaves alone, because the row is no longer one this write may change. Not
// a failure: nothing is written and the caller says what happened.
var ErrIngestSkipped = errors.New("ingest update skipped")

// The statements, assembled from constants like UpdateDestination's, so there
// is nothing left at a call site for a value to reach.
const (
	// The columns dropUnsendableSettings may clear. See above.
	destRepairCols = `account_id=?, compliance=?, facebook=?, go_live_title=?, x_go_live_tweet=?, `

	// What Refresh stream key fetches, and the redundant feed a save that
	// first turns on Facebook's backup ingest adds: the ingest URL, the
	// transport the key is for, the backup endpoint, and the two keys. The key
	// columns are a prefix, decided per half exactly as UpdateDestination
	// decides them (keepsSealedKey).
	destIngestCols             = `kind=?, url=?, backup_url=?, ` + destRepairCols + `updated_at=? WHERE id=?`
	destIngestQuery            = `UPDATE destinations SET ` + destUpdateKeyCols + destIngestCols
	destIngestKeepKeyQuery     = `UPDATE destinations SET ` + destIngestCols
	destIngestKeepBackupQuery  = `UPDATE destinations SET ` + destUpdatePrimaryKeyCols + destIngestCols
	destIngestKeepPrimaryQuery = `UPDATE destinations SET ` + destUpdateBackupKeyCols + destIngestCols

	// The three expert-mode columns. updated_at moves with them because it is
	// what the expert panel reports as "last edited".
	destExpertArgsQuery = `UPDATE destinations SET
		extra_input_args=?, extra_output_args=?, expert_ack_reencode=?, ` + destRepairCols + `updated_at=? WHERE id=?`
)

// UpdateDestinationIngest writes ONLY the ingest columns -- url, kind,
// stream_key, backup_url, backup_stream_key and their sealed forms -- plus the
// repair columns, from the row as it stands inside the transaction.
//
// apply edits those fields on the current row. It returns nil to write,
// ErrIngestSkipped to leave the row alone, or another error to refuse; both
// roll back and are returned as they are. The keys are sealed the way
// UpdateDestination seals them, per half, so a half whose stored key cannot be
// read and that apply left empty keeps its ciphertext.
func (d *DB) UpdateDestinationIngest(id int64, apply func(*Destination) error) (*Destination, error) {
	return d.writeDestinationPart(id, apply, func(tx *sql.Tx, cur *Destination, now int64) error {
		keepPrimary, keepBackup, keyArgs, err := d.sealedKeyArgs(cur)
		if err != nil {
			return err
		}
		var query string
		switch {
		case keepPrimary && keepBackup:
			query = destIngestKeepKeyQuery
		case keepPrimary:
			query = destIngestKeepPrimaryQuery
		case keepBackup:
			query = destIngestKeepBackupQuery
		default:
			query = destIngestQuery
		}
		repair, err := repairColumnArgs(cur)
		if err != nil {
			return err
		}
		args := append(keyArgs, cur.Kind, cur.URL, cur.BackupURL)
		args = append(args, repair...)
		return execOneDestination(tx, query, append(args, now, cur.ID)...)
	})
}

// UpdateDestinationExpertArgs writes ONLY the three expert-mode columns --
// extra_input_args, extra_output_args, expert_ack_reencode -- plus the repair
// columns, from the row as it stands inside the transaction. apply returns nil
// to write or an error to refuse, which rolls back.
func (d *DB) UpdateDestinationExpertArgs(id int64, apply func(*Destination) error) (*Destination, error) {
	return d.writeDestinationPart(id, apply, func(tx *sql.Tx, cur *Destination, now int64) error {
		repair, err := repairColumnArgs(cur)
		if err != nil {
			return err
		}
		args := append([]any{cur.ExtraInputArgs, cur.ExtraOutputArgs, cur.ExpertAckReencode}, repair...)
		return execOneDestination(tx, destExpertArgsQuery, append(args, now, cur.ID)...)
	})
}

// writeDestinationPart is the transaction both writers share: read the row
// again, let apply edit it, check it as UpdateDestination would, write the
// columns write lists, and read the stored row back before committing.
func (d *DB) writeDestinationPart(id int64, apply func(*Destination) error,
	write func(tx *sql.Tx, cur *Destination, now int64) error) (*Destination, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := d.scanDestination(tx.QueryRow(destByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := apply(cur); err != nil {
		return nil, err
	}

	// UpdateDestination's checks, on the row as it will be stored. Its other
	// refusals -- retyping to a retired platform, the caption go-live edge, a
	// rendition from another programme -- are about platform, enabled and
	// rendition_id, which neither writer touches.
	if cur.AudioBitrate == 0 {
		cur.AudioBitrate = 160
	}
	if cur.Platform == "" {
		cur.Platform = PlatformCustom
	}
	cur.Profile.ApplyDefaults()
	if err := cur.Validate(); err != nil {
		return nil, err
	}
	if cur.Enabled && cur.Platform.RetiredReason() != "" {
		return nil, retiredError(cur.Platform, "this destination can no longer be started")
	}

	if err := write(tx, cur, time.Now().Unix()); err != nil {
		return nil, err
	}
	// As UpdateDestination does: a destination that does not post captions to
	// YouTube has no use for a signed caption URL. Decided on the CURRENT row,
	// so a URL a key refresh stored for a destination whose captions were
	// switched off during the fetch is forgotten, not kept. In the transaction,
	// because DeleteCaptionIngestURL would wait for the connection this holds.
	if !sendsYouTubeCaptionPOST(cur.Platform, cur.CaptionMode) {
		if _, err := tx.Exec(captionIngestDeleteQuery, id); err != nil {
			return nil, err
		}
	}
	stored, err := d.scanDestination(tx.QueryRow(destByIDQuery, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

// sealedKeyArgs makes UpdateDestination's per-half key decision for a narrow
// write: keepPrimary and keepBackup report which halves keep their stored
// ciphertext (see keepsSealedKey), and args are the sealed values for the
// halves that do not, primary first, in the order destUpdateKeyCols lists them.
func (d *DB) sealedKeyArgs(dst *Destination) (keepPrimary, keepBackup bool, args []any, err error) {
	keepPrimary, keepBackup = keepsSealedPrimaryKey(dst), keepsSealedBackupKey(dst)
	if !keepPrimary {
		enc, plain, err := d.sealStreamKey(dst.StreamKey)
		if err != nil {
			return false, false, nil, fmt.Errorf("seal stream key: %w", err)
		}
		args = append(args, plain, enc)
	}
	if !keepBackup {
		enc, plain, err := d.sealStreamKey(dst.BackupStreamKey)
		if err != nil {
			return false, false, nil, fmt.Errorf("seal backup stream key: %w", err)
		}
		args = append(args, plain, enc)
	}
	return keepPrimary, keepBackup, args, nil
}

// repairColumnArgs are the values for destRepairCols, in its order.
func repairColumnArgs(dst *Destination) ([]any, error) {
	compliance, err := json.Marshal(dst.Compliance)
	if err != nil {
		return nil, err
	}
	facebook, err := json.Marshal(dst.Facebook)
	if err != nil {
		return nil, err
	}
	return []any{dst.AccountID, string(compliance), string(facebook), dst.GoLiveTitle, dst.XGoLiveTweet}, nil
}

// execOneDestination runs one UPDATE and reports a row that is gone as
// ErrNotFound.
func execOneDestination(tx *sql.Tx, query string, args ...any) error {
	res, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
