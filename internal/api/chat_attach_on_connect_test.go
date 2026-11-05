package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// A newly connected account reading no chat until a restart.
//
// Connect Twitch in Settings → Platforms and its chat never reached the
// Dashboard's chat card: StartChat attached one adapter per account, once, at
// startup, and neither the OAuth callback nor the device-code poll ever told
// the Hub. The same gap swallowed the reconnect a refused Twitch login asks for
// ("reconnect the Twitch account in Settings → Platforms"): the failed adapter
// stayed attached, so the chip went on reading Failed until the process
// restarted.
//
// Everything below runs the real handlers and the real adapter constructors
// against fakes: a fake platform for the token exchange and the identity read,
// and a fake Twitch IRC server on loopback that the real Twitch adapter dials
// through Server.twitchChatDial. No real credential or platform is involved.

// twitchUserRef is the Twitch account every fake connect signs in as.
const twitchUserRef = "44322889"

// fakeConnectPlatform answers the calls a connect makes: Twitch's token
// exchange (code and device grants alike), its /users read and its device-code
// start, and X's token exchange and /2/users/me.
type fakeConnectPlatform struct {
	URL string

	mu sync.Mutex
	// tokens are handed out in order by Twitch's token endpoint, so a test can
	// tell one connect's adapter from the next by the token on its PASS line.
	tokens []string
	issued int
	// duringExchange, when set, runs inside Twitch's token exchange: after the
	// callback has read the credentials and before it stores the account.
	duringExchange func()
}

func newFakeConnectPlatform(t *testing.T, tokens ...string) *fakeConnectPlatform {
	t.Helper()
	f := &fakeConnectPlatform{tokens: tokens}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *fakeConnectPlatform) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/oauth2/token":
		f.mu.Lock()
		f.issued++
		tok := fmt.Sprintf("tw-at-%d", f.issued)
		if len(f.tokens) > 0 {
			tok, f.tokens = f.tokens[0], f.tokens[1:]
		}
		during := f.duringExchange
		f.mu.Unlock()
		if during != nil {
			during()
		}
		write(w, stubAnswer{http.StatusOK, `{"access_token":"` + tok + `","refresh_token":"tw-rt",` +
			`"expires_in":14400,"scope":["chat:read","chat:edit"],"token_type":"bearer"}`})
	case "/users":
		write(w, stubAnswer{http.StatusOK,
			`{"data":[{"id":"` + twitchUserRef + `","login":"dallas","display_name":"Dallas"}]}`})
	case "/oauth2/device":
		write(w, stubAnswer{http.StatusOK, `{"device_code":"DEV-CODE","user_code":"ABCD-1234",` +
			`"verification_uri":"https://www.twitch.tv/activate","expires_in":1800,"interval":5}`})
	case "/2/oauth2/token":
		write(w, stubAnswer{http.StatusOK, `{"access_token":"x-at","refresh_token":"x-rt",` +
			`"expires_in":7200,"scope":"tweet.read users.read offline.access","token_type":"bearer"}`})
	case "/2/users/me":
		write(w, stubAnswer{http.StatusOK, `{"data":{"id":"7777","username":"jev","name":"Jev"}}`})
	default:
		write(w, stubAnswer{http.StatusNotFound, `{"message":"no stub for ` + r.URL.Path + `"}`})
	}
}

// fakeTwitchIRC is a Twitch chat server on loopback. It welcomes any token
// except the ones it was told to refuse, which get Twitch's own fatal login
// notice, and it keeps every connection it accepted so a test can count the
// ones still open and see which token each one signed in with.
type fakeTwitchIRC struct {
	ln net.Listener

	mu     sync.Mutex
	conns  []*fakeIRCConn
	refuse map[string]bool
	sent   int
}

type fakeIRCConn struct {
	c      net.Conn
	token  string
	nick   string
	joined string
	closed bool
}

func newFakeTwitchIRC(t *testing.T, refuse ...string) *fakeTwitchIRC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake IRC listen: %v", err)
	}
	f := &fakeTwitchIRC{ln: ln, refuse: map[string]bool{}}
	for _, tok := range refuse {
		f.refuse[tok] = true
	}
	go f.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			_ = c.c.Close()
		}
	})
	return f
}

