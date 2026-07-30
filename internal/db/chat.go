package db

// Persistence for the unified cross-platform chat.
//
// This is a bounded replay buffer, not an archive. A browser that opens the
// chat pane twenty minutes into a broadcast should see the recent conversation
// rather than a blank panel, and that is the entire requirement — so the table
// is written in batches, read newest-first, and purged aggressively. Nothing
// here is the system of record for anything: the platforms own the messages,
// and losing this table costs the operator a scrollback, not data.

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// ChatMessage is one message as stored. It mirrors chat.Message, which is the
// type the rest of the system passes around; the conversion lives in the chat
// package so this one keeps no opinion about badges or emotes beyond "JSON the
// renderer understands".
type ChatMessage struct {
	ID       int64    `json:"id"`
	Platform Platform `json:"platform"`
	// Account is the platform_accounts.account_ref this arrived on. Two Twitch
	// channels connected at once are two accounts and their chat must stay
	// distinguishable.
	Account   string `json:"account,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Channel   string `json:"channel,omitempty"`

	AuthorID    string `json:"authorId,omitempty"`
	AuthorName  string `json:"authorName"`
	AuthorColor string `json:"authorColor,omitempty"`
	Moderator   bool   `json:"moderator,omitempty"`
	Subscriber  bool   `json:"subscriber,omitempty"`
	Broadcaster bool   `json:"broadcaster,omitempty"`

	Text string `json:"text"`
	// Badges and Emotes are opaque JSON arrays. They are RawMessage rather than
	// a decoded type so a platform that adds a field costs nothing here.
	Badges json.RawMessage `json:"badges,omitempty"`
	Emotes json.RawMessage `json:"emotes,omitempty"`

	ReplyToID string `json:"replyToId,omitempty"`
	ReplyTo   string `json:"replyTo,omitempty"`
	// Echo marks a message polyemesis sent itself, so the UI can render it as
	// ours even when the platform hands it back through the normal feed.
	Echo bool      `json:"echo,omitempty"`
	At   time.Time `json:"at"`
}

// jsonArray normalises an optional JSON column. An empty or malformed value
// becomes "[]" rather than an error: a badge list we cannot parse is worth
// dropping, and the message it was attached to is not.
func jsonArray(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || !json.Valid([]byte(s)) {
		return "[]"
	}
	return s
}

// AppendChatMessages stores a batch and reports how many rows were new.
//
// Conflicts on (platform, account, message_id) are ignored rather than
// updated. Every adapter can redeliver — an IRC reconnect replays nothing but
// a webhook retry and a poll that overlaps its own page both do — and a
// redelivered message is the same message. Making the write idempotent here is
// what lets the adapters stay simple about it.
func (d *DB) AppendChatMessages(msgs []ChatMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO chat_messages
		(platform, account, message_id, channel, author_id, author_name, author_color,
		 moderator, subscriber, broadcaster, text, badges, emotes, reply_to_id, reply_to, echo, at_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform, account, message_id) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, m := range msgs {
		at := m.At
		if at.IsZero() {
			at = time.Now()
		}
		res, err := stmt.Exec(m.Platform, m.Account, m.MessageID, m.Channel,
			m.AuthorID, m.AuthorName, m.AuthorColor,
			boolInt(m.Moderator), boolInt(m.Subscriber), boolInt(m.Broadcaster),
			m.Text, jsonArray(m.Badges), jsonArray(m.Emotes),
			m.ReplyToID, m.ReplyTo, boolInt(m.Echo), at.UnixMilli())
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

const chatColumns = `id, platform, account, message_id, channel, author_id, author_name, author_color,
	moderator, subscriber, broadcaster, text, badges, emotes, reply_to_id, reply_to, echo, at_ms`

// The chat reads, as whole compile-time constants rather than strings assembled
// at the call site.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. That is the point: a query
// built by concatenation at the call site is indistinguishable, to a reader and
// to a static analyser, from one that interpolates a variable — and SonarCloud
// raised exactly that on ChatMessagesByAuthor (go:S2077, Major).
//
// It was a false positive: every value in these queries travels as a `?`
// placeholder and the only concatenated part was chatColumns, a const. But
// "it is safe, trust me" is a claim the next reader has to re-derive, and a
// suppression comment is worse — it silences the rule for whatever the line
// becomes later. Making the query a constant makes it safe BY CONSTRUCTION: no
// value can reach the SQL text, because there is no expression left to reach it.
const (
	chatRecentQuery = `SELECT ` + chatColumns + ` FROM chat_messages
		ORDER BY at_ms DESC, id DESC LIMIT ?`

	chatRecentForPlatformQuery = `SELECT ` + chatColumns + ` FROM chat_messages
		WHERE platform = ? ORDER BY at_ms DESC, id DESC LIMIT ?`

	chatByAuthorQuery = `SELECT ` + chatColumns + ` FROM chat_messages
		WHERE platform = ? AND author_id = ?
		ORDER BY at_ms DESC, id DESC LIMIT ?`
)

// RecentChatMessages returns the newest limit messages in reading order —
// oldest first — because that is how a chat pane renders them and reversing a
// slice in the browser is one more thing to get wrong.
func (d *DB) RecentChatMessages(limit int) ([]ChatMessage, error) {
	return d.recentChat("", limit)
}

// RecentChatMessagesFor is the same, narrowed to one platform's tab.
func (d *DB) RecentChatMessagesFor(p Platform, limit int) ([]ChatMessage, error) {
	return d.recentChat(p, limit)
}

// ChatMessagesByAuthor returns what one person has said, newest last.
//
// This is the data behind the moderator's user card, and it is worth saying why
// it is a local query rather than a platform call: NO platform here publishes an
// API for a user's message history. Twitch's mod card — the window that opens
// when a moderator clicks a name — is a Twitch web-app feature backed by
// internal endpoints, not by anything in Helix; Helix offers Get Chatters (who
// is present now) and Get Moderators, neither of which is a history. YouTube,
// Kick and Facebook publish nothing comparable at all.
//
// polyemesis does not need one. Every message it has ever stored carries
// author_id, on all four platforms, so this works uniformly — and across
// platforms, which Twitch's own card cannot do.
//
// The honest limitation is depth, not breadth: this reads polyemesis's own
// retained scrollback, which defaults to two hours or 2000 messages. It is
// shallower than Twitch's card and the UI has to say so, because a moderator who
// reads "3 messages" as "this person has said three things ever" has been misled
// by a window that was only ever showing them a slice.
func (d *DB) ChatMessagesByAuthor(p Platform, authorID string, limit int) ([]ChatMessage, error) {
	if limit <= 0 || strings.TrimSpace(authorID) == "" {
		return []ChatMessage{}, nil
	}
	rows, err := d.sql.Query(chatByAuthorQuery, p, authorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ChatMessage{}
	for rows.Next() {
		m, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Oldest first, like every other chat read here: a card is read top to
	// bottom the same way the pane is.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (d *DB) recentChat(p Platform, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		return []ChatMessage{}, nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if p == "" {
		rows, err = d.sql.Query(chatRecentQuery, limit)
	} else {
		rows, err = d.sql.Query(chatRecentForPlatformQuery, p, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ChatMessage{}
	for rows.Next() {
		m, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func scanChat(rows *sql.Rows) (ChatMessage, error) {
	var (
		m                            ChatMessage
		badges, emotes               string
		mod, sub, caster, echo       int
		atMS                         int64
		account, msgID, channel      string
		authorID, authorName, colour string
		replyID, replyTo             string
	)
	err := rows.Scan(&m.ID, &m.Platform, &account, &msgID, &channel,
		&authorID, &authorName, &colour,
		&mod, &sub, &caster, &m.Text, &badges, &emotes,
		&replyID, &replyTo, &echo, &atMS)
	if err != nil {
		return m, err
	}
	m.Account, m.MessageID, m.Channel = account, msgID, channel
	m.AuthorID, m.AuthorName, m.AuthorColor = authorID, authorName, colour
	m.ReplyToID, m.ReplyTo = replyID, replyTo
	m.Moderator, m.Subscriber, m.Broadcaster, m.Echo = mod != 0, sub != 0, caster != 0, echo != 0
	m.Badges, m.Emotes = json.RawMessage(badges), json.RawMessage(emotes)
	m.At = time.UnixMilli(atMS)
	return m, nil
}

// DeleteChatMessage removes one message, for a moderator deletion arriving from
// the platform. Missing is success: a delete for a message we purged an hour
// ago has already achieved what it wanted.
func (d *DB) DeleteChatMessage(p Platform, account, messageID string) error {
	_, err := d.sql.Exec(`DELETE FROM chat_messages
		WHERE platform = ? AND account = ? AND message_id = ?`, p, account, messageID)
	return err
}

// PurgeChatMessages drops messages older than cutoff, always keeping the newest
// keep of them whatever their age.
//
// The keep floor is the important half. A channel that was quiet for a day
// still deserves its last hundred messages on screen, and an age-only purge
// would hand it an empty pane — which reads exactly like chat being broken.
func (d *DB) PurgeChatMessages(cutoff time.Time, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	res, err := d.sql.Exec(`DELETE FROM chat_messages
		WHERE at_ms < ?
		AND id NOT IN (SELECT id FROM chat_messages ORDER BY at_ms DESC, id DESC LIMIT ?)`,
		cutoff.UnixMilli(), keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ChatMessageCount is the row count, for the purge scheduler and the status
// page.
func (d *DB) ChatMessageCount() (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM chat_messages`).Scan(&n)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
