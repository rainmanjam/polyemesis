package chat

// Twitch chat over IRC, and the adapter the Adapter interface was shaped
// around. It is the only one of the four platforms that gives polyemesis a
// real push stream, so everything the others have to fake — liveness, ordering,
// badges, emote positions — arrives here for free and set the vocabulary the
// rest of the package normalises into.
//
// Two things about this transport are non-negotiable and have bitten every
// project that has implemented it: the server PINGs roughly every five minutes
// and closes the connection if the PONG does not come back, and a socket that
// stops being read is dropped without ceremony. Both are handled below, and
// neither can be tested against Twitch offline — see the package doc for what
// is tested instead.

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// TwitchIRCAddr is the TLS chat endpoint. Plaintext 6667 exists and is not
	// offered: the OAuth token goes over this socket.
	TwitchIRCAddr = "irc.chat.twitch.tv:6697"

	// twitchReadTimeout is how long a silent socket is tolerated. Twitch PINGs
	// about every five minutes, so eight minutes of nothing means the
	// connection is gone in a way TCP has not noticed yet — the classic
	// half-open socket that makes chat look alive and empty for hours.
	twitchReadTimeout = 8 * time.Minute
	// twitchKeepalive is our own PING interval. It is shorter than the read
	// timeout so a dead path is discovered by us rather than waited out.
	twitchKeepalive = 3 * time.Minute
	// twitchWriteTimeout bounds a blocked write.
	twitchWriteTimeout = 15 * time.Second
	// twitchMaxLine is the read buffer. IRCv3 tags make a PRIVMSG far longer
	// than the classic 512 bytes; Twitch caps tags at 4096 and the message at
	// 500, and this leaves room for both plus the prefix.
	twitchMaxLine = 16 << 10
	// TwitchMaxMessage is Twitch's published chat message limit.
	TwitchMaxMessage = 500
)

// TwitchConfig configures the IRC adapter.
type TwitchConfig struct {
	// Nick is the login name of the account the token belongs to, lowercase.
	// Twitch authenticates on the token but still requires the NICK to match
	// it, and the mismatch failure ("Login authentication failed") does not say
	// which of the two was wrong.
	Nick string
	// Channel is the channel to read, without the leading '#'. It is normally
	// the same as Nick, and is separate so an operator can read a channel they
	// moderate rather than their own.
	Channel string
	// AccountRef is the platform_accounts.account_ref this connection belongs
	// to; it travels on every message.
	AccountRef string
	// Token is the OAuth access token. It is written once, to the PASS line,
	// and appears in no log, error or status message anywhere in this file.
	Token string
	// Dial replaces the TLS dial. It is the seam the tests use, and nothing at
	// runtime sets it.
	Dial func(ctx context.Context) (net.Conn, error)
}

// TwitchAdapter reads and writes one Twitch channel's chat.
type TwitchAdapter struct {
	cfg TwitchConfig

	mu     sync.Mutex
	health Health
	conn   net.Conn
	// writeMu serialises writes: the keepalive ticker and the read loop's
	// PONG both write to the same socket.
	writeMu sync.Mutex
}

// NewTwitch builds the adapter. A missing nick, channel or token is a
// configuration state rather than a crash, and the error says which one so the
// caller can render "not configured" with the missing piece named.
func NewTwitch(cfg TwitchConfig) (*TwitchAdapter, error) {
	cfg.Nick = strings.ToLower(strings.TrimSpace(cfg.Nick))
	cfg.Channel = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cfg.Channel), "#"))
	if cfg.Channel == "" {
		cfg.Channel = cfg.Nick
	}
	switch {
	case cfg.Nick == "":
		return nil, fmt.Errorf("twitch chat is not configured: no account name, so reconnect the Twitch account")
	case cfg.Token == "":
		return nil, fmt.Errorf("twitch chat is not configured: no access token, so connect the Twitch account in Settings → Platforms")
	}
	if cfg.AccountRef == "" {
		cfg.AccountRef = cfg.Nick
	}
	return &TwitchAdapter{
		cfg:    cfg,
		health: Health{State: StateConnecting},
	}, nil
}

func (t *TwitchAdapter) Platform() db.Platform { return db.PlatformTwitch }
func (t *TwitchAdapter) Account() string       { return t.cfg.AccountRef }

// Health is the adapter's own view, which is the only place that knows whether
// the JOIN completed.
func (t *TwitchAdapter) Health() Health {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.health
}

func (t *TwitchAdapter) setHealth(state State, detail string) {
	t.mu.Lock()
	t.health = Health{State: state, Detail: detail}
	t.mu.Unlock()
}