// dial is what goes into Server.twitchChatDial.
func (f *fakeTwitchIRC) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", f.ln.Addr().String())
}

func (f *fakeTwitchIRC) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		fc := &fakeIRCConn{c: c}
		f.mu.Lock()
		f.conns = append(f.conns, fc)
		f.mu.Unlock()
		go f.serve(fc)
	}
}

func (f *fakeTwitchIRC) serve(fc *fakeIRCConn) {
	defer func() {
		f.mu.Lock()
		fc.closed = true
		f.mu.Unlock()
		_ = fc.c.Close()
	}()
	sc := bufio.NewScanner(fc.c)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case strings.HasPrefix(line, "PASS oauth:"):
			f.mu.Lock()
			fc.token = strings.TrimPrefix(line, "PASS oauth:")
			f.mu.Unlock()
		case strings.HasPrefix(line, "NICK "):
			f.mu.Lock()
			fc.nick = strings.TrimPrefix(line, "NICK ")
			nick, refused := fc.nick, f.refuse[fc.token]
			f.mu.Unlock()
			if refused {
				// Kept open rather than closed: the adapter ends the
				// connection itself on this notice, and a close from here could
				// race it to an RST that loses the notice.
				f.send(fc, ":tmi.twitch.tv NOTICE * :Login authentication failed")
				continue
			}
			f.send(fc, ":tmi.twitch.tv 001 "+nick+" :Welcome, GLHF!")
		case strings.HasPrefix(line, "JOIN "):
			ch := strings.TrimPrefix(line, "JOIN ")
			f.mu.Lock()
			fc.joined = ch
			nick := fc.nick
			f.mu.Unlock()
			f.send(fc, ":"+nick+"!"+nick+"@"+nick+".tmi.twitch.tv JOIN "+ch)
		case strings.HasPrefix(line, "PING"):
			f.send(fc, "PONG :tmi.twitch.tv")
		}
	}
}

func (f *fakeTwitchIRC) send(fc *fakeIRCConn, line string) {
	_, _ = fc.c.Write([]byte(line + "\r\n"))
}

// open is the token of every connection still open, in the order accepted.
func (f *fakeTwitchIRC) open() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.conns {
		if !c.closed {
			out = append(out, c.token)
		}
	}
	return out
}

// accepted is how many connections the server has ever seen.
func (f *fakeTwitchIRC) accepted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// say posts a chat line, as a viewer, to every open connection that has
// joined its channel.
func (f *fakeTwitchIRC) say(text string) {
	f.mu.Lock()
	f.sent++
	id := f.sent
	var to []*fakeIRCConn
	for _, c := range f.conns {
		if !c.closed && c.joined != "" {
			to = append(to, c)
		}
	}
	f.mu.Unlock()
	for _, c := range to {
		f.send(c, fmt.Sprintf("@id=fake-msg-%d;user-id=99;display-name=Viewer :viewer!viewer@viewer.tmi.twitch.tv PRIVMSG %s :%s",
			id, c.joined, text))
	}
}

// chatConnectServer is a server with chat on and started, the given platforms'
// developer credentials stored, providers pointed at the fake platform and
// Twitch chat pointed at the fake IRC server.
func chatConnectServer(t *testing.T, fake *fakeConnectPlatform, irc *fakeTwitchIRC, creds ...db.Platform) (*Server, http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	s, h, store := testServerWith(t, Options{Config: config.Config{}, Providers: oauth.NewSet(oauth.WithBaseURL(fake.URL))})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	if irc != nil {
		s.twitchChatDial = irc.dial
	}
	for _, p := range creds {
		if err := store.PutPlatformCreds(s.box, p, string(p)+"-app", string(p)+"-secret"); err != nil {
			t.Fatalf("store %s credentials: %v", p, err)
		}
	}
	return s, h, store, login(t, h)
}

// startChat runs StartChat under a lifetime the test owns, the way main runs it
// under the process's.
func startChat(t *testing.T, s *Server) int {
	t.Helper()
	life, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	return s.StartChat(life)
}

