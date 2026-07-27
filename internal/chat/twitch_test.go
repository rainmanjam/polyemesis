package chat

import (
	"bufio"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestParseIRCLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantOK  bool
		command string
		params  []string
		nick    string
		tags    map[string]string
	}{
		{
			name:    "a tagged PRIVMSG yields its tags, channel and text",
			line:    "@id=abc;mod=1 :bob!bob@bob.tmi.twitch.tv PRIVMSG #chan :hello there",
			wantOK:  true,
			command: "PRIVMSG",
			params:  []string{"#chan", "hello there"},
			nick:    "bob",
			tags:    map[string]string{"id": "abc", "mod": "1"},
		},
		{
			name:    "a server PING has no prefix",
			line:    "PING :tmi.twitch.tv",
			wantOK:  true,
			command: "PING",
			params:  []string{"tmi.twitch.tv"},
		},
		{
			name:    "a colon inside the message body is not a parameter separator",
			line:    ":bob!bob@bob PRIVMSG #chan :look: a colon",
			wantOK:  true,
			command: "PRIVMSG",
			params:  []string{"#chan", "look: a colon"},
			nick:    "bob",
		},
		{
			name:    "an escaped semicolon in a tag does not split the tag list",
			line:    `@display-name=we\:rd;color=#FF0000 :x!x@x PRIVMSG #c :hi`,
			wantOK:  true,
			command: "PRIVMSG",
			params:  []string{"#c", "hi"},
			nick:    "x",
			tags:    map[string]string{"display-name": "we;rd", "color": "#FF0000"},
		},
		{
			name:    "an escaped space in a tag survives",
			line:    `@system-msg=two\swords :tmi.twitch.tv USERNOTICE #c`,
			wantOK:  true,
			command: "USERNOTICE",
			params:  []string{"#c"},
			tags:    map[string]string{"system-msg": "two words"},
		},
		{
			name:    "a valueless tag decodes to empty rather than being dropped",
			line:    "@flags :x!x@x PRIVMSG #c :hi",
			wantOK:  true,
			command: "PRIVMSG",
			tags:    map[string]string{"flags": ""},
			params:  []string{"#c", "hi"},
			nick:    "x",
		},
		{
			name:    "the numeric welcome parses",
			line:    ":tmi.twitch.tv 001 polyemesis :Welcome, GLHF!",
			wantOK:  true,
			command: "001",
			params:  []string{"polyemesis", "Welcome, GLHF!"},
		},
		{name: "an empty line is skipped", line: "", wantOK: false},
		{name: "a line that is only tags is skipped", line: "@id=1", wantOK: false},
		{name: "a line that is only a prefix is skipped", line: ":tmi.twitch.tv", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseIRC(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Command != tc.command {
				t.Errorf("command = %q, want %q", got.Command, tc.command)
			}
			if tc.params != nil && !reflect.DeepEqual(got.Params, tc.params) {
				t.Errorf("params = %q, want %q", got.Params, tc.params)
			}
			if tc.nick != "" && got.Nick() != tc.nick {
				t.Errorf("nick = %q, want %q", got.Nick(), tc.nick)
			}
			for k, v := range tc.tags {
				if got.Tags[k] != v {
					t.Errorf("tag %q = %q, want %q", k, got.Tags[k], v)
				}
			}
		})
	}
}