// Run connects, joins and reads until ctx ends or the connection breaks.
func (t *TwitchAdapter) Run(ctx context.Context, sink Sink) error {
	conn, err := t.dial(ctx)
	if err != nil {
		t.setHealth(StateFailed, "could not reach Twitch chat: "+err.Error())
		return err
	}
	defer conn.Close()

	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.conn = nil
		t.mu.Unlock()
	}()

	// Closing the socket is what unblocks the read below; a context that ends
	// while we are parked in Read would otherwise wait out the read deadline.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	t.setHealth(StateConnecting, "connecting to Twitch chat")
	// Tags carry the badges, colour, emote positions and message id — without
	// this capability a Twitch message is a name and a string. Commands carries
	// RECONNECT and the moderation notices.
	if err := t.writeLine(conn, "CAP REQ :twitch.tv/tags twitch.tv/commands"); err != nil {
		return err
	}
	if err := t.writeLine(conn, "PASS oauth:"+t.cfg.Token); err != nil {
		return fmt.Errorf("sending Twitch credentials failed: %w", err)
	}
	if err := t.writeLine(conn, "NICK "+t.cfg.Nick); err != nil {
		return err
	}

	go t.keepalive(ctx, conn, stop)

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), twitchMaxLine)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(twitchReadTimeout))
		if !sc.Scan() {
			if ctx.Err() != nil {
				t.setHealth(StateStopped, "")
				return nil
			}
			if err := sc.Err(); err != nil {
				t.setHealth(StateFailed, "Twitch chat connection dropped")
				return fmt.Errorf("twitch chat connection dropped: %w", err)
			}
			t.setHealth(StateFailed, "Twitch closed the chat connection")
			return fmt.Errorf("twitch closed the chat connection")
		}

		line, ok := parseIRC(strings.TrimRight(sc.Text(), "\r\n"))
		if !ok {
			continue
		}
		if done, err := t.handle(conn, line, sink); done {
			return err
		}
	}
}

// handle processes one parsed line. It returns done=true when the connection
// should end, with the error that ends it (nil for a clean end the Hub should
// simply reconnect after).
func (t *TwitchAdapter) handle(conn net.Conn, l ircLine, sink Sink) (bool, error) {
	switch l.Command {
	case "PING":
		// Answering this is the whole reason the connection survives past five
		// minutes. The payload is echoed back verbatim.
		payload := ":tmi.twitch.tv"
		if len(l.Params) > 0 {
			payload = ":" + l.Params[len(l.Params)-1]
		}
		if err := t.writeLine(conn, "PONG "+payload); err != nil {
			return true, err
		}

	case "PONG":
		// Our own keepalive came back; nothing to do but be reassured.

	case "001":
		// Welcome: authentication succeeded, so the channel can be joined.
		if err := t.writeLine(conn, "JOIN #"+t.cfg.Channel); err != nil {
			return true, err
		}

	case "JOIN":
		if strings.EqualFold(l.Nick(), t.cfg.Nick) {
			t.setHealth(StateLive, "")
		}

	case "RECONNECT":
		// Twitch is about to restart this server. A clean return sends the Hub
		// round its normal reconnect path.
		t.setHealth(StateDegraded, "Twitch asked us to reconnect")
		return true, nil

	case "NOTICE":
		if fatalNotice(l) {
			// A rejected token cannot be fixed by trying again, and retrying a
			// bad password every thirty seconds is how an IP gets banned.
			t.setHealth(StateFailed, "Twitch rejected the chat login; reconnect the Twitch account in Settings → Platforms")
			return true, Fatal(fmt.Errorf("twitch rejected the chat login (the token may have expired or lack chat:read); " +
				"reconnect the Twitch account in Settings → Platforms"))
		}

	case "PRIVMSG":
		if m, ok := t.messageFrom(l); ok {
			t.setHealth(StateLive, "")
			sink.Deliver(m)
		}
	}
	return false, nil
}

func (t *TwitchAdapter) dial(ctx context.Context) (net.Conn, error) {
	if t.cfg.Dial != nil {
		return t.cfg.Dial(ctx)
	}
	d := &tls.Dialer{Config: &tls.Config{ServerName: "irc.chat.twitch.tv", MinVersion: tls.VersionTLS12}}
	return d.DialContext(ctx, "tcp", TwitchIRCAddr)
}

