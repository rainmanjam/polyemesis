package db

import (
	"database/sql"
	"errors"
	"time"
)

// ChatSanction is the ban or timeout polyemesis last placed on one viewer in
// one channel (UX Council A48).
//
// It is polyemesis's OWN record, written by chat.Hub when a Ban succeeds and
// removed when an Unban does. No platform here offers a uniform "is this person
// banned" read, so a ban placed from a platform's own dashboard, or by another
// moderator, is not in it. The user card says so in its wording.
//
// Persisted rather than held in memory because the card disables Lift ban when
// this says there is nothing to lift: a memory-only record would read "none"
// for every banned viewer after a restart, and take the one working control
// away from the people it is for.
type ChatSanction struct {
	Platform Platform
	Account  string
	UserID   string
	// Action is chat.ModBan or chat.ModTimeout -- the moderation event's own
	// values, so this table invents no vocabulary of its own.
	Action string
	// Until is a timeout's end. Zero on a permanent ban.
	Until time.Time
	// At is when it was placed.
	At time.Time
	// By is who placed it: chat.ActorOperator or chat.ActorAutomod.
	By string
	// Reason is what the platform was told: the moderator's reason, or
	// automod's closed-set platform reason. Never the model's prose (#495).
	Reason string
}

// MigrateChatSanctions creates the sanction table.
//
// The statement is idempotent, so Open runs it on every boot. It does not move
// currentSchemaVersion: an older binary never reads this table.
func (d *DB) MigrateChatSanctions() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS chat_sanctions (
    platform  TEXT    NOT NULL,
    account   TEXT    NOT NULL DEFAULT '',
    user_id   TEXT    NOT NULL,
    action    TEXT    NOT NULL,             -- ban | timeout
    until_ms  INTEGER NOT NULL DEFAULT 0,   -- 0 = permanent
    at_ms     INTEGER NOT NULL DEFAULT 0,
    by_actor  TEXT    NOT NULL DEFAULT '',
    reason    TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (platform, account, user_id)
)`)
	return err
}

// RecordChatSanction stores s, replacing any earlier sanction on the same
// viewer in the same channel: a timeout escalated to a ban is a ban.
//
// Every write also prunes the timeouts that have already ended. ActiveChatSanction
// filters them out, so they are dead rows, and a raid timing out hundreds of
// accounts would otherwise grow this table without bound. Pruning on write
// bounds it by the sanctions actually in force, with no timer to keep alive.
func (d *DB) RecordChatSanction(s ChatSanction) error {
	var until int64
	if !s.Until.IsZero() {
		until = s.Until.UnixMilli()
	}
	if _, err := d.sql.Exec(`DELETE FROM chat_sanctions WHERE until_ms != 0 AND until_ms <= ?`,
		s.At.UnixMilli()); err != nil {
		return err
	}
	_, err := d.sql.Exec(`INSERT INTO chat_sanctions
		(platform, account, user_id, action, until_ms, at_ms, by_actor, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (platform, account, user_id) DO UPDATE SET
			action = excluded.action, until_ms = excluded.until_ms,
			at_ms = excluded.at_ms, by_actor = excluded.by_actor, reason = excluded.reason`,
		s.Platform, s.Account, s.UserID, s.Action, until, s.At.UnixMilli(), s.By, s.Reason)
	return err
}

// ClearChatSanction removes the record for a lifted ban. An empty account
// lifts it in every channel on the platform, which is what an unban sent
// without one means. Missing is success.
func (d *DB) ClearChatSanction(p Platform, account, userID string) error {
	_, err := d.sql.Exec(`DELETE FROM chat_sanctions
		WHERE platform = ? AND user_id = ? AND (? = '' OR account = ?)`,
		p, userID, account, account)
	return err
}

// ActiveChatSanction reports the sanction in force on a viewer at now, if any.
//
// A timeout whose end is at or before now is over and reads as none. An empty
// account matches any channel on the platform; when more than one holds a
// sanction, the most recent is reported.
func (d *DB) ActiveChatSanction(p Platform, account, userID string, now time.Time) (ChatSanction, bool, error) {
	row := d.sql.QueryRow(`SELECT account, action, until_ms, at_ms, by_actor, reason
		FROM chat_sanctions
		WHERE platform = ? AND user_id = ? AND (? = '' OR account = ?)
			AND (until_ms = 0 OR until_ms > ?)
		ORDER BY at_ms DESC LIMIT 1`,
		p, userID, account, account, now.UnixMilli())
	var (
		s           = ChatSanction{Platform: p, UserID: userID}
		until, atMS int64
	)
	err := row.Scan(&s.Account, &s.Action, &until, &atMS, &s.By, &s.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatSanction{}, false, nil
	}
	if err != nil {
		return ChatSanction{}, false, err
	}
	if until != 0 {
		s.Until = time.UnixMilli(until)
	}
	s.At = time.UnixMilli(atMS)
	return s, true, nil
}
