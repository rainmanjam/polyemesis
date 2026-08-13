//go:build ignore

// Driver for acceptance-chat.sh.
//
// Every command here opens a socket to a real platform. That is the entire
// point, and it is what internal/chat's own doc comment says is missing:
//
//	the IRC transport cannot be exercised against Twitch offline, and this
//	package does not pretend otherwise -- there is no test here that proves
//	polyemesis can talk to irc.chat.twitch.tv. [...] The parts that need a
//	socket to a real platform are verified by connecting to one.
//
// That last sentence described a practice nothing performed. This is it.
//
// WHY A REFUSAL IS WORTH TESTING. fatalNotice() classifies Twitch's login
// failure by matching Twitch's own wording -- "login authentication failed" and
// two variants. A unit test feeding it a hand-written NOTICE proves the matcher
// works on the string we imagined. Only a live connection proves it works on
// the string Twitch actually sends, and the cost of being wrong is specific:
// an unrecognised NOTICE is treated as retryable, so polyemesis would retry a
// rejected password every thirty seconds, which the code comment itself says is
// how an IP gets banned.
//
//	go run scripts/acceptance_chat_driver.go <cmd> [args]
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_chat_driver.go <cmd> [args]")
	}
	switch os.Args[1] {
	case "twitch-reach":
		twitchReach()
	case "twitch-refusal":
		twitchRefusal()
	case "twitch-anon":
		twitchAnon()
	case "kick-key":
		kickKey()
	case "kick-verify":
		kickVerify()
	case "twitch-live":
		twitchLive()
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func fail(f string, a ...any) {
	fmt.Printf("ERR "+f+"\n", a...)
	os.Exit(1)
}

// emit prints one key=value result line. The suite greps these, so a command
// that cannot answer prints nothing rather than a plausible zero.
func emit(k string, v any) { fmt.Printf("%s=%v\n", k, v) }

// ---------------------------------------------------------------- twitch

// twitchReach proves the transport before anything is sent over it: DNS, TCP,
// TLS with the right server name, and a certificate chain that verifies
// against the system roots.
//
// Separate from the refusal check because they fail for different reasons and
// a combined check would report a expired-root problem as a protocol problem.
func twitchReach() {
	const addr = "irc.chat.twitch.tv:6697"
	start := time.Now()
	d := &tls.Dialer{Config: &tls.Config{
		ServerName: "irc.chat.twitch.tv",
		MinVersion: tls.VersionTLS12,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		emit("reached", false)
		emit("error", err.Error())
		return
	}
	defer conn.Close()

	st := conn.(*tls.Conn).ConnectionState()
	emit("reached", true)
	emit("tlsVersion", tlsName(st.Version))
	emit("cipherOK", st.CipherSuite != 0)
	emit("chainLen", len(st.PeerCertificates))
	if len(st.PeerCertificates) > 0 {
		leaf := st.PeerCertificates[0]
		emit("certCN", leaf.Subject.CommonName)
		// Days remaining, so a suite can warn before an expiry becomes an
		// outage rather than after.
		emit("certDaysLeft", int(time.Until(leaf.NotAfter).Hours()/24))
	}
	emit("dialMs", time.Since(start).Milliseconds())
}

func tlsName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// twitchRefusal drives the real adapter, against the real server, with a token
// that cannot work -- and asserts that polyemesis understands the rejection.
//
// The token is deliberately well-formed nonsense rather than empty: an empty
// one can fail a length check locally and never reach the wire, which would
// make this test pass without Twitch having been consulted at all.
//
// THE NICK IS NOT justinfan*, AND THAT IS THE WHOLE TEST. Written with one,
// this check ran for 45 seconds and reported no refusal at all -- because
// Twitch treats a justinfan nick as an anonymous reader and ignores the PASS
// line entirely, answering "001 Welcome, GLHF!" to a token that is plainly
// rubbish. A refusal test that cannot produce a refusal is not a test, and this
// one was caught only by running it. See twitchAnon for the other half of that
// discovery, which turned out to be worth more than this check.
func twitchRefusal() {
	tok := "invalidtokeninvalidtokeninvalid0"
	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick:       "polyemesisacceptance",
		Channel:    "twitchpresents",
		AccountRef: "acceptance",
		Token:      tok,
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var got int64
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(chat.Message) { atomic.AddInt64(&got, 1) }))
	elapsed := time.Since(start)

	h := a.Health()
	emit("elapsedMs", elapsed.Milliseconds())
	emit("returned", runErr != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	emit("state", string(h.State))
	emit("messages", atomic.LoadInt64(&got))
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
	// TIMED OUT is its own verdict and must not read as a refusal. If the
	// context expired we were never told anything, which is a different
	// failure from being told no.
	emit("timedOut", ctx.Err() != nil)
}

// twitchLive is the authenticated half, and runs only when a token is present.
// It holds the session open long enough to be more than a handshake: the
// keepalive is what a short connect test cannot reach, and a broken PING/PONG
// looks perfect for the first thirty seconds.
func twitchLive() {
	tok := os.Getenv("TWITCH_CHAT_TOKEN")
	nick := os.Getenv("TWITCH_CHAT_NICK")
	ch := os.Getenv("TWITCH_CHAT_CHANNEL")
	if tok == "" || nick == "" || ch == "" {
		emit("skipped", true)
		return
	}
	hold := 30 * time.Second
	if s := os.Getenv("POLY_CHAT_HOLD"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			hold = d
		}
	}

	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick: nick, Channel: ch, AccountRef: "acceptance", Token: tok,
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var msgs int64
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(chat.Message) { atomic.AddInt64(&msgs, 1) }))
	h := a.Health()

	emit("skipped", false)
	emit("heldMs", time.Since(start).Milliseconds())
	emit("state", string(h.State))
	emit("messages", atomic.LoadInt64(&msgs))
	// The session ending because OUR deadline expired is the success shape.
	// Any other end means Twitch or the transport dropped us.
	emit("endedOnOurDeadline", ctx.Err() != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
}