// keepalive PINGs on a slow ticker. Twitch's own PING is the primary
// heartbeat; this one exists so that a path which has gone away without an RST
// is discovered in three minutes rather than eight.
func (t *TwitchAdapter) keepalive(ctx context.Context, conn net.Conn, stop <-chan struct{}) {
	tk := time.NewTicker(twitchKeepalive)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-tk.C:
			if err := t.writeLine(conn, "PING :polyemesis"); err != nil {
				return
			}
		}
	}
}

// writeLine sends one IRC line. The payload is never logged: the PASS line
// carries the access token, and one careless debug statement here would put it
// in every operator's log file.
func (t *TwitchAdapter) writeLine(conn net.Conn, line string) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(twitchWriteTimeout))
	_, err := conn.Write([]byte(line + "\r\n"))
	return err
}

// Send posts to the channel.
//
// It returns a synthesised echo because Twitch does not deliver your own
// PRIVMSG back to you: without this the operator would type into the unified
// box and watch nothing appear, on the one platform where the message did
// definitely arrive.
func (t *TwitchAdapter) Send(ctx context.Context, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, fmt.Errorf("nothing to send")
	}
	// Twitch's 500-character limit is published, so saying so beats a silent
	// truncation at the server that leaves the operator wondering where the end
	// of their sentence went.
	if n := len([]rune(text)); n > TwitchMaxMessage {
		return Message{}, fmt.Errorf("Twitch accepts %d characters and this message is %d", TwitchMaxMessage, n)
	}

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return Message{}, fmt.Errorf("Twitch chat is not connected")
	}
	if err := t.writeLine(conn, "PRIVMSG #"+t.cfg.Channel+" :"+text); err != nil {
		return Message{}, err
	}

	return Message{
		Platform: db.PlatformTwitch,
		Account:  t.cfg.AccountRef,
		Channel:  t.cfg.Channel,
		Author:   Author{ID: t.cfg.AccountRef, Name: t.cfg.Nick},
		Text:     text,
		At:       time.Now(),
	}, nil
}

// messageFrom converts a PRIVMSG into the unified model.
func (t *TwitchAdapter) messageFrom(l ircLine) (Message, bool) {
	if len(l.Params) < 2 {
		return Message{}, false
	}
	channel := strings.TrimPrefix(l.Params[0], "#")
	text := l.Params[len(l.Params)-1]

	action := false
	// A /me arrives CTCP-wrapped. The emote offsets in the tag are indexed
	// against this raw form, so they are shifted below by exactly what is
	// stripped here.
	const ctcp = "\x01ACTION "
	if strings.HasPrefix(text, ctcp) && strings.HasSuffix(text, "\x01") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, ctcp), "\x01")
		action = true
	}

	m := Message{
		ID:       l.Tags["id"],
		Platform: db.PlatformTwitch,
		Account:  t.cfg.AccountRef,
		Channel:  channel,
		Text:     text,
		Action:   action,
		Author: Author{
			ID:    l.Tags["user-id"],
			Name:  firstNonEmpty(l.Tags["display-name"], l.Nick()),
			Color: l.Tags["color"],
			// The mod tag is authoritative for moderators; the badge list adds
			// the broadcaster and the subscriber, and Normalise folds the two
			// sources together.
			Moderator:  l.Tags["mod"] == "1",
			Subscriber: l.Tags["subscriber"] == "1",
			Badges:     parseTwitchBadges(l.Tags["badges"]),
		},
		ReplyToID: l.Tags["reply-parent-msg-id"],
		ReplyTo:   l.Tags["reply-parent-display-name"],
		At:        twitchTime(l.Tags["tmi-sent-ts"]),
	}

	shift := 0
	if action {
		shift = len([]rune(ctcp))
	}
	m.Emotes = parseTwitchEmotes(l.Tags["emotes"], shift)
	return m, true
}

// twitchTime reads the tmi-sent-ts tag, falling back to now. Using the
// platform's timestamp keeps a burst in the order Twitch put it in, which is
// not necessarily the order it reached this machine.
func twitchTime(s string) time.Time {
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ms)
}

// parseTwitchBadges reads "moderator/1,subscriber/12".
func parseTwitchBadges(s string) []Badge {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]Badge, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		name, version, _ := strings.Cut(p, "/")
		out = append(out, Badge{ID: name, Version: version})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseTwitchEmotes reads "25:0-4,12-16/1902:6-10" into rune ranges.