func TestUnescapeIRCTag(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"a plain value is untouched", "hello", "hello"},
		{"backslash-colon is a semicolon", `a\:b`, "a;b"},
		{"backslash-s is a space", `a\sb`, "a b"},
		{"a doubled backslash is one backslash", `a\\b`, `a\b`},
		{"an unknown escape yields the character itself", `a\qb`, "aqb"},
		{"a trailing lone backslash is dropped", `ab\`, "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unescapeIRCTag(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTwitchEmotes(t *testing.T) {
	tests := []struct {
		name  string
		tag   string
		shift int
		want  []Emote
	}{
		{
			name: "an inclusive end becomes an exclusive one",
			tag:  "25:0-4",
			want: []Emote{{ID: "25", Start: 0, End: 5, URL: twitchEmoteURL("25")}},
		},
		{
			name: "one emote used twice yields two ranges",
			tag:  "25:0-4,12-16",
			want: []Emote{
				{ID: "25", Start: 0, End: 5, URL: twitchEmoteURL("25")},
				{ID: "25", Start: 12, End: 17, URL: twitchEmoteURL("25")},
			},
		},
		{
			name: "two emotes are separated by a slash",
			tag:  "25:0-4/1902:6-10",
			want: []Emote{
				{ID: "25", Start: 0, End: 5, URL: twitchEmoteURL("25")},
				{ID: "1902", Start: 6, End: 11, URL: twitchEmoteURL("1902")},
			},
		},
		{
			name:  "an ACTION prefix shifts the offsets back",
			tag:   "25:8-12",
			shift: 8,
			want:  []Emote{{ID: "25", Start: 0, End: 5, URL: twitchEmoteURL("25")}},
		},
		{name: "an empty tag yields nothing", tag: "", want: nil},
		{name: "a malformed range is skipped rather than failing the message", tag: "25:oops", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTwitchEmotes(tc.tag, tc.shift)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTwitchMessageNormalisation(t *testing.T) {
	a := &TwitchAdapter{cfg: TwitchConfig{Nick: "streamer", Channel: "streamer", AccountRef: "42"}}

	tests := []struct {
		name  string
		line  string
		check func(t *testing.T, m Message)
	}{
		{
			name: "display name, colour, id and timestamp come off the tags",
			line: "@id=msg-1;display-name=Bob;user-id=7;color=#1E90FF;tmi-sent-ts=1700000000000 " +
				":bob!bob@bob PRIVMSG #streamer :hello",
			check: func(t *testing.T, m Message) {
				if m.ID != "msg-1" || m.Author.Name != "Bob" || m.Author.ID != "7" {
					t.Fatalf("identity wrong: %+v", m)
				}
				if m.Author.Color != "#1E90FF" {
					t.Fatalf("colour = %q", m.Author.Color)
				}
				if !m.At.Equal(time.UnixMilli(1700000000000)) {
					t.Fatalf("timestamp = %v, want the platform's", m.At)
				}
				if m.Channel != "streamer" {
					t.Fatalf("channel = %q", m.Channel)
				}
			},
		},
		{
			name: "a missing display-name falls back to the prefix nick",
			line: ":bob!bob@bob PRIVMSG #streamer :hello",
			check: func(t *testing.T, m Message) {
				if m.Author.Name != "bob" {
					t.Fatalf("name = %q, want the prefix nick", m.Author.Name)
				}
			},
		},
		{
			name: "the mod tag and the badge list both feed the roles",
			line: "@mod=1;subscriber=1;badges=broadcaster/1,subscriber/12 :bob!bob@bob PRIVMSG #streamer :hi",
			check: func(t *testing.T, m Message) {
				m = m.Normalise(nil)
				if !m.Author.Moderator || !m.Author.Subscriber || !m.Author.Broadcaster {
					t.Fatalf("roles = %+v", m.Author)
				}
				if len(m.Author.Badges) != 2 {
					t.Fatalf("badges = %+v", m.Author.Badges)
				}
			},
		},
		{
			name: "a reply carries the parent id and name",
			line: "@reply-parent-msg-id=parent-1;reply-parent-display-name=Alice :bob!bob@bob PRIVMSG #streamer :sure",
			check: func(t *testing.T, m Message) {
				if m.ReplyToID != "parent-1" || m.ReplyTo != "Alice" {
					t.Fatalf("reply = %q/%q", m.ReplyToID, m.ReplyTo)
				}
			},
		},
		{
			name: "a CTCP ACTION is unwrapped and flagged",
			line: ":bob!bob@bob PRIVMSG #streamer :\x01ACTION waves\x01",
			check: func(t *testing.T, m Message) {
				if m.Text != "waves" || !m.Action {
					t.Fatalf("text = %q action = %v", m.Text, m.Action)
				}
			},
		},
		{
			name: "an emote in an ACTION lands on the right characters",
			line: "@emotes=25:8-12 :bob!bob@bob PRIVMSG #streamer :\x01ACTION Kappa waves\x01",
			check: func(t *testing.T, m Message) {
				m = m.Normalise(nil)
				if len(m.Emotes) != 1 {
					t.Fatalf("emotes = %+v", m.Emotes)
				}
				runes := []rune(m.Text)
				if got := string(runes[m.Emotes[0].Start:m.Emotes[0].End]); got != "Kappa" {
					t.Fatalf("emote covers %q, want Kappa", got)
				}
			},
		},
		{
			name: "an emote in a plain message lands on the right characters",
			line: "@emotes=25:0-4 :bob!bob@bob PRIVMSG #streamer :Kappa hello",
			check: func(t *testing.T, m Message) {
				m = m.Normalise(nil)
				runes := []rune(m.Text)
				if got := string(runes[m.Emotes[0].Start:m.Emotes[0].End]); got != "Kappa" {
					t.Fatalf("emote covers %q, want Kappa", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, ok := parseIRC(tc.line)
			if !ok {
				t.Fatalf("line did not parse: %q", tc.line)
			}
			m, ok := a.messageFrom(l)
			if !ok {
				t.Fatal("messageFrom rejected a PRIVMSG")
			}
			tc.check(t, m)
		})
	}
}

func TestFatalNoticeOnlyMatchesLoginFailures(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"a rejected login is fatal", "Login authentication failed", true},
		{"a malformed auth line is fatal", "Improperly formatted auth", true},
		{"a slow-mode notice is not fatal", "This room is now in slow mode.", false},
		{"an unfamiliar notice is not fatal", "Something new Twitch invented", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := parseIRC(":tmi.twitch.tv NOTICE * :" + tc.text)
			if got := fatalNotice(l); got != tc.want {
				t.Fatalf("fatalNotice = %v, want %v", got, tc.want)
			}
		})
	}
}

// ----------------------------------------------------------- transport tests
//
// These run the real read loop over an in-memory pipe. They prove the
// handshake order, the PING/PONG that keeps the connection alive past five
// minutes, and the login-failure path — none of which can be exercised against
// irc.chat.twitch.tv from a test, and all of which are where this transport
// actually breaks.

// ircServer is the far end of a net.Pipe, reading lines and writing replies.
type ircServer struct {
	conn net.Conn
	sc   *bufio.Scanner
}

func newTwitchOverPipe(t *testing.T, cfg TwitchConfig) (*TwitchAdapter, *ircServer) {
	t.Helper()
	client, server := net.Pipe()
	cfg.Dial = func(context.Context) (net.Conn, error) { return client, nil }
	if cfg.Nick == "" {
		cfg.Nick = "streamer"
	}
	if cfg.Token == "" {
		cfg.Token = "s3cret-token"
	}
	a, err := NewTwitch(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return a, &ircServer{conn: server, sc: bufio.NewScanner(server)}
}

func (s *ircServer) readLine(t *testing.T) string {
	t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !s.sc.Scan() {
		t.Fatalf("expected a line from the client: %v", s.sc.Err())
	}
	return strings.TrimRight(s.sc.Text(), "\r\n")
}

func (s *ircServer) write(t *testing.T, line string) {
	t.Helper()
	_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := s.conn.Write([]byte(line + "\r\n")); err != nil {
		t.Fatalf("writing %q: %v", line, err)
	}
}

func TestTwitchHandshakeAndPingPong(t *testing.T) {
	a, srv := newTwitchOverPipe(t, TwitchConfig{Nick: "streamer", Channel: "streamer", AccountRef: "42"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Message, 4)
	done := make(chan error, 1)
	go func() {
		done <- a.Run(ctx, SinkFunc(func(m Message) { got <- m }))
	}()

	// The capability request must come first: without tags, a message is a
	// name and a string.
	if line := srv.readLine(t); line != "CAP REQ :twitch.tv/tags twitch.tv/commands" {
		t.Fatalf("first line = %q, want the CAP request", line)
	}
	pass := srv.readLine(t)
	if !strings.HasPrefix(pass, "PASS oauth:") {
		t.Fatalf("second line = %q, want PASS", pass)
	}
	if line := srv.readLine(t); line != "NICK streamer" {
		t.Fatalf("third line = %q, want NICK", line)
	}

	// JOIN only after the server accepts the login.
	srv.write(t, ":tmi.twitch.tv 001 streamer :Welcome, GLHF!")
	if line := srv.readLine(t); line != "JOIN #streamer" {
		t.Fatalf("after 001 the client sent %q, want JOIN", line)
	}

	// The five-minute PING is the thing that drops connections when unhandled.
	srv.write(t, "PING :tmi.twitch.tv")
	if line := srv.readLine(t); line != "PONG :tmi.twitch.tv" {
		t.Fatalf("PING answered with %q, want PONG echoing the payload", line)
	}

	srv.write(t, ":tmi.twitch.tv JOIN #streamer")
	srv.write(t, "@id=m1;display-name=Bob;user-id=7 :bob!bob@bob PRIVMSG #streamer :hello there")

	select {
	case m := <-got:
		if m.Text != "hello there" || m.Author.Name != "Bob" || m.ID != "m1" {
			t.Fatalf("delivered %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the PRIVMSG never reached the sink")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after a cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestTwitchLoginFailureIsFatal(t *testing.T) {
	a, srv := newTwitchOverPipe(t, TwitchConfig{Nick: "streamer", AccountRef: "42"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, SinkFunc(func(Message) {})) }()

	srv.readLine(t) // CAP
	srv.readLine(t) // PASS
	srv.readLine(t) // NICK
	srv.write(t, ":tmi.twitch.tv NOTICE * :Login authentication failed")

	select {
	case err := <-done:
		if !IsFatal(err) {
			t.Fatalf("Run returned %v, want a fatal error so the Hub stops retrying", err)
		}
		if strings.Contains(err.Error(), "s3cret-token") {
			t.Fatal("the access token leaked into the error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a rejected login did not end the connection")
	}

	if h := a.Health(); h.State != StateFailed || h.Detail == "" {
		t.Fatalf("health = %+v, want failed with an instruction", h)
	}
	if strings.Contains(a.Health().Detail, "s3cret-token") {
		t.Fatal("the access token leaked into the health detail")
	}
}

func TestTwitchSendWritesPrivmsgAndEchoesLocally(t *testing.T) {
	a, srv := newTwitchOverPipe(t, TwitchConfig{Nick: "streamer", Channel: "streamer", AccountRef: "42"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, SinkFunc(func(Message) {})) }()

	srv.readLine(t) // CAP
	srv.readLine(t) // PASS
	srv.readLine(t) // NICK

	sent := make(chan Message, 1)
	errc := make(chan error, 1)
	go func() {
		m, err := a.Send(ctx, "hello chat")
		sent <- m
		errc <- err
	}()

	if line := srv.readLine(t); line != "PRIVMSG #streamer :hello chat" {
		t.Fatalf("sent %q", line)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	m := <-sent
	// Twitch does not deliver your own PRIVMSG back, so a local echo is the
	// only way the operator sees what they typed.
	if m.Zero() || m.Text != "hello chat" || m.Platform != db.PlatformTwitch {
		t.Fatalf("echo = %+v, want a locally rendered copy", m)
	}
}

func TestTwitchSendRefusesOverlongMessagesWithTheLimitNamed(t *testing.T) {
	a, _ := newTwitchOverPipe(t, TwitchConfig{Nick: "streamer", AccountRef: "42"})
	_, err := a.Send(context.Background(), strings.Repeat("a", TwitchMaxMessage+1))
	if err == nil {
		t.Fatal("an overlong message was accepted")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error %q does not name the limit", err)
	}
}

func TestNewTwitchExplainsMissingConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  TwitchConfig
		want string
	}{
		{"no account name", TwitchConfig{Token: "t"}, "account name"},
		{"no token", TwitchConfig{Nick: "streamer"}, "access token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTwitch(tc.cfg)
			if err == nil {
				t.Fatal("missing configuration was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("error %q does not read as a configuration state", err)
			}
		})
	}
}

func TestTwitchDialFailureIsRetryableNotFatal(t *testing.T) {
	a, err := NewTwitch(TwitchConfig{Nick: "streamer", Token: "t", AccountRef: "42",
		Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("no route to host") }})
	if err != nil {
		t.Fatal(err)
	}
	runErr := a.Run(context.Background(), SinkFunc(func(Message) {}))
	if runErr == nil {
		t.Fatal("a dial failure returned no error")
	}
	if IsFatal(runErr) {
		t.Fatal("a dial failure was marked fatal; a network blip must be retried")
	}
}