// twitchAnon is the check this suite exists for, and it needs no credentials.
//
// Twitch accepts any nick matching justinfan* as an anonymous read-only client
// and ignores the PASS line, so the ordinary adapter -- unmodified, still
// sending its PASS -- can join a real channel and receive real chat. That
// exercises the whole read path against live traffic: the IRCv3 line parser,
// tag parsing, the CAP handshake, JOIN, PING/PONG, and message normalisation.
//
// It was written after twitchRefusal failed to refuse. The capability had been
// recorded here as impossible on the grounds that the adapter always sends a
// PASS; that was true and irrelevant, because the server discards it.
//
// MESSAGES ARE REPORTED, NOT REQUIRED. Whether a given channel is talking right
// now is not something this suite controls, so a zero is reported as a zero and
// the caller decides. Connecting and joining are the assertions.
func twitchAnon() {
	ch := os.Getenv("POLY_CHAT_ANON_CHANNEL")
	if ch == "" {
		ch = "twitchpresents"
	}
	hold := 25 * time.Second
	if s := os.Getenv("POLY_CHAT_ANON_HOLD"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			hold = d
		}
	}

	// justinfan plus digits. Fixed rather than random: the whole run is
	// reproducible, and Twitch does not care which one.
	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick:       "justinfan41287",
		Channel:    ch,
		AccountRef: "acceptance-anon",
		Token:      "anonymous",
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var msgs int64
	var first atomic.Value
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()

	// POLLED, because the FINAL state is not the interesting one. Run sets
	// StateStopped as it returns, so reading Health() afterwards reports
	// "stopped" whether the session joined a channel or never got past the
	// handshake -- which is how the first version of this check reported a
	// successful anonymous join as though nothing had happened.
	var everLive atomic.Bool
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if a.Health().State == chat.StateLive {
					everLive.Store(true)
				}
			}
		}
	}()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(m chat.Message) {
		if atomic.AddInt64(&msgs, 1) == 1 {
			first.Store(m)
		}
	}))
	cancel()
	<-pollDone

	emit("channel", ch)
	emit("heldMs", time.Since(start).Milliseconds())
	// The assertion that does not depend on anyone talking: we joined.
	emit("joined", everLive.Load())
	emit("messages", atomic.LoadInt64(&msgs))
	emit("endedOnOurDeadline", ctx.Err() != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	// A parsed message proves more than a counter does: an author and a body
	// mean the tag parser and the normaliser both ran on real bytes.
	if v := first.Load(); v != nil {
		m := v.(chat.Message)
		emit("firstAuthorLen", len(m.Author.Name))
		emit("firstTextLen", len(m.Text))
		emit("firstHasID", m.ID != "")
	}
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
}

// ------------------------------------------------------------------ kick

// kickKey fetches Kick's published webhook public key over the real network
// and parses it. Nothing here is secret -- that is the point of a public key --
// so this needs no credentials and belongs in the always-run tier.
//
// It matters because the key is fetched at runtime by design: kick_verify.go
// records that a rotation cannot be fixed short of a new binary if it were
// compiled in. A URL that starts 404ing is therefore a silent break in webhook
// verification, and nothing else would notice.
func kickKey() {
	f := &chat.KickKeyFetcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	pub, err := f.Key(ctx)
	if err != nil {
		emit("fetched", false)
		emit("error", err.Error())
		return
	}
	emit("fetched", true)
	emit("fetchMs", time.Since(start).Milliseconds())
	emit("bits", pub.N.BitLen())
	emit("exponent", pub.E)
	emit("url", chat.KickPublicKeyURL)
}