//
// Twitch's ends are inclusive and this model's are exclusive, so every end
// gains one. shift accounts for a CTCP ACTION prefix that was stripped from the
// text after the offsets were computed. Anything unparseable is skipped rather
// than failing the message: an emote rendered as its literal name is a small
// loss, and a dropped message is not.
func parseTwitchEmotes(s string, shift int) []Emote {
	if s == "" {
		return nil
	}
	var out []Emote
	for _, group := range strings.Split(s, "/") {
		id, ranges, ok := strings.Cut(group, ":")
		if !ok || id == "" {
			continue
		}
		for _, r := range strings.Split(ranges, ",") {
			startS, endS, ok := strings.Cut(r, "-")
			if !ok {
				continue
			}
			start, err1 := strconv.Atoi(startS)
			end, err2 := strconv.Atoi(endS)
			if err1 != nil || err2 != nil {
				continue
			}
			out = append(out, Emote{
				ID:    id,
				Start: start - shift,
				End:   end + 1 - shift,
				URL:   twitchEmoteURL(id),
			})
		}
	}
	return out
}

// twitchEmoteURL is Twitch's published CDN template. It is built here rather
// than in the browser so that every platform's emotes arrive as a URL and the
// renderer needs no per-platform knowledge.
func twitchEmoteURL(id string) string {
	return "https://static-cdn.jtvnw.net/emoticons/v2/" + id + "/default/dark/1.0"
}

// fatalNotice recognises the login failures that retrying cannot fix. The
// match is on Twitch's own wording, and anything unrecognised is treated as
// retryable — guessing that an unfamiliar NOTICE is fatal would disconnect a
// working chat over a message we had simply never seen.
func fatalNotice(l ircLine) bool {
	if len(l.Params) == 0 {
		return false
	}
	text := strings.ToLower(l.Params[len(l.Params)-1])
	return strings.Contains(text, "login authentication failed") ||
		strings.Contains(text, "improperly formatted auth") ||
		strings.Contains(text, "login unsuccessful")
}

// ------------------------------------------------------------- IRC parsing

// ircLine is one parsed IRCv3 line.
type ircLine struct {
	Tags    map[string]string
	Prefix  string
	Command string
	Params  []string
}

// Nick is the sending nickname out of the prefix, empty for a server message.
func (l ircLine) Nick() string {
	if l.Prefix == "" {
		return ""
	}
	if i := strings.IndexAny(l.Prefix, "!@"); i >= 0 {
		return l.Prefix[:i]
	}
	return l.Prefix
}

// parseIRC parses "@tags :prefix COMMAND params :trailing".
//
// It is a total function: a line it does not understand yields ok=false and is
// skipped, because one malformed frame must never end a connection that is
// otherwise delivering chat.
func parseIRC(line string) (ircLine, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ircLine{}, false
	}
	var out ircLine

	if strings.HasPrefix(line, "@") {
		raw, rest, ok := strings.Cut(line[1:], " ")
		if !ok {
			return ircLine{}, false
		}
		out.Tags = parseIRCTags(raw)
		line = strings.TrimLeft(rest, " ")
	}
	if strings.HasPrefix(line, ":") {
		prefix, rest, ok := strings.Cut(line[1:], " ")
		if !ok {
			return ircLine{}, false
		}
		out.Prefix = prefix
		line = strings.TrimLeft(rest, " ")
	}
	if line == "" {
		return ircLine{}, false
	}

	// The trailing parameter is everything after " :" and may contain spaces
	// and colons; it has to come off before the rest is split.
	rest := line
	var trailing string
	hasTrailing := false
	if i := strings.Index(rest, " :"); i >= 0 {
		trailing, hasTrailing = rest[i+2:], true
		rest = rest[:i]
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ircLine{}, false
	}
	out.Command = strings.ToUpper(fields[0])
	out.Params = fields[1:]
	if hasTrailing {
		out.Params = append(out.Params, trailing)
	}
	if out.Tags == nil {
		out.Tags = map[string]string{}
	}
	return out, true
}

// parseIRCTags reads "a=1;b=escaped\svalue;c".
func parseIRCTags(raw string) map[string]string {
	tags := map[string]string{}
	for _, kv := range strings.Split(raw, ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if k == "" {
			continue
		}
		tags[k] = unescapeIRCTag(v)
	}
	return tags
}

// unescapeIRCTag applies the IRCv3 escapes. A display name containing a
// semicolon arrives as "\:" and would otherwise split the tag list; getting
// this wrong corrupts every tag after it.
func unescapeIRCTag(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			b.WriteByte(v[i])
			continue
		}
		if i+1 >= len(v) {
			// A trailing lone backslash is dropped, per the spec.
			break
		}
		i++
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case '\\':
			b.WriteByte('\\')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}