// oauthCallback runs one authorization-code callback through the router. The
// request's context is cancelled the moment the handler returns, as a real
// server's is, so an adapter attached under the request rather than under the
// server's lifetime stops with it. It returns the redirect's query, or an
// error, so it can run off the test goroutine.
func oauthCallback(h http.Handler, sign func(*http.Request), store *db.DB, p db.Platform, state string) (url.Values, error) {
	if err := store.PutOAuthState(state, p, ""); err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/oauth/"+string(p)+"/callback?code=the-code&state="+state, nil).WithContext(ctx)
	r.RemoteAddr = "203.0.113.5:44444"
	sign(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cancel()
	if w.Code != http.StatusFound {
		return nil, fmt.Errorf("callback: status %d, body %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		return nil, err
	}
	q := loc.Query()
	if e := q.Get("oauth_error"); e != "" {
		return nil, fmt.Errorf("callback reported an error: %s", e)
	}
	return q, nil
}

// connectTwitch is oauthCallback for Twitch on the test goroutine.
func connectTwitch(t *testing.T, h http.Handler, sign func(*http.Request), store *db.DB, state string) {
	t.Helper()
	if _, err := oauthCallback(h, sign, store, db.PlatformTwitch, state); err != nil {
		t.Fatal(err)
	}
}

// chatsOn is every attached adapter on one platform, as the Hub reports them.
func chatsOn(h *chat.Hub, p db.Platform) []chat.Status {
	var out []chat.Status
	for _, st := range h.Statuses() {
		if st.Platform == p {
			out = append(out, st)
		}
	}
	return out
}

// heard reports whether a chat line with this text has reached the Hub.
func heard(h *chat.Hub, text string) bool {
	for _, m := range h.History(0) {
		if m.Text == text {
			return true
		}
	}
	return false
}

// accountID is the id of the stored account with this key, or zero.
func accountID(t *testing.T, store *db.DB, p db.Platform, ref string) int64 {
	t.Helper()
	accts, err := store.ListPlatformAccounts()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	for _, a := range accts {
		if a.Platform == p && a.AccountRef == ref {
			return a.ID
		}
	}
	return 0
}

// seedTwitchChatAccount stores the fake's Twitch account with a given token, as an
// earlier connect would have, and returns its id.
func seedTwitchChatAccount(t *testing.T, s *Server, store *db.DB, token string) int64 {
	t.Helper()
	a, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "Dallas", AccountRef: twitchUserRef,
		AccessToken: token, RefreshToken: "tw-rt", ExpiresAt: time.Now().Add(4 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed twitch account: %v", err)
	}
	return a.ID
}

// TestConnectingAnAccountStartsItsChatAtOnce is the report: connect Twitch and
// its chat is on the Dashboard straight away, not after a restart.
//
// The chip alone is not enough. An adapter attached under the callback's own
// context would be listed and then stop the moment the redirect went out, so a
// line sent AFTER the request has ended has to arrive.
//
// Mutations:
//   - drop the attachAccountChat call from handleOAuthCallback. Observed FAIL
//     ("connecting Twitch attached no chat").
//   - make connectChat attach under a context cancelled when it returns, as a
//     request's is. Observed FAIL ("timed out waiting for the Twitch adapter
//     to sign in to the fake IRC server"): the chip is listed, and the chat
//     behind it never starts.
func TestConnectingAnAccountStartsItsChatAtOnce(t *testing.T) {
	fake := newFakeConnectPlatform(t)
	irc := newFakeTwitchIRC(t)
	s, h, store, sign := chatConnectServer(t, fake, irc, db.PlatformTwitch)
	if n := startChat(t, s); n != 0 {
		t.Fatalf("StartChat attached %d adapters with nothing connected", n)
	}

	connectTwitch(t, h, sign, store, "state-twitch-1")

	if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 1 || got[0].Account != twitchUserRef {
		t.Fatalf("connecting Twitch attached no chat: the Hub lists %v, want one adapter for %s", got, twitchUserRef)
	}
	if got := chatAccounts(t, h, sign); !got["twitch:"+twitchUserRef] {
		t.Fatalf("GET /chat does not list the Twitch account just connected, so the Dashboard draws no chip: %v", got)
	}
	waitFor(t, "the Twitch adapter to sign in to the fake IRC server", func() bool {
		open := irc.open()
		return len(open) == 1 && open[0] == "tw-at-1"
	})
	waitFor(t, "the Twitch chat to report live", func() bool {
		st := chatsOn(s.chat, db.PlatformTwitch)
		return len(st) == 1 && st[0].State == chat.StateLive
	})

	irc.say("hello from after the redirect")
	deadline := time.Now().Add(5 * time.Second)
	for !heard(s.chat, "hello from after the redirect") {
		if time.Now().After(deadline) {
			t.Fatalf("a chat line sent after the connect finished never arrived; the adapter is not running (statuses %v)",
				chatsOn(s.chat, db.PlatformTwitch))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestConnectingByDeviceCodeStartsItsChatAtOnce is the other way an account is
// created: Twitch's device-code flow, which stores the account from the poll
// rather than from a callback.
//
// Mutation: drop the attachAccountChat call from handlePollDeviceAuth.
// Observed FAIL ("a device-code connect attached no chat").
func TestConnectingByDeviceCodeStartsItsChatAtOnce(t *testing.T) {
	fake := newFakeConnectPlatform(t, "tw-device-1")
	irc := newFakeTwitchIRC(t)
	s, h, _, sign := chatConnectServer(t, fake, irc, db.PlatformTwitch)
	startChat(t, s)

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)
	if got := pollDeviceFlow(t, h, sign, start.Handle); got.State != deviceConnected {
		t.Fatalf("poll state = %q, want %q", got.State, deviceConnected)
	}

	if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 1 || got[0].Account != twitchUserRef {
		t.Fatalf("a device-code connect attached no chat: the Hub lists %v", got)
	}
	waitFor(t, "the device-code account's chat to sign in", func() bool {
		open := irc.open()
		return len(open) == 1 && open[0] == "tw-device-1"
	})
}

// TestReconnectingReplacesTheAdapterRatherThanAddingOne pins the idempotence:
// connecting an account again leaves exactly one adapter for it, and it is the
// new one.
//
// The first half is the case that matters most. Twitch refuses a login, the
// adapter stops for good and tells the operator to reconnect; the reconnect
// must bring the chat back. The Hub already refuses a second adapter for one
// account, so a reconnect that only attached would keep the failed one and the
// chip would go on reading Failed. The second half reconnects over a healthy
// chat and counts the open connections, so the old adapter's socket must
// actually close.
//
// Mutation: drop the detachChat call from attachAccountChat (attach only).
// Observed FAIL ("the reconnect kept the adapter whose login was refused").
func TestReconnectingReplacesTheAdapterRatherThanAddingOne(t *testing.T) {
	fake := newFakeConnectPlatform(t, "tw-fresh-2", "tw-fresh-3")
	irc := newFakeTwitchIRC(t, "tw-revoked")
	s, h, store, sign := chatConnectServer(t, fake, irc, db.PlatformTwitch)
	seedTwitchChatAccount(t, s, store, "tw-revoked")
	if n := startChat(t, s); n != 1 {
		t.Fatalf("StartChat attached %d adapters, want 1", n)
	}
	waitFor(t, "Twitch to refuse the revoked token", func() bool {
		st := chatsOn(s.chat, db.PlatformTwitch)
		return len(st) == 1 && st[0].State == chat.StateFailed
	})
	failed, _ := s.chat.Adapter(db.PlatformTwitch, twitchUserRef)

	// The reconnect the failure asks for.
	connectTwitch(t, h, sign, store, "state-reconnect-1")

	st := chatsOn(s.chat, db.PlatformTwitch)
	if len(st) != 1 {
		t.Fatalf("a reconnect left %d Twitch adapters for one account, want exactly 1: %v", len(st), st)
	}
	if now, _ := s.chat.Adapter(db.PlatformTwitch, twitchUserRef); now == failed {
		t.Fatalf("the reconnect kept the adapter whose login was refused; the chip still reads %q", st[0].State)
	}
	waitFor(t, "the reconnected chat to sign in with the new token and report live", func() bool {
		open := irc.open()
		st := chatsOn(s.chat, db.PlatformTwitch)
		return len(open) == 1 && open[0] == "tw-fresh-2" && len(st) == 1 && st[0].State == chat.StateLive
	})

	// Again, over a chat that is working.
	connectTwitch(t, h, sign, store, "state-reconnect-2")
	if st := chatsOn(s.chat, db.PlatformTwitch); len(st) != 1 {
		t.Fatalf("a second reconnect left %d Twitch adapters, want exactly 1: %v", len(st), st)
	}
	waitFor(t, "exactly one open IRC connection, signed in with the newest token", func() bool {
		open := irc.open()
		return len(open) == 1 && open[0] == "tw-fresh-3"
	})
	irc.say("one copy only")
	waitFor(t, "the line to arrive", func() bool { return heard(s.chat, "one copy only") })
}

// TestConnectingAPlatformWithoutCredentialsStartsNoChat is #985's startup rule
// applied to a connect: no developer credentials, no chat.
//
// The way an account arrives for a platform with none is a race: the operator
// removes the credentials while a sign-in is mid-exchange. The callback read
// them before they went, so it still stores the account; the chat must not
// start, or it would be the chip #985 removed, back again.
//
// A guard rather than a regression test: before this change nothing attached
// on connect at all, so it passed then too. Proven by its mutation: drop the
// withCreds check from connectChat. Observed FAIL ("a connect attached chat
// for a platform with no developer credentials").
func TestConnectingAPlatformWithoutCredentialsStartsNoChat(t *testing.T) {
	fake := newFakeConnectPlatform(t)
	irc := newFakeTwitchIRC(t)
	s, h, store, sign := chatConnectServer(t, fake, irc, db.PlatformTwitch)
	startChat(t, s)

	// The fake platform answers on its own goroutine, so the status comes back
	// over a channel rather than through a shared variable.
	removed := make(chan int, 1)
	fake.duringExchange = func() {
		r := jsonRequest(t, http.MethodDelete, "/api/v1/platforms/credentials/twitch", nil)
		sign(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		removed <- w.Code
	}
	connectTwitch(t, h, sign, store, "state-twitch-race")

	if code := <-removed; code != http.StatusOK {
		t.Fatalf("removing the credentials mid-exchange: status %d", code)
	}
	if accountID(t, store, db.PlatformTwitch, twitchUserRef) == 0 {
		t.Fatal("the connect did not store the account, so this test proves nothing about the chat it would start")
	}
	if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 0 {
		t.Fatalf("a connect attached chat for a platform with no developer credentials: %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := irc.accepted(); n != 0 {
		t.Fatalf("Twitch chat was dialled %d times for a platform with no developer credentials", n)
	}
}

// TestAConnectBeforeChatStartsIsLeftToStartChat covers the window before
// StartChat has run, when there is no server lifetime to attach under yet. The
// connect attaches nothing, and StartChat, when it runs, picks the account up
// with every other one. main runs StartChat before it serves a request, so in
// production the window is closed; the order still has to be safe.
func TestAConnectBeforeChatStartsIsLeftToStartChat(t *testing.T) {
	irc := newFakeTwitchIRC(t)
	s, h, store, sign := chatConnectServer(t, newFakeConnectPlatform(t), irc, db.PlatformTwitch)

	connectTwitch(t, h, sign, store, "state-early")
	if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 0 {
		t.Fatalf("a connect before StartChat attached chat with no lifetime to run under: %v", got)
	}

	if n := startChat(t, s); n != 1 {
		t.Fatalf("StartChat attached %d adapters, want the 1 account connected before it ran", n)
	}
	waitFor(t, "the early account's chat to sign in", func() bool { return len(irc.open()) == 1 })
}

// TestAConnectAfterChatHasClosedStillConnects is a connect that lands during
// shutdown, after the Hub has closed. The Hub refuses the attach; the account
// is connected all the same, because a chat that cannot start is a log line,
// never a failed sign-in.
func TestAConnectAfterChatHasClosedStillConnects(t *testing.T) {
	s, h, store, sign := chatConnectServer(t, newFakeConnectPlatform(t), newFakeTwitchIRC(t), db.PlatformTwitch)
	startChat(t, s)
	s.chat.Close()

	connectTwitch(t, h, sign, store, "state-late")
	if accountID(t, store, db.PlatformTwitch, twitchUserRef) == 0 {
		t.Fatal("a connect whose chat could not attach did not store the account")
	}
	if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 0 {
		t.Fatalf("a closed Hub took an adapter: %v", got)
	}
}

// TestAFailedReadLeavesAConnectsChatAsItWasAndStillEndsADisconnects pins what
// the two halves do when the database read under chatWire fails, which is
// deliberately not the same:
//   - a connect leaves the chat as it was. With the row unknown there is nothing
//     to build from, and detaching anyway would trade a chat that may well be
//     working for none over a read that may only have hiccupped.
//   - a disconnect ends the chat anyway. The delete it follows went through,
//     and leaving the chat attached is the bug #985 fixed.
//
// A table renamed out from under the server is the failing read.
func TestAFailedReadLeavesAConnectsChatAsItWasAndStillEndsADisconnects(t *testing.T) {
	connected := func(t *testing.T) (*Server, *db.DB, chat.Adapter) {
		t.Helper()
		s, h, store, sign := chatConnectServer(t, newFakeConnectPlatform(t), newFakeTwitchIRC(t), db.PlatformTwitch)
		startChat(t, s)
		connectTwitch(t, h, sign, store, "state-read")
		a, ok := s.chat.Adapter(db.PlatformTwitch, twitchUserRef)
		if !ok {
			t.Fatal("the connect attached no chat")
		}
		return s, store, a
	}
	breakTable := func(t *testing.T, store *db.DB, table string) {
		t.Helper()
		if _, err := store.SQL().Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_unreadable`); err != nil {
			t.Fatalf("rename %s: %v", table, err)
		}
	}

	for _, table := range []string{"platform_accounts", "platform_creds"} {
		t.Run("a connect whose "+table+" read fails", func(t *testing.T) {
			s, store, before := connected(t)
			breakTable(t, store, table)
			s.attachAccountChat(db.PlatformTwitch, twitchUserRef)
			if now, ok := s.chat.Adapter(db.PlatformTwitch, twitchUserRef); !ok || now != before {
				t.Fatalf("a connect that could not read %s changed the chat (still attached: %v, same adapter: %v)",
					table, ok, now == before)
			}
		})
	}

	t.Run("a disconnect whose re-read fails", func(t *testing.T) {
		s, store, _ := connected(t)
		breakTable(t, store, "platform_accounts")
		s.detachAccountChat(db.PlatformTwitch, twitchUserRef)
		if got := chatsOn(s.chat, db.PlatformTwitch); len(got) != 0 {
			t.Fatalf("a disconnect that could not re-read left the chat attached: %v", got)
		}
	})
}

// TestConnectingXRespectsTheLivestreamGate is the X gate, on a connect exactly
// as at startup: X chat belongs to the Livestream family, so it is attached only
// while POLYEMESIS_X_LIVESTREAM is set.
//
// Gate open fails before this change (nothing attached on connect). Gate closed
// is a guard, proven by its mutation: drop the LivestreamAccess check from
// chatAdapter. Observed FAIL ("X chat was attached with the Livestream gate
// closed").
//
// The app bearer is cleared in both, so the X adapter that is attached refuses
// at once, with its own explanation, and never reaches X.
func TestConnectingXRespectsTheLivestreamGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate string
		want int
	}{
		{"gate closed", "", 0},
		{"gate open", "1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POLYEMESIS_X_LIVESTREAM", tc.gate)
			t.Setenv("POLYEMESIS_X_APP_BEARER", "")
			fake := newFakeConnectPlatform(t)
			s, h, store, sign := chatConnectServer(t, fake, nil, db.PlatformX)
			startChat(t, s)

			if _, err := oauthCallback(h, sign, store, db.PlatformX, "state-x-1"); err != nil {
				t.Fatal(err)
			}
			if accountID(t, store, db.PlatformX, "7777") == 0 {
				t.Fatal("the X connect did not store the account, so this test proves nothing")
			}
			got := chatsOn(s.chat, db.PlatformX)
			if len(got) != tc.want {
				if tc.want == 0 {
					t.Fatalf("X chat was attached with the Livestream gate closed: %v", got)
				}
				t.Fatalf("connecting X with the Livestream gate open attached %d adapters, want %d: %v", len(got), tc.want, got)
			}
		})
	}
}

// TestAConnectAndADisconnectThatRaceLeaveTheHubAgreeingWithTheDatabase pins
// the serialisation. Whatever order a connect and a disconnect of the same
// account land in, the account has a chat exactly when its row exists.
//
// The interleavings that can break it are forced one by one, each through a
// seam at the point it needs, and then the two are raced for real under -race.
//
// Every case runs on two platforms:
//   - Twitch, the platform the report was about.
//   - X, because its adapter is built from the row alone. Twitch's reads its
//     token from the store as it is built, so a row deleted under it fails
//     the build by accident and hides a missing re-read or a missing lock.
//     Facebook, YouTube and Kick build the way X does, so X is the one the
//     mutations below are observed on.
func TestAConnectAndADisconnectThatRaceLeaveTheHubAgreeingWithTheDatabase(t *testing.T) {
	// The X Livestream gate open and no app bearer, so X chat is attached and
	// then refuses at once, without a request, with its own explanation.
	t.Setenv("POLYEMESIS_X_LIVESTREAM", "1")
	t.Setenv("POLYEMESIS_X_APP_BEARER", "")

	for _, acct := range []struct {
		platform db.Platform
		ref      string
	}{
		{db.PlatformTwitch, twitchUserRef},
		{db.PlatformX, "7777"},
	} {
		p, ref := acct.platform, acct.ref

		// agrees fails the test when the Hub and platform_accounts disagree
		// about the account.
		agrees := func(t *testing.T, s *Server, store *db.DB, what string) {
			t.Helper()
			row := accountID(t, store, p, ref) != 0
			chats := chatsOn(s.chat, p)
			if row && len(chats) != 1 {
				t.Fatalf("%s: the account is connected and has %d chat adapters, want 1: %v", what, len(chats), chats)
			}
			if !row && len(chats) != 0 {
				t.Fatalf("%s: the account is gone and its chat is still attached, with nothing left to end it: %v", what, chats)
			}
		}

		connect := func(t *testing.T, h http.Handler, sign func(*http.Request), store *db.DB, state string) {
			t.Helper()
			if _, err := oauthCallback(h, sign, store, p, state); err != nil {
				t.Fatal(err)
			}
		}

		// connected is a server whose account is connected and attached.
		connected := func(t *testing.T) (*Server, http.Handler, *db.DB, func(*http.Request)) {
			t.Helper()
			s, h, store, sign := chatConnectServer(t, newFakeConnectPlatform(t), newFakeTwitchIRC(t), p)
			startChat(t, s)
			connect(t, h, sign, store, "state-first")
			agrees(t, s, store, "after the first connect")
			return s, h, store, sign
		}

		// whileAttaching runs req through the router from inside the connect's
		// attach, on its own goroutine, and gives it a second to finish there.
		// Serialised, it cannot: it waits on chatWire until the attach is done.
		// The returned channel yields its status.
		whileAttaching := func(t *testing.T, s *Server, h http.Handler, sign func(*http.Request), method, path string) <-chan int {
			var once sync.Once
			done := make(chan int, 1)
			s.whileChatAttach = func() {
				once.Do(func() {
					go func() {
						r := jsonRequest(t, method, path, nil)
						sign(r)
						w := httptest.NewRecorder()
						h.ServeHTTP(w, r)
						done <- w.Code
					}()
					select {
					case code := <-done:
						done <- code
					case <-time.After(time.Second):
					}
				})
			}
			return done
		}

		t.Run(string(p), func(t *testing.T) {
			// Mutation: read the row and the credentials before taking chatWire
			// (move both reads above the beforeChatAttach seam). Observed FAIL
			// on x ("the account is gone and its chat is still attached").
			t.Run("the disconnect lands between the stored row and the attach", func(t *testing.T) {
				s, h, store, sign := connected(t)
				id := accountID(t, store, p, ref)
				var once sync.Once
				deleted := 0
				s.beforeChatAttach = func() {
					once.Do(func() { deleted = deleteAccount(t, h, sign, id, nil).Code })
				}
				connect(t, h, sign, store, "state-race-a")
				if deleted != http.StatusOK {
					t.Fatalf("the disconnect: status %d", deleted)
				}
				agrees(t, s, store, "a disconnect between the connect's write and its attach")
			})

			// Mutation: drop the accountByKey check from detachAccountChat
			// (detach unconditionally). Observed FAIL on both ("the account is
			// connected and has 0 chat adapters").
			t.Run("a reconnect lands between the disconnect's delete and its detach", func(t *testing.T) {
				s, h, store, sign := connected(t)
				id := accountID(t, store, p, ref)

				// handleDeleteAccount's two halves with a whole reconnect
				// between them: the row goes, the account is connected again
				// under a new id, then the disconnect ends the chat it read the
				// key for.
				if err := store.DeletePlatformAccount(id); err != nil {
					t.Fatalf("delete: %v", err)
				}
				connect(t, h, sign, store, "state-race-b")
				if accountID(t, store, p, ref) == 0 {
					t.Fatal("the reconnect did not store the account again")
				}
				s.detachAccountChat(p, ref)

				agrees(t, s, store, "a reconnect between a disconnect's delete and its detach")
			})

			// Mutation: drop the chatWire lock from detachAccountChat. Observed
			// FAIL on x ("the account is gone and its chat is still attached").
			t.Run("the disconnect lands while the attach is under way", func(t *testing.T) {
				s, h, store, sign := connected(t)
				id := accountID(t, store, p, ref)
				done := whileAttaching(t, s, h, sign, http.MethodDelete, fmt.Sprintf("/api/v1/platforms/accounts/%d", id))
				connect(t, h, sign, store, "state-race-c")
				if code := <-done; code != http.StatusOK {
					t.Fatalf("the disconnect: status %d", code)
				}
				agrees(t, s, store, "a disconnect during the connect's attach")
			})

			// The same seam with the credentials going instead of the account.
			// The account outlives them, so the rule is #985's: no developer
			// credentials, no chat.
			//
			// Mutation: drop the chatWire lock from detachPlatformChat.
			// Observed FAIL on both ("the platform's credentials are gone and
			// its chat is still attached").
			t.Run("the credentials go while the attach is under way", func(t *testing.T) {
				s, h, store, sign := connected(t)
				done := whileAttaching(t, s, h, sign, http.MethodDelete, "/api/v1/platforms/credentials/"+string(p))
				connect(t, h, sign, store, "state-race-creds")
				if code := <-done; code != http.StatusOK {
					t.Fatalf("removing the credentials: status %d", code)
				}
				if got := chatsOn(s.chat, p); len(got) != 0 {
					t.Fatalf("the platform's credentials are gone and its chat is still attached, with nothing left to end it: %v", got)
				}
			})

			t.Run("racing for real", func(t *testing.T) {
				s, h, store, sign := connected(t)
				for round := range 20 {
					id := accountID(t, store, p, ref)
					if id == 0 {
						connect(t, h, sign, store, fmt.Sprintf("state-restore-%d", round))
						id = accountID(t, store, p, ref)
					}
					var wg sync.WaitGroup
					var connectErr error
					var deleted int
					start := make(chan struct{})
					wg.Add(2)
					go func() {
						defer wg.Done()
						<-start
						_, connectErr = oauthCallback(h, sign, store, p, fmt.Sprintf("state-round-%d", round))
					}()
					go func() {
						defer wg.Done()
						<-start
						r := jsonRequest(t, http.MethodDelete, fmt.Sprintf("/api/v1/platforms/accounts/%d", id), nil)
						sign(r)
						w := httptest.NewRecorder()
						h.ServeHTTP(w, r)
						deleted = w.Code
					}()
					close(start)
					wg.Wait()
					if connectErr != nil {
						t.Fatalf("round %d: connect: %v", round, connectErr)
					}
					if deleted != http.StatusOK && deleted != http.StatusNotFound {
						t.Fatalf("round %d: disconnect: status %d", round, deleted)
					}
					agrees(t, s, store, fmt.Sprintf("round %d", round))
				}
			})
		})
	}
}