// kickVerify checks the verifier against the REAL key, both ways round.
//
// We cannot produce a valid Kick signature -- that would need Kick's private
// key -- so the positive case is built with a keypair of our own, and the
// negative case is that same signature presented against Kick's real key.
//
// Both directions are needed. A verifier that accepted everything would pass a
// positive-only test; a verifier that rejected everything would pass a
// negative-only one. Neither alone says the thing works.
func kickVerify() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	realKey, err := (&chat.KickKeyFetcher{}).Key(ctx)
	if err != nil {
		emit("fetched", false)
		emit("error", err.Error())
		return
	}
	emit("fetched", true)

	ourKey, err := chat.ParseKickPublicKey([]byte(fixturePublicKeyPEM))
	if err != nil {
		fail("parsing the fixture key: %v", err)
	}

	req := func(sigB64 string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/hooks/kick", strings.NewReader(fixtureBody))
		r.Header.Set("Kick-Event-Message-Id", fixtureMessageID)
		r.Header.Set("Kick-Event-Message-Timestamp", fixtureTimestamp)
		r.Header.Set("Kick-Event-Signature", sigB64)
		return r
	}
	body := []byte(fixtureBody)

	// 1. The fixture signature against the key that made it: must pass, or the
	//    verifier is broken in a way that would reject genuine Kick traffic too.
	emit("ourSigOurKey", chat.VerifyKickSignature(ourKey, req(fixtureSignature), body) == nil)
	// 2. The same signature against KICK'S real fetched key: must fail. This is
	//    the check that proves verification happens against the key we fetched
	//    rather than against whatever was passed in.
	emit("ourSigKickKey", chat.VerifyKickSignature(realKey, req(fixtureSignature), body) == nil)
	// 3. A tampered body: must fail, proving the body is inside the signed
	//    material rather than merely alongside it.
	emit("tamperedBody", chat.VerifyKickSignature(ourKey, req(fixtureSignature), []byte(`{"event":"x"}`)) == nil)
	// 4. Garbage in the signature header: must fail without panicking.
	emit("garbageSig", chat.VerifyKickSignature(ourKey, req("!!!not-base64!!!"), body) == nil)
}

// A FIXED TEST VECTOR RATHER THAN A KEYPAIR MINTED EACH RUN.
//
// Signing here would mean calling rsa.SignPKCS1v15, and a scanner will rate that
// CRITICAL for using PKCS#1 v1.5 padding instead of PSS. The rule is right in
// general and cannot be satisfied here: Kick signs its webhooks with v1.5, so
// kick_verify.go must call VerifyPKCS1v15, and a driver that signed with PSS
// would exercise a code path that never runs in production.
//
// A precomputed vector removes the signing call without weakening any of the
// four checks above, and is what one would want anyway -- the same bytes every
// run, and no 2048-bit key generation on the clock.
//
// SAFE TO PIN because VerifyKickSignature does not check timestamp freshness:
// the timestamp is signed material, not a validity window, so this vector
// verifies identically in ten years. If that ever changes, this fixture stops
// verifying and check 1 fails loudly, which is the right way to find out.
//
// The private key was discarded at generation and is in no repository. To
// regenerate: sign sha256("<id>.<timestamp>.<body>") with a fresh 2048-bit key
// and replace all four constants together.
const (
	fixtureMessageID = "acceptance-1"
	fixtureTimestamp = "2026-01-01T00:00:00Z"
	fixtureBody      = `{"event":"chat.message.sent","data":{"content":"acceptance"}}`
	fixtureSignature = "jClO6Byrix7VKC8fgIGG0xfjBviRjXNaEEBpVHqYP1mAq0zlpSQ4S+2CkoHnv+wH+x441u4zeIVWeJ773AaD2MgbIMJVlp6SH1IWvKDQ5ejsgLbv3PJJ5YYSbXN294pw/wswLbsEEPCIXztIPeIJIAQkPZd8VvG0vjq23wWoSpFxDRNm1E3AcvXFwwvM+fDmWVmbCRsSw29pTkbS/6F33SqIWhkIU9A0OK8EbeYNNh+fNDoNABfDR7Pdv8NJqxqvv5XFrQNR0hx/3nTROhZUIOqeEEmAWG0ix81zpywIIajWvQzY42H+pPIKBcXcnStuwql088NdjAcjsHJqyowK+g=="

	fixturePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtY262V+RYGOCCCV7pkEj
dMZgcC0Wx9Vs+ISZUK7MhApBYgxRuojeZbZijqi8lwOF0EdQywmublvgpT+hpaSv
Wihaf5V3x2KDYGDKtSl5JmUBEu5xRRLvhbGKtgjk+0aXCvmPu5AndMZBTzAexFV3
gcbCFx1LitFJ6QDWp1Lz13IatAGsfL96S6LugtKGUrRYDBePPYY7skML44+Ggd8R
1F/GtApSw3f1RwULNjeCjGtEQmY3erabTtC6XlidyIBSJ6/ApHfZSUZyRS3intOC
x+WlEf8lKiQE+dVRQHstQZ9UrK5kKVKcgD1cbEzfkKyM63qXhIbmHyLNuU5v8Knc
9QIDAQAB
-----END PUBLIC KEY-----
`
)
